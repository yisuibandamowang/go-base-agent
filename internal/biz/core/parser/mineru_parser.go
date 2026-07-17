package parser

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go-base-agent/internal/biz/rag"

	"golang.org/x/sync/semaphore"
)

// MinerUOptions 配置 MinerU 解析行为。
type MinerUOptions struct {
	APIURL           string
	APIKey           string
	PollInterval     time.Duration
	Timeout          time.Duration
	EnableTable      bool
	EnableFormula    bool
	OCR              bool
	Language         string
	ConcurrencyLimit int64
}

// MinerUParser 调用 MinerU SaaS 解析复杂版面文档。
type MinerUParser struct {
	client   *MinerUClient
	unpacker *MinerUResultUnpacker
	opts     MinerUOptions
	sem      *semaphore.Weighted
}

// NewMinerUParser 创建 MinerUParser。
func NewMinerUParser(client *MinerUClient, unpacker *MinerUResultUnpacker, opts MinerUOptions) *MinerUParser {
	var sem *semaphore.Weighted
	if opts.ConcurrencyLimit > 0 {
		sem = semaphore.NewWeighted(opts.ConcurrencyLimit)
	}
	return &MinerUParser{
		client:   client,
		unpacker: unpacker,
		opts:     opts,
		sem:      sem,
	}
}

func (p *MinerUParser) Type() rag.ParserType { return rag.ParserMinerU }

func (p *MinerUParser) Supports(mimeType string) bool {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "application/pdf",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/msword",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"application/vnd.ms-powerpoint",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.ms-excel":
		return true
	default:
		return false
	}
}

func (p *MinerUParser) Parse(ctx context.Context, data []byte, mimeType string, options map[string]string) (*rag.ParsedDocument, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("mineru input is empty")
	}
	if p.client == nil || p.unpacker == nil {
		return nil, fmt.Errorf("mineru parser is not configured")
	}
	acquireCtx := ctx
	if p.opts.Timeout > 0 {
		var cancel context.CancelFunc
		acquireCtx, cancel = context.WithTimeout(ctx, p.opts.Timeout)
		defer cancel()
	}
	if p.sem != nil {
		if err := p.sem.Acquire(acquireCtx, 1); err != nil {
			return nil, fmt.Errorf("acquire mineru permit: %w", err)
		}
		defer p.sem.Release(1)
	}

	sourceFile := parseOption(options, "sourceFile")
	documentID := parseOption(options, "documentId")
	if documentID == "" {
		documentID = sourceFile
	}
	if documentID == "" {
		documentID = fmt.Sprintf("mineru-%d", time.Now().UnixNano())
	}
	fileName := resolveMinerUFileName(sourceFile, mimeType, documentID)
	submit := minerUSubmitRequest{
		FileName:      fileName,
		DataID:        documentID,
		OCR:           p.opts.OCR,
		EnableTable:   p.opts.EnableTable,
		EnableFormula: p.opts.EnableFormula,
		Language:      firstNonEmpty(p.opts.Language, "ch"),
	}

	ticket, err := p.client.requestUpload(ctx, submit)
	if err != nil {
		return nil, fmt.Errorf("request mineru upload: %w", err)
	}
	if err := p.client.uploadFile(ctx, ticket.UploadURL, data); err != nil {
		return nil, fmt.Errorf("upload mineru file: %w", err)
	}

	status, err := p.awaitResult(ctx, ticket.BatchID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(status.ZipURL) == "" {
		return nil, fmt.Errorf("mineru result missing zip url")
	}
	zipBytes, err := p.client.downloadZip(ctx, status.ZipURL)
	if err != nil {
		return nil, fmt.Errorf("download mineru zip: %w", err)
	}
	parsed, err := p.unpacker.Unpack(ctx, zipBytes, sourceFile, documentID)
	if err != nil {
		return nil, err
	}
	if parsed.Metadata == nil {
		parsed.Metadata = make(map[string]string)
	}
	parsed.Metadata["mineru.batchId"] = ticket.BatchID
	parsed.Metadata["mineru.zipUrl"] = status.ZipURL
	parsed.Metadata["mimeType"] = mimeType
	parsed.Metadata["parser"] = string(rag.ParserMinerU)
	return parsed, nil
}

func (p *MinerUParser) awaitResult(ctx context.Context, batchID string) (minerUStatus, error) {
	timeout := p.opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	pollInterval := p.opts.PollInterval
	if pollInterval <= 0 {
		pollInterval = 5 * time.Second
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		status, err := p.client.queryResult(ctx, batchID)
		if err != nil {
			return minerUStatus{}, fmt.Errorf("query mineru result: %w", err)
		}
		switch status.State {
		case minerUStateSucceeded, minerUStateCompleted, minerUStateFinished:
			return status, nil
		case minerUStateFailed, minerUStateCancelled:
			if strings.TrimSpace(status.ErrMsg) == "" {
				status.ErrMsg = "mineru task failed"
			}
			return minerUStatus{}, fmt.Errorf("mineru task %s: %s", batchID, status.ErrMsg)
		}

		select {
		case <-ctx.Done():
			return minerUStatus{}, ctx.Err()
		case <-deadline.C:
			return minerUStatus{}, fmt.Errorf("mineru task timeout after %s", timeout)
		case <-ticker.C:
		}
	}
}

func resolveMinerUFileName(sourceFile, mimeType, documentID string) string {
	if strings.TrimSpace(sourceFile) != "" {
		return sourceFile
	}
	return "doc-" + documentID + extFromMime(mimeType)
}

func extFromMime(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "application/pdf":
		return ".pdf"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return ".docx"
	case "application/msword":
		return ".doc"
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return ".pptx"
	case "application/vnd.ms-powerpoint":
		return ".ppt"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return ".xlsx"
	case "application/vnd.ms-excel":
		return ".xls"
	default:
		return ".bin"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
