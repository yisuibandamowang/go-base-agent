package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CommandRunner 执行外部命令，便于测试替换。
type CommandRunner interface {
	Run(ctx context.Context, command string, args ...string) ([]byte, error)
}

// GeelibSourceConfig 配置极库内部文档抓取器。
type GeelibSourceConfig struct {
	Command  string
	WorkDir  string
	Timeout  time.Duration
	MaxBytes int64
	Domains  []string
	Runner   CommandRunner
}

// GeelibSource 通过 editor-cli 拉取 geelib 内部文档。
type GeelibSource struct {
	cfg    GeelibSourceConfig
	runner CommandRunner
}

// NewGeelibSource 创建 GeelibSource。
func NewGeelibSource(cfg GeelibSourceConfig) *GeelibSource {
	runner := cfg.Runner
	if runner == nil {
		runner = execCommandRunner{workDir: cfg.WorkDir}
	}
	if strings.TrimSpace(cfg.Command) == "" {
		cfg.Command = "editor-cli"
	}
	return &GeelibSource{cfg: cfg, runner: runner}
}

// Name 返回定时调度注册用来源名称。
func (s *GeelibSource) Name() string {
	return "internal_url"
}

// ListDocuments 返回单个配置 URL 下的文档元信息。
func (s *GeelibSource) ListDocuments(ctx context.Context) ([]DocumentMeta, error) {
	return nil, nil
}

// FetchDocument 根据内部文档 URL 拉取单篇文档。
func (s *GeelibSource) FetchDocument(ctx context.Context, rawURL string) (*Document, error) {
	ref, err := parseGeelibURL(rawURL, s.cfg.Domains)
	if err != nil {
		return nil, err
	}
	return s.fetchDocument(ctx, ref, geelibTreeNode{DocID: ref.docID, Title: ref.docID})
}

// WatchChanges 当前 geelib 来源不使用推送变更。
func (s *GeelibSource) WatchChanges(ctx context.Context, since time.Time) (<-chan ChangeEvent, error) {
	ch := make(chan ChangeEvent)
	close(ch)
	_ = ctx
	_ = since
	return ch, nil
}

// FetchDocuments 根据内部 URL 递归拉取目录树中有正文内容的文档。
func (s *GeelibSource) FetchDocuments(ctx context.Context, rawURL string) ([]Document, error) {
	ref, err := parseGeelibURL(rawURL, s.cfg.Domains)
	if err != nil {
		return nil, err
	}
	treePayload, err := s.runJSON(ctx, "tree", "-s", ref.spaceID, "-p", ref.docID, "--deep", "--json")
	if err != nil {
		return nil, fmt.Errorf("读取内部文档树失败: %w", err)
	}
	nodes, err := parseGeelibTreeNodes(treePayload)
	if err != nil {
		return nil, fmt.Errorf("解析内部文档树失败: %w", err)
	}
	if len(nodes) == 0 {
		nodes = []geelibTreeNode{{DocID: ref.docID, Title: ref.docID}}
	}
	flat := flattenGeelibTreeNodes(nodes)
	if len(flat) == 0 {
		flat = []geelibTreeNode{{DocID: ref.docID, Title: ref.docID}}
	}
	docs := make([]Document, 0, len(flat))
	for _, node := range flat {
		doc, err := s.fetchDocument(ctx, ref, node)
		if err != nil {
			return nil, err
		}
		if isGeelibEmptyContentPlaceholder(doc.Content) {
			continue
		}
		docs = append(docs, *doc)
	}
	return docs, nil
}

func (s *GeelibSource) fetchDocument(ctx context.Context, ref geelibURLRef, node geelibTreeNode) (*Document, error) {
	readPayload, err := s.runJSON(ctx, "read", node.docIDString(), "--json")
	if err != nil {
		return nil, fmt.Errorf("读取内部文档 %s 失败: %w", node.docIDString(), err)
	}
	content, err := extractGeelibReadContent(readPayload)
	if err != nil {
		return nil, fmt.Errorf("解析内部文档 %s 内容失败: %w", node.docIDString(), err)
	}
	if s.cfg.MaxBytes > 0 && int64(len(content)) > s.cfg.MaxBytes {
		return nil, fmt.Errorf("内部文档 %s 超过大小限制: %d > %d", node.docIDString(), len(content), s.cfg.MaxBytes)
	}
	title := strings.TrimSpace(node.Title)
	if title == "" {
		title = node.docIDString()
	}
	if filepath.Ext(title) == "" {
		title += ".md"
	}
	docURL := fmt.Sprintf("https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=%s&docId=%s", ref.spaceID, node.docIDString())
	extra := map[string]string{
		"space_id":    ref.spaceID,
		"doc_id":      node.docIDString(),
		"source_type": "internal_url",
	}
	if parentDocID := strings.TrimSpace(node.ParentDocID); parentDocID != "" {
		extra["parent_doc_id"] = parentDocID
		extra["parent_url"] = fmt.Sprintf("https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=%s&docId=%s", ref.spaceID, parentDocID)
	}
	if node.HasChildren {
		extra["has_children"] = "true"
	}
	return &Document{
		Meta: DocumentMeta{
			ID:         node.docIDString(),
			Title:      title,
			URL:        docURL,
			MimeType:   "text/markdown",
			SourceName: "geelib",
			Extra:      extra,
		},
		Content: content,
	}, nil
}

func (s *GeelibSource) runJSON(ctx context.Context, subCommand string, args ...string) ([]byte, error) {
	command := s.cfg.Command
	if strings.TrimSpace(command) == "" {
		command = "editor-cli"
	}
	if s.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.cfg.Timeout)
		defer cancel()
	}
	allArgs := append([]string{subCommand}, args...)
	return s.runner.Run(ctx, command, allArgs...)
}

type execCommandRunner struct {
	workDir string
}

func (r execCommandRunner) Run(ctx context.Context, command string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	if strings.TrimSpace(r.workDir) != "" {
		cmd.Dir = strings.TrimSpace(r.workDir)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("command %s failed: %w: %s", command, err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

type geelibURLRef struct {
	spaceID string
	docID   string
}

func parseGeelibURL(rawURL string, domains []string) (geelibURLRef, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return geelibURLRef{}, fmt.Errorf("内部 URL 不能为空")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return geelibURLRef{}, fmt.Errorf("解析内部 URL 失败: %w", err)
	}
	if len(domains) == 0 {
		domains = []string{"geelib.qihoo.net"}
	}
	if !hostAllowed(parsed.Host, domains) {
		return geelibURLRef{}, fmt.Errorf("仅支持 geelib 内部链接")
	}
	spaceID := strings.TrimSpace(parsed.Query().Get("spaceId"))
	docID := strings.TrimSpace(parsed.Query().Get("docId"))
	if spaceID == "" || docID == "" {
		return geelibURLRef{}, fmt.Errorf("内部 URL 缺少 spaceId 或 docId")
	}
	return geelibURLRef{spaceID: spaceID, docID: docID}, nil
}

func hostAllowed(host string, domains []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, domain := range domains {
		if strings.EqualFold(host, strings.TrimSpace(domain)) {
			return true
		}
	}
	return false
}

type geelibTreeNode struct {
	DocID       string
	Title       string
	ParentDocID string
	HasChildren bool
	Children    []geelibTreeNode
}

func (n geelibTreeNode) docIDString() string {
	return strings.TrimSpace(n.DocID)
}

func parseGeelibTreeNodes(payload []byte) ([]geelibTreeNode, error) {
	var raw any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, err
	}
	return collectGeelibTreeNodes(raw), nil
}

func collectGeelibTreeNodes(value any) []geelibTreeNode {
	switch v := value.(type) {
	case []any:
		out := make([]geelibTreeNode, 0, len(v))
		for _, item := range v {
			out = append(out, collectGeelibTreeNodes(item)...)
		}
		return out
	case map[string]any:
		if tree, ok := v["tree"]; ok {
			return collectGeelibTreeNodes(tree)
		}
		node := geelibTreeNode{
			DocID:       firstString(v["docId"], v["docID"], v["id"]),
			Title:       firstString(v["title"], v["name"], v["docName"]),
			HasChildren: boolFromAny(v["hasChildren"]),
		}
		if children, ok := v["children"]; ok {
			node.Children = collectGeelibTreeNodes(children)
		}
		if node.docIDString() != "" {
			return []geelibTreeNode{node}
		}
		out := make([]geelibTreeNode, 0, len(v))
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			out = append(out, collectGeelibTreeNodes(v[key])...)
		}
		return out
	default:
		return nil
	}
}

func flattenGeelibTreeNodes(nodes []geelibTreeNode) []geelibTreeNode {
	return flattenGeelibTreeNodesWithParent(nodes, "")
}

func flattenGeelibTreeNodesWithParent(nodes []geelibTreeNode, parentDocID string) []geelibTreeNode {
	var out []geelibTreeNode
	for _, node := range nodes {
		hasChildren := node.HasChildren || len(node.Children) > 0
		out = append(out, geelibTreeNode{
			DocID:       node.DocID,
			Title:       node.Title,
			ParentDocID: parentDocID,
			HasChildren: hasChildren,
		})
		if len(node.Children) > 0 {
			out = append(out, flattenGeelibTreeNodesWithParent(node.Children, node.docIDString())...)
		}
	}
	return out
}

func isGeelibEmptyContentPlaceholder(content []byte) bool {
	text := strings.TrimSpace(strings.TrimPrefix(string(content), "\ufeff"))
	return text == "该文档内容为空"
}

func extractGeelibReadContent(payload []byte) ([]byte, error) {
	var raw any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, err
	}
	if text := collectGeelibText(raw); strings.TrimSpace(text) != "" {
		return []byte(text), nil
	}
	return nil, fmt.Errorf("内部文档没有可读取内容")
}

func collectGeelibText(value any) string {
	switch v := value.(type) {
	case map[string]any:
		if content, ok := v["content"]; ok {
			if text := collectGeelibText(content); strings.TrimSpace(text) != "" {
				return text
			}
		}
		if data, ok := v["data"]; ok {
			if text := collectGeelibText(data); strings.TrimSpace(text) != "" {
				return text
			}
		}
		if text, ok := v["text"].(string); ok {
			return text
		}
		if body, ok := v["body"].(string); ok {
			return body
		}
		return ""
	case []any:
		var b strings.Builder
		for _, item := range v {
			part := collectGeelibText(item)
			if strings.TrimSpace(part) == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(part)
		}
		return b.String()
	case string:
		return v
	default:
		return ""
	}
}

func firstString(values ...any) string {
	for _, value := range values {
		switch v := value.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		case fmt.Stringer:
			if strings.TrimSpace(v.String()) != "" {
				return strings.TrimSpace(v.String())
			}
		case json.Number:
			if v.String() != "" {
				return v.String()
			}
		case float64:
			return strconv.FormatInt(int64(v), 10)
		case int:
			return strconv.Itoa(v)
		case int64:
			return strconv.FormatInt(v, 10)
		}
	}
	return ""
}

func boolFromAny(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		v = strings.TrimSpace(strings.ToLower(v))
		return v == "1" || v == "true" || v == "yes"
	default:
		return false
	}
}
