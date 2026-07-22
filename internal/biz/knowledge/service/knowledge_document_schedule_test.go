package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"go-base-agent/internal/biz/crawler"
	knowledgeModel "go-base-agent/internal/biz/knowledge/model"
	knowledgeRepo "go-base-agent/internal/biz/knowledge/repo"
	"go-base-agent/internal/framework/config"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeScheduleSource struct {
	name      string
	fetchedID string
	doc       *crawler.Document
	onFetch   func()
}

func (f *fakeScheduleSource) Name() string {
	if f.name != "" {
		return f.name
	}
	return "url"
}

func (f *fakeScheduleSource) ListDocuments(context.Context) ([]crawler.DocumentMeta, error) {
	return nil, nil
}

func (f *fakeScheduleSource) FetchDocument(_ context.Context, id string) (*crawler.Document, error) {
	f.fetchedID = id
	if f.onFetch != nil {
		f.onFetch()
	}
	return f.doc, nil
}

func (f *fakeScheduleSource) WatchChanges(context.Context, time.Time) (<-chan crawler.ChangeEvent, error) {
	ch := make(chan crawler.ChangeEvent)
	close(ch)
	return ch, nil
}

type fakeScheduleFileStore struct {
	docID string
	name  string
	data  []byte
}

func (f *fakeScheduleFileStore) Put(docID string, name string, data []byte) {
	f.docID = docID
	f.name = name
	f.data = append([]byte(nil), data...)
}

type fakeScheduleChunkStarter struct {
	docID  string
	userID string
}

func (f *fakeScheduleChunkStarter) StartChunk(_ context.Context, docID string, userID string) error {
	f.docID = docID
	f.userID = userID
	return nil
}

type fakeScheduleChunkRunner struct {
	docID     string
	userID    string
	runErr    error
	runCalled bool
}

func (f *fakeScheduleChunkRunner) StartChunk(_ context.Context, docID string, userID string) error {
	f.docID = docID
	f.userID = userID
	return nil
}

func (f *fakeScheduleChunkRunner) RunChunkNow(_ context.Context, docID string, userID string) error {
	f.runCalled = true
	f.docID = docID
	f.userID = userID
	return f.runErr
}

func TestDocumentScheduleService_ScanDueFetchesRemoteDocumentAndStartsChunk(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
		&knowledgeModel.KnowledgeDocumentScheduleExec{},
	); err != nil {
		t.Fatalf("migrate schedule tables: %v", err)
	}

	now := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:            "kb-1",
		DocName:         "old.md",
		FileURL:         "https://example.com/member-agent.md",
		FileType:        "md",
		SourceType:      "url",
		SourceLocation:  "https://example.com/member-agent.md",
		ScheduleEnabled: 1,
		ScheduleCron:    "@every 1h",
		Status:          "success",
		CreatedBy:       "user-1",
	}
	doc.ID = "doc-1"
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	schedule := &knowledgeModel.KnowledgeDocumentSchedule{
		DocID:       doc.ID,
		KbID:        doc.KbID,
		CronExpr:    "@every 1h",
		Enabled:     1,
		NextRunTime: ptrTime(now.Add(-time.Minute)),
	}
	if err := gdb.Create(schedule).Error; err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	fileStore := &fakeScheduleFileStore{}
	chunkStarter := &fakeScheduleChunkStarter{}
	source := &fakeScheduleSource{doc: &crawler.Document{
		Meta: crawler.DocumentMeta{
			ID:         doc.SourceLocation,
			Title:      "member-agent.md",
			URL:        doc.SourceLocation,
			MimeType:   "text/markdown",
			Size:       34,
			SourceName: "url",
			UpdatedAt:  now,
			Extra:      map[string]string{"etag": "etag-1", "last_modified": "Thu, 16 Jul 2026 10:00:00 GMT"},
		},
		Content: []byte("会员 Agent 支持权益查询和积分查询。"),
	}}

	svc := NewDocumentScheduleService(
		gdb,
		knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		fileStore,
		chunkStarter,
		config.RAGKnowledgeScheduleConfig{BatchSize: 10, LockSeconds: 60},
	)
	svc.now = func() time.Time { return now }
	svc.RegisterSource(source)

	count, err := svc.ScanDue(context.Background())
	if err != nil {
		t.Fatalf("scan due: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 processed schedule, got %d", count)
	}
	if source.fetchedID != doc.SourceLocation {
		t.Fatalf("expected fetch %q, got %q", doc.SourceLocation, source.fetchedID)
	}
	if fileStore.docID != doc.ID || fileStore.name != "member-agent.md" || string(fileStore.data) != string(source.doc.Content) {
		t.Fatalf("unexpected stored file: doc=%s name=%s data=%s", fileStore.docID, fileStore.name, string(fileStore.data))
	}
	if chunkStarter.docID != doc.ID || chunkStarter.userID != doc.CreatedBy {
		t.Fatalf("expected chunk start doc/user, got %s/%s", chunkStarter.docID, chunkStarter.userID)
	}

	var updated knowledgeModel.KnowledgeDocumentSchedule
	if err := gdb.First(&updated, "doc_id = ?", doc.ID).Error; err != nil {
		t.Fatalf("find updated schedule: %v", err)
	}
	if updated.LastStatus != "success" || updated.LastSuccessTime == nil || updated.LastContentHash == "" || updated.NextRunTime == nil {
		t.Fatalf("schedule not marked success: %+v", updated)
	}
	if !updated.NextRunTime.After(now) {
		t.Fatalf("expected next run after now, got %v", updated.NextRunTime)
	}

	var execCount int64
	if err := gdb.Model(&knowledgeModel.KnowledgeDocumentScheduleExec{}).Where("doc_id = ? AND status = ?", doc.ID, "success").Count(&execCount).Error; err != nil {
		t.Fatalf("count schedule exec: %v", err)
	}
	if execCount != 1 {
		t.Fatalf("expected one success exec log, got %d", execCount)
	}
}

func TestDocumentScheduleService_ScanDueMarksSkippedWhenRemoteDocumentUnchanged(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
		&knowledgeModel.KnowledgeDocumentScheduleExec{},
	); err != nil {
		t.Fatalf("migrate schedule tables: %v", err)
	}

	now := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	content := []byte("unchanged content")
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:            "kb-1",
		DocName:         "same.md",
		FileURL:         "https://example.com/same.md",
		FileType:        "md",
		SourceType:      "url",
		SourceLocation:  "https://example.com/same.md",
		ScheduleEnabled: 1,
		ScheduleCron:    "@every 1h",
		Status:          "success",
		CreatedBy:       "user-1",
	}
	doc.ID = "doc-unchanged"
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	schedule := &knowledgeModel.KnowledgeDocumentSchedule{
		DocID:           doc.ID,
		KbID:            doc.KbID,
		CronExpr:        "@every 1h",
		Enabled:         1,
		LastContentHash: sha256Hex(content),
		NextRunTime:     ptrTime(now.Add(-time.Minute)),
	}
	if err := gdb.Create(schedule).Error; err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	fileStore := &fakeScheduleFileStore{}
	chunkStarter := &fakeScheduleChunkStarter{}
	source := &fakeScheduleSource{doc: &crawler.Document{
		Meta: crawler.DocumentMeta{
			Title:    "same.md",
			URL:      doc.SourceLocation,
			MimeType: "text/markdown",
			Extra:    map[string]string{"etag": "etag-2", "last_modified": "Thu, 16 Jul 2026 10:00:00 GMT"},
		},
		Content: content,
	}}
	svc := NewDocumentScheduleService(
		gdb,
		knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		fileStore,
		chunkStarter,
		config.RAGKnowledgeScheduleConfig{BatchSize: 10, LockSeconds: 60},
	)
	svc.now = func() time.Time { return now }
	svc.RegisterSource(source)

	count, err := svc.ScanDue(context.Background())
	if err != nil {
		t.Fatalf("scan due: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 processed schedule, got %d", count)
	}
	if fileStore.docID != "" || chunkStarter.docID != "" {
		t.Fatalf("unchanged document should not be stored or chunked, stored=%s chunked=%s", fileStore.docID, chunkStarter.docID)
	}

	var updated knowledgeModel.KnowledgeDocumentSchedule
	if err := gdb.First(&updated, "doc_id = ?", doc.ID).Error; err != nil {
		t.Fatalf("find updated schedule: %v", err)
	}
	if updated.LastStatus != "skipped" || updated.LastSuccessTime != nil || updated.NextRunTime == nil {
		t.Fatalf("schedule not marked skipped: %+v", updated)
	}
	if updated.LastETag != "etag-2" || updated.LastModified == "" {
		t.Fatalf("schedule did not retain remote validators: %+v", updated)
	}

	var exec knowledgeModel.KnowledgeDocumentScheduleExec
	if err := gdb.First(&exec, "doc_id = ?", doc.ID).Error; err != nil {
		t.Fatalf("find schedule exec: %v", err)
	}
	if exec.Status != "skipped" || exec.ContentHash != sha256Hex(content) || exec.EndTime == nil {
		t.Fatalf("exec not marked skipped: %+v", exec)
	}
}

func TestDocumentScheduleService_ScanDueSkipsWhenDocumentIsRunning(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
		&knowledgeModel.KnowledgeDocumentScheduleExec{},
	); err != nil {
		t.Fatalf("migrate schedule tables: %v", err)
	}

	now := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:            "kb-1",
		DocName:         "running.md",
		FileURL:         "https://example.com/running.md",
		FileType:        "md",
		SourceType:      "url",
		SourceLocation:  "https://example.com/running.md",
		ScheduleEnabled: 1,
		ScheduleCron:    "@every 1h",
		Status:          "running",
		CreatedBy:       "user-1",
	}
	doc.ID = "doc-running"
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	schedule := &knowledgeModel.KnowledgeDocumentSchedule{
		DocID:       doc.ID,
		KbID:        doc.KbID,
		CronExpr:    "@every 1h",
		Enabled:     1,
		NextRunTime: ptrTime(now.Add(-time.Minute)),
	}
	if err := gdb.Create(schedule).Error; err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	fileStore := &fakeScheduleFileStore{}
	chunkStarter := &fakeScheduleChunkStarter{}
	source := &fakeScheduleSource{doc: &crawler.Document{
		Meta: crawler.DocumentMeta{
			Title:    "running.md",
			URL:      doc.SourceLocation,
			MimeType: "text/markdown",
			Extra:    map[string]string{"etag": "etag-running"},
		},
		Content: []byte("new remote content"),
	}}
	svc := NewDocumentScheduleService(
		gdb,
		knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		fileStore,
		chunkStarter,
		config.RAGKnowledgeScheduleConfig{BatchSize: 10, LockSeconds: 60},
	)
	svc.now = func() time.Time { return now }
	svc.RegisterSource(source)

	count, err := svc.ScanDue(context.Background())
	if err != nil {
		t.Fatalf("scan due: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 processed schedule, got %d", count)
	}
	if fileStore.docID != "" || chunkStarter.docID != "" {
		t.Fatalf("running document should not be stored or chunked, stored=%s chunked=%s", fileStore.docID, chunkStarter.docID)
	}

	var updated knowledgeModel.KnowledgeDocumentSchedule
	if err := gdb.First(&updated, "doc_id = ?", doc.ID).Error; err != nil {
		t.Fatalf("find updated schedule: %v", err)
	}
	if updated.LastStatus != "skipped" || updated.LastError == "" || updated.NextRunTime == nil {
		t.Fatalf("schedule not marked skipped: %+v", updated)
	}
	var exec knowledgeModel.KnowledgeDocumentScheduleExec
	if err := gdb.First(&exec, "doc_id = ?", doc.ID).Error; err != nil {
		t.Fatalf("find schedule exec: %v", err)
	}
	if exec.Status != "skipped" || exec.Message == "" || exec.EndTime == nil {
		t.Fatalf("exec not marked skipped: %+v", exec)
	}
}

func TestDocumentScheduleService_ScanDueDoesNotOverwriteScheduleWhenLeaseLost(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
		&knowledgeModel.KnowledgeDocumentScheduleExec{},
	); err != nil {
		t.Fatalf("migrate schedule tables: %v", err)
	}

	now := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:            "kb-1",
		DocName:         "lease.md",
		FileURL:         "https://example.com/lease.md",
		FileType:        "md",
		SourceType:      "url",
		SourceLocation:  "https://example.com/lease.md",
		ScheduleEnabled: 1,
		ScheduleCron:    "@every 1h",
		Status:          "success",
		CreatedBy:       "user-1",
	}
	doc.ID = "doc-lease"
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	schedule := &knowledgeModel.KnowledgeDocumentSchedule{
		DocID:       doc.ID,
		KbID:        doc.KbID,
		CronExpr:    "@every 1h",
		Enabled:     1,
		NextRunTime: ptrTime(now.Add(-time.Minute)),
	}
	schedule.ID = "schedule-lease"
	if err := gdb.Create(schedule).Error; err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	source := &fakeScheduleSource{
		doc: &crawler.Document{
			Meta: crawler.DocumentMeta{
				Title:    "lease.md",
				URL:      doc.SourceLocation,
				MimeType: "text/markdown",
				Extra:    map[string]string{"etag": "etag-lease"},
			},
			Content: []byte("new lease content"),
		},
		onFetch: func() {
			if err := gdb.Model(&knowledgeModel.KnowledgeDocumentSchedule{}).
				Where("id = ?", schedule.ID).
				Update("lock_owner", "other-worker").Error; err != nil {
				t.Fatalf("steal lock: %v", err)
			}
		},
	}
	svc := NewDocumentScheduleService(
		gdb,
		knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		&fakeScheduleFileStore{},
		&fakeScheduleChunkStarter{},
		config.RAGKnowledgeScheduleConfig{BatchSize: 10, LockSeconds: 60},
	)
	svc.now = func() time.Time { return now }
	svc.RegisterSource(source)

	count, err := svc.ScanDue(context.Background())
	if err != nil {
		t.Fatalf("scan due: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 processed schedule, got %d", count)
	}

	var updated knowledgeModel.KnowledgeDocumentSchedule
	if err := gdb.First(&updated, "id = ?", schedule.ID).Error; err != nil {
		t.Fatalf("find updated schedule: %v", err)
	}
	if updated.LockOwner != "other-worker" || updated.LastStatus != "" || updated.LastSuccessTime != nil {
		t.Fatalf("stale worker overwrote schedule: %+v", updated)
	}
	var exec knowledgeModel.KnowledgeDocumentScheduleExec
	if err := gdb.First(&exec, "doc_id = ?", doc.ID).Error; err != nil {
		t.Fatalf("find schedule exec: %v", err)
	}
	if exec.Status != "success" || !strings.Contains(exec.Message, scheduleLeaseLostNote) {
		t.Fatalf("exec should retain lease lost note: %+v", exec)
	}
}

func TestDocumentScheduleService_ScanDueUsesSynchronousChunkRunnerWhenAvailable(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
		&knowledgeModel.KnowledgeDocumentScheduleExec{},
	); err != nil {
		t.Fatalf("migrate schedule tables: %v", err)
	}

	now := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:            "kb-1",
		DocName:         "sync.md",
		FileURL:         "https://example.com/sync.md",
		FileType:        "md",
		SourceType:      "url",
		SourceLocation:  "https://example.com/sync.md",
		ScheduleEnabled: 1,
		ScheduleCron:    "@every 1h",
		Status:          "success",
		CreatedBy:       "user-1",
	}
	doc.ID = "doc-sync"
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	schedule := &knowledgeModel.KnowledgeDocumentSchedule{
		DocID:       doc.ID,
		KbID:        doc.KbID,
		CronExpr:    "@every 1h",
		Enabled:     1,
		NextRunTime: ptrTime(now.Add(-time.Minute)),
	}
	if err := gdb.Create(schedule).Error; err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	fileStore := &fakeScheduleFileStore{}
	runner := &fakeScheduleChunkRunner{runErr: context.DeadlineExceeded}
	source := &fakeScheduleSource{doc: &crawler.Document{
		Meta: crawler.DocumentMeta{
			Title:    "sync.md",
			URL:      doc.SourceLocation,
			MimeType: "text/markdown",
			Extra:    map[string]string{"etag": "etag-sync"},
		},
		Content: []byte("sync content"),
	}}
	svc := NewDocumentScheduleService(
		gdb,
		knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		fileStore,
		runner,
		config.RAGKnowledgeScheduleConfig{BatchSize: 10, LockSeconds: 60},
	)
	svc.now = func() time.Time { return now }
	svc.RegisterSource(source)

	count, err := svc.ScanDue(context.Background())
	if err != nil {
		t.Fatalf("scan due: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 processed schedule, got %d", count)
	}
	if !runner.runCalled {
		t.Fatalf("expected synchronous chunk runner to be called")
	}
	if fileStore.docID != doc.ID {
		t.Fatalf("expected file to be stored before chunking, got %s", fileStore.docID)
	}

	var updated knowledgeModel.KnowledgeDocumentSchedule
	if err := gdb.First(&updated, "doc_id = ?", doc.ID).Error; err != nil {
		t.Fatalf("find updated schedule: %v", err)
	}
	if updated.LastStatus != "failed" || updated.LastError == "" {
		t.Fatalf("expected schedule failure from sync runner, got %+v", updated)
	}
}

func TestDocumentScheduleService_SourceForDocumentPrefersFeishuWikiSource(t *testing.T) {
	svc := NewDocumentScheduleService(nil, nil, nil, nil, nil, config.RAGKnowledgeScheduleConfig{})
	svc.RegisterSource(&fakeScheduleSource{name: "url"})
	svc.RegisterSource(&fakeScheduleSource{name: "feishu"})

	doc := &knowledgeModel.KnowledgeDocument{
		SourceType:     "url",
		SourceLocation: "https://my.feishu.cn/wiki/RvgUw7bhxi4rUfkwyt6c2LD7nVb",
	}

	got := svc.sourceForDocument(doc)
	if got == nil || got.Name() != "feishu" {
		t.Fatalf("expected feishu source, got %#v", got)
	}
}

func TestDocumentScheduleService_SourceForDocumentPrefersConfluenceSource(t *testing.T) {
	svc := NewDocumentScheduleService(nil, nil, nil, nil, nil, config.RAGKnowledgeScheduleConfig{})
	svc.RegisterSource(&fakeScheduleSource{name: "url"})
	svc.RegisterSource(&fakeScheduleSource{name: "confluence"})

	doc := &knowledgeModel.KnowledgeDocument{
		SourceType:     "url",
		SourceLocation: "https://mycompany.atlassian.net/wiki/spaces/ENG/pages/12345/Confluence+文档",
	}

	got := svc.sourceForDocument(doc)
	if got == nil || got.Name() != "confluence" {
		t.Fatalf("expected confluence source, got %#v", got)
	}
}

func ptrTime(t time.Time) *time.Time {
	return &t
}
