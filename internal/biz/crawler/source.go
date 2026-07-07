// Package crawler 预留文档爬取能力，用于 Agentic RAG 升级。
//
// 计划支持的 Source:
//   - Confluence REST API
//   - Notion API
//   - Git 仓库文档
//   - 飞书文档
//   - S3 对象存储
//
// 具体实现见 docs/AGENTIC_RAG_PLAN.md，将在基础 RAG 全链路完成（阶段 0-7）后启动阶段 8。
package crawler

import (
	"context"
	"time"
)

// DocumentMeta 文档元信息，来源无关。
type DocumentMeta struct {
	ID          string
	Title       string
	URL         string
	MimeType    string
	Size        int64
	UpdatedAt   time.Time
	SourceName  string
	Extra       map[string]string
}

// Document 完整文档内容。
type Document struct {
	Meta    DocumentMeta
	Content []byte
}

// ChangeEvent 文档变更事件。
type ChangeEvent struct {
	Type     string // created / updated / deleted
	Document DocumentMeta
	Time     time.Time
}

// Source 文档来源接口，每种外部系统实现一个。
type Source interface {
	Name() string
	ListDocuments(ctx context.Context) ([]DocumentMeta, error)
	FetchDocument(ctx context.Context, id string) (*Document, error)
	WatchChanges(ctx context.Context, since time.Time) (<-chan ChangeEvent, error)
}

// Scheduler 管理多个 Source 的定时抓取与变更监听。
type Scheduler struct {
	Sources []Source
}

func NewScheduler(sources ...Source) *Scheduler {
	return &Scheduler{Sources: sources}
}
