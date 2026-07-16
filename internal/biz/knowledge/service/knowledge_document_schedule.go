package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go-base-agent/internal/biz/crawler"
	"go-base-agent/internal/biz/knowledge/model"
	"go-base-agent/internal/biz/knowledge/repo"
	"go-base-agent/internal/framework/config"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

type scheduleFileWriter interface {
	Put(docID string, name string, data []byte)
}

type scheduleChunkStarter interface {
	StartChunk(ctx context.Context, docID string, userID string) error
}

// DocumentScheduleService 执行知识库文档定时刷新。
type DocumentScheduleService struct {
	db           *gorm.DB
	docRepo      *repo.KnowledgeDocumentRepo
	scheduleRepo *repo.KnowledgeDocumentScheduleRepo
	fileStore    scheduleFileWriter
	chunkStarter scheduleChunkStarter
	cfg          config.RAGKnowledgeScheduleConfig
	sources      map[string]crawler.Source
	owner        string
	now          func() time.Time
	parser       cron.Parser
}

// NewDocumentScheduleService 创建文档定时刷新服务。
func NewDocumentScheduleService(
	db *gorm.DB,
	docRepo *repo.KnowledgeDocumentRepo,
	scheduleRepo *repo.KnowledgeDocumentScheduleRepo,
	fileStore scheduleFileWriter,
	chunkStarter scheduleChunkStarter,
	cfg config.RAGKnowledgeScheduleConfig,
) *DocumentScheduleService {
	host, _ := os.Hostname()
	if strings.TrimSpace(host) == "" {
		host = "ragent"
	}
	return &DocumentScheduleService{
		db:           db,
		docRepo:      docRepo,
		scheduleRepo: scheduleRepo,
		fileStore:    fileStore,
		chunkStarter: chunkStarter,
		cfg:          cfg,
		sources:      make(map[string]crawler.Source),
		owner:        fmt.Sprintf("%s-%d", host, os.Getpid()),
		now:          time.Now,
		parser: cron.NewParser(
			cron.SecondOptional |
				cron.Minute |
				cron.Hour |
				cron.Dom |
				cron.Month |
				cron.Dow |
				cron.Descriptor,
		),
	}
}

// RegisterSource 注册一个远程文档来源。
func (s *DocumentScheduleService) RegisterSource(source crawler.Source) {
	if source == nil {
		return
	}
	s.sources[strings.ToLower(strings.TrimSpace(source.Name()))] = source
}

// Run 定时扫描到期文档刷新任务。
func (s *DocumentScheduleService) Run(ctx context.Context) {
	delay := time.Duration(s.cfg.ScanDelayMs) * time.Millisecond
	if delay <= 0 {
		delay = 10 * time.Second
	}
	ticker := time.NewTicker(delay)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.ScanDue(ctx); err != nil {
				slog.Warn("knowledge document schedule scan failed", "err", err)
			}
		}
	}
}

// ScanDue 扫描并执行到期的文档刷新任务。
func (s *DocumentScheduleService) ScanDue(ctx context.Context) (int, error) {
	if s == nil || s.db == nil || s.scheduleRepo == nil || s.docRepo == nil || s.fileStore == nil || s.chunkStarter == nil {
		return 0, nil
	}
	now := s.now()
	lockSeconds := s.cfg.LockSeconds
	if lockSeconds <= 0 {
		lockSeconds = 900
	}
	schedules, err := s.scheduleRepo.ClaimDue(ctx, now, s.cfg.BatchSize, s.owner, now.Add(time.Duration(lockSeconds)*time.Second))
	if err != nil {
		return 0, fmt.Errorf("claim due document schedules: %w", err)
	}
	for _, schedule := range schedules {
		if err := s.refreshOne(ctx, schedule, now); err != nil {
			slog.Warn("knowledge document schedule refresh failed", "scheduleId", schedule.ID, "docId", schedule.DocID, "err", err)
		}
	}
	return len(schedules), nil
}

func (s *DocumentScheduleService) refreshOne(ctx context.Context, schedule model.KnowledgeDocumentSchedule, startedAt time.Time) error {
	doc, err := s.docRepo.FindByID(ctx, schedule.DocID)
	if err != nil {
		return s.markFailed(ctx, schedule, startedAt, fmt.Errorf("find document: %w", err), nil)
	}
	if doc.ScheduleEnabled != 1 || schedule.Enabled != 1 {
		return s.releaseSchedule(ctx, schedule.ID, map[string]any{"last_status": "skipped"})
	}
	source := s.sourceForDocument(doc)
	if source == nil {
		return s.markFailed(ctx, schedule, startedAt, fmt.Errorf("document source %q is not registered", doc.SourceType), nil)
	}
	location := firstNonEmpty(documentSourceURL(doc), doc.SourceLocation, doc.FileURL)
	if strings.TrimSpace(location) == "" {
		return s.markFailed(ctx, schedule, startedAt, fmt.Errorf("document source location is empty"), nil)
	}
	remoteDoc, err := source.FetchDocument(ctx, location)
	if err != nil {
		return s.markFailed(ctx, schedule, startedAt, fmt.Errorf("fetch remote document: %w", err), nil)
	}
	if remoteDoc == nil || len(remoteDoc.Content) == 0 {
		return s.markFailed(ctx, schedule, startedAt, fmt.Errorf("remote document content is empty"), remoteDoc)
	}

	contentHash := sha256Hex(remoteDoc.Content)
	fileName := firstNonEmpty(remoteDoc.Meta.Title, doc.DocName, filepath.Base(location))
	etag := remoteDoc.Meta.Extra["etag"]
	lastModified := remoteDoc.Meta.Extra["last_modified"]
	unchanged := contentHash == schedule.LastContentHash && schedule.LastContentHash != ""
	if !unchanged {
		s.fileStore.Put(doc.ID, fileName, remoteDoc.Content)
		if err := s.updateDocumentAfterFetch(ctx, doc, remoteDoc, fileName); err != nil {
			return s.markFailed(ctx, schedule, startedAt, err, remoteDoc)
		}
		if err := s.chunkStarter.StartChunk(ctx, doc.ID, doc.CreatedBy); err != nil {
			return s.markFailed(ctx, schedule, startedAt, fmt.Errorf("start chunk: %w", err), remoteDoc)
		}
	}

	nextRun, err := s.nextRunTime(schedule.CronExpr, startedAt)
	if err != nil {
		return s.markFailed(ctx, schedule, startedAt, fmt.Errorf("parse cron: %w", err), remoteDoc)
	}
	now := s.now()
	updates := map[string]any{
		"last_run_time":     startedAt,
		"last_success_time": now,
		"last_status":       "success",
		"last_error":        "",
		"last_etag":         etag,
		"last_modified":     lastModified,
		"last_content_hash": contentHash,
		"next_run_time":     nextRun,
		"lock_owner":        "",
		"lock_until":        nil,
		"update_time":       now,
	}
	if err := s.scheduleRepo.UpdateByID(ctx, schedule.ID, updates); err != nil {
		return fmt.Errorf("update schedule success: %w", err)
	}
	message := "refreshed"
	if unchanged {
		message = "unchanged"
	}
	return s.recordExec(ctx, schedule, "success", message, startedAt, now, remoteDoc, contentHash)
}

func (s *DocumentScheduleService) updateDocumentAfterFetch(ctx context.Context, doc *model.KnowledgeDocument, remoteDoc *crawler.Document, fileName string) error {
	updates := map[string]any{
		"doc_name":    fileName,
		"file_size":   int64(len(remoteDoc.Content)),
		"update_time": s.now(),
	}
	if remoteDoc.Meta.MimeType != "" {
		if fileType := fileTypeFromMimeOrName(remoteDoc.Meta.MimeType, fileName); fileType != "" {
			updates["file_type"] = fileType
		}
	}
	if remoteDoc.Meta.URL != "" {
		updates["source_location"] = remoteDoc.Meta.URL
		updates["file_url"] = remoteDoc.Meta.URL
	}
	if err := s.db.WithContext(ctx).Model(&model.KnowledgeDocument{}).Where("id = ?", doc.ID).Updates(updates).Error; err != nil {
		return fmt.Errorf("update fetched document: %w", err)
	}
	return nil
}

func (s *DocumentScheduleService) markFailed(ctx context.Context, schedule model.KnowledgeDocumentSchedule, startedAt time.Time, cause error, remoteDoc *crawler.Document) error {
	now := s.now()
	nextRun := startedAt.Add(time.Duration(maxInt(s.cfg.MinIntervalSeconds, 60)) * time.Second)
	_ = s.scheduleRepo.UpdateByID(ctx, schedule.ID, map[string]any{
		"last_run_time": startedAt,
		"last_status":   "failed",
		"last_error":    truncateString(cause.Error(), 512),
		"next_run_time": nextRun,
		"lock_owner":    "",
		"lock_until":    nil,
		"update_time":   now,
	})
	var contentHash string
	if remoteDoc != nil {
		contentHash = sha256Hex(remoteDoc.Content)
	}
	_ = s.recordExec(ctx, schedule, "failed", truncateString(cause.Error(), 512), startedAt, now, remoteDoc, contentHash)
	return cause
}

func (s *DocumentScheduleService) releaseSchedule(ctx context.Context, scheduleID string, extra map[string]any) error {
	if extra == nil {
		extra = make(map[string]any)
	}
	extra["lock_owner"] = ""
	extra["lock_until"] = nil
	extra["update_time"] = s.now()
	return s.scheduleRepo.UpdateByID(ctx, scheduleID, extra)
}

func (s *DocumentScheduleService) recordExec(ctx context.Context, schedule model.KnowledgeDocumentSchedule, status, message string, start, end time.Time, remoteDoc *crawler.Document, contentHash string) error {
	exec := &model.KnowledgeDocumentScheduleExec{
		ScheduleID:  schedule.ID,
		DocID:       schedule.DocID,
		KbID:        schedule.KbID,
		Status:      status,
		Message:     truncateString(message, 512),
		StartTime:   &start,
		EndTime:     &end,
		ContentHash: contentHash,
	}
	if remoteDoc != nil {
		exec.FileName = remoteDoc.Meta.Title
		exec.FileSize = int64(len(remoteDoc.Content))
		exec.ETag = remoteDoc.Meta.Extra["etag"]
		exec.LastModified = remoteDoc.Meta.Extra["last_modified"]
	}
	return s.scheduleRepo.CreateExec(ctx, exec)
}

func (s *DocumentScheduleService) nextRunTime(expr string, from time.Time) (time.Time, error) {
	parsed, err := s.parser.Parse(strings.TrimSpace(expr))
	if err != nil {
		return time.Time{}, err
	}
	next := parsed.Next(from)
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("cron expression has no next run")
	}
	minInterval := s.cfg.MinIntervalSeconds
	if minInterval > 0 && next.Before(from.Add(time.Duration(minInterval)*time.Second)) {
		next = from.Add(time.Duration(minInterval) * time.Second)
	}
	return next, nil
}

func (s *DocumentScheduleService) sourceForDocument(doc *model.KnowledgeDocument) crawler.Source {
	keys := []string{
		strings.ToLower(strings.TrimSpace(doc.SourceType)),
	}
	location := firstNonEmpty(doc.SourceLocation, doc.FileURL)
	if isConfluenceDocumentURL(location) {
		keys = append([]string{"confluence"}, keys...)
	}
	if isFeishuDocumentURL(location) {
		keys = append([]string{"feishu"}, keys...)
	}
	if parsed, err := url.Parse(location); err == nil {
		if parsed.Scheme == "http" || parsed.Scheme == "https" {
			keys = append(keys, "url", "http")
		}
	}
	for _, key := range keys {
		if source := s.sources[key]; source != nil {
			return source
		}
	}
	return nil
}

func isFeishuDocumentURL(location string) bool {
	parsed, err := url.Parse(strings.TrimSpace(location))
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Host)
	path := strings.ToLower(parsed.Path)
	return strings.Contains(host, "feishu.cn") && (strings.Contains(path, "/wiki/") || strings.Contains(path, "/docx/") || strings.Contains(path, "/docs/"))
}

func isConfluenceDocumentURL(location string) bool {
	parsed, err := url.Parse(strings.TrimSpace(location))
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Host)
	path := strings.ToLower(parsed.Path)
	return (strings.Contains(path, "/wiki/spaces/") && strings.Contains(path, "/pages/")) ||
		(strings.Contains(host, "atlassian.net") && strings.Contains(path, "/wiki/"))
}

func sha256Hex(data []byte) string {
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash[:])
}

func fileTypeFromMimeOrName(mimeType, fileName string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "text/markdown":
		return "md"
	case "text/html":
		return "html"
	case "text/plain":
		return "txt"
	case "application/pdf":
		return "pdf"
	case "application/json":
		return "json"
	}
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(fileName)), ".")
}

func truncateString(value string, maxLen int) string {
	if maxLen <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxLen {
		return value
	}
	return string(runes[:maxLen])
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
