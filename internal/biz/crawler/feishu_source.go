package crawler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// FeishuSourceConfig configures a Feishu document source.
type FeishuSourceConfig struct {
	Name          string
	URL           string
	FileName      string
	AppID         string
	AppSecret     string
	AccessToken   string
	TenantToken   string
	TokenEndpoint string
	BaseURL       string
	Headers       map[string]string
	MaxBytes      int64
	Client        *http.Client
}

// FeishuSource fetches Feishu docs.
type FeishuSource struct {
	cfg    FeishuSourceConfig
	client *http.Client
}

// NewFeishuSource creates a Feishu source.
func NewFeishuSource(cfg FeishuSourceConfig) *FeishuSource {
	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = "https://open.feishu.cn"
	}
	if strings.TrimSpace(cfg.TokenEndpoint) == "" {
		cfg.TokenEndpoint = cfg.BaseURL + "/open-apis/auth/v3/tenant_access_token/internal/"
	}
	return &FeishuSource{cfg: cfg, client: client}
}

func (s *FeishuSource) Name() string {
	if strings.TrimSpace(s.cfg.Name) != "" {
		return s.cfg.Name
	}
	return "feishu"
}

func (s *FeishuSource) ListDocuments(ctx context.Context) ([]DocumentMeta, error) {
	target := strings.TrimSpace(s.cfg.URL)
	if target == "" {
		return nil, fmt.Errorf("feishu source url is empty")
	}
	if isFeishuWikiURL(target) {
		node, err := s.fetchWikiNode(ctx, target)
		if err != nil {
			return nil, err
		}
		meta := DocumentMeta{
			ID:         target,
			Title:      firstNonEmpty(s.cfg.FileName, node.Title, pathBase(target)),
			URL:        target,
			MimeType:   "text/html",
			SourceName: s.Name(),
			Extra: map[string]string{
				"source_type": "feishu",
				"obj_type":    node.ObjType,
				"obj_token":   node.ObjToken,
			},
		}
		return []DocumentMeta{meta}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
	if err != nil {
		return nil, fmt.Errorf("build feishu head request: %w", err)
	}
	s.applyHeaders(ctx, req)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("feishu head failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("feishu head status: %d", resp.StatusCode)
	}
	meta := s.metaFromHeaders(target, resp.Header)
	return []DocumentMeta{meta}, nil
}

func (s *FeishuSource) FetchDocument(ctx context.Context, id string) (*Document, error) {
	target := strings.TrimSpace(id)
	if target == "" {
		target = strings.TrimSpace(s.cfg.URL)
	}
	if target == "" {
		return nil, fmt.Errorf("feishu source url is empty")
	}

	if isFeishuWikiURL(target) {
		node, err := s.fetchWikiNode(ctx, target)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(node.ObjToken) == "" {
			return nil, fmt.Errorf("feishu wiki node obj_token is empty")
		}
		switch strings.ToLower(strings.TrimSpace(node.ObjType)) {
		case "", "docx":
			return s.fetchDocxDocument(ctx, target, node.ObjToken, node.Title)
		default:
			return nil, fmt.Errorf("feishu wiki node obj_type %q is not supported", node.ObjType)
		}
	}

	if isFeishuDocxURL(target) {
		docToken := extractFeishuDocToken(target)
		return s.fetchDocxDocument(ctx, target, docToken, "")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("build feishu get request: %w", err)
	}
	s.applyHeaders(ctx, req)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("feishu fetch failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("feishu fetch status: %d", resp.StatusCode)
	}
	if err := s.checkSize(resp.ContentLength); err != nil {
		return nil, err
	}
	body, err := readWithLimit(resp.Body, s.cfg.MaxBytes)
	if err != nil {
		return nil, err
	}
	meta := s.metaFromHeaders(target, resp.Header)
	meta.Size = int64(len(body))
	return &Document{Meta: meta, Content: body}, nil
}

func (s *FeishuSource) WatchChanges(ctx context.Context, since time.Time) (<-chan ChangeEvent, error) {
	ch := make(chan ChangeEvent)
	close(ch)
	_ = ctx
	_ = since
	return ch, nil
}

func (s *FeishuSource) applyHeaders(ctx context.Context, req *http.Request) {
	for k, v := range s.cfg.Headers {
		if strings.TrimSpace(k) != "" {
			req.Header.Set(k, v)
		}
	}
	if token := s.resolveAccessToken(ctx); strings.TrimSpace(token) != "" && req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

type feishuWikiNode struct {
	Title    string
	ObjType  string
	ObjToken string
}

func (s *FeishuSource) fetchWikiNode(ctx context.Context, target string) (*feishuWikiNode, error) {
	wikiToken := extractFeishuWikiToken(target)
	if strings.TrimSpace(wikiToken) == "" {
		return nil, fmt.Errorf("feishu wiki token is empty")
	}
	apiURL := strings.TrimRight(s.cfg.BaseURL, "/") + "/open-apis/wiki/v2/spaces/get_node?token=" + url.QueryEscape(wikiToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build feishu wiki get node request: %w", err)
	}
	s.applyHeaders(ctx, req)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("feishu wiki get node request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("feishu wiki get node status: %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read feishu wiki get node response: %w", err)
	}
	var decoded struct {
		Data struct {
			Node struct {
				Title    string `json:"title"`
				ObjType  string `json:"obj_type"`
				ObjToken string `json:"obj_token"`
			} `json:"node"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("parse feishu wiki get node response: %w", err)
	}
	return &feishuWikiNode{
		Title:    decoded.Data.Node.Title,
		ObjType:  decoded.Data.Node.ObjType,
		ObjToken: decoded.Data.Node.ObjToken,
	}, nil
}

func (s *FeishuSource) fetchDocxDocument(ctx context.Context, targetURL, docToken, title string) (*Document, error) {
	apiURL := strings.TrimRight(s.cfg.BaseURL, "/") + "/open-apis/docx/v1/documents/" + docToken + "/raw_content"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build feishu raw content request: %w", err)
	}
	s.applyHeaders(ctx, req)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("feishu raw content request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("feishu raw content status: %d", resp.StatusCode)
	}
	if err := s.checkSize(resp.ContentLength); err != nil {
		return nil, err
	}
	body, err := readWithLimit(resp.Body, s.cfg.MaxBytes)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > s.cfg.MaxBytes && s.cfg.MaxBytes > 0 {
		return nil, fmt.Errorf("feishu source document exceeds max size: %d > %d", len(body), s.cfg.MaxBytes)
	}
	text := extractFeishuDocContent(body)
	if strings.TrimSpace(text) == "" {
		text = string(body)
	}
	meta := DocumentMeta{
		ID:         targetURL,
		Title:      firstNonEmpty(s.cfg.FileName, title, docToken+".txt"),
		URL:        targetURL,
		MimeType:   "text/plain",
		Size:       int64(len(text)),
		SourceName: s.Name(),
		Extra:      map[string]string{"source_type": "feishu"},
	}
	return &Document{Meta: meta, Content: []byte(text)}, nil
}

func (s *FeishuSource) resolveAccessToken(ctx context.Context) string {
	_ = ctx
	if strings.TrimSpace(s.cfg.AccessToken) != "" {
		return s.cfg.AccessToken
	}
	if strings.TrimSpace(s.cfg.TenantToken) != "" {
		return s.cfg.TenantToken
	}
	if strings.TrimSpace(s.cfg.AppID) == "" || strings.TrimSpace(s.cfg.AppSecret) == "" {
		return ""
	}
	payload := map[string]string{
		"app_id":     s.cfg.AppID,
		"app_secret": s.cfg.AppSecret,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.TokenEndpoint, bytes.NewReader(body))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	var decoded struct {
		TenantAccessToken string `json:"tenant_access_token"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return ""
	}
	return decoded.TenantAccessToken
}

func isFeishuDocxURL(location string) bool {
	return strings.Contains(location, "/docx/") || strings.Contains(location, "/docs/")
}

func isFeishuWikiURL(location string) bool {
	return strings.Contains(location, "/wiki/")
}

func extractFeishuWikiToken(location string) string {
	parts := strings.Split(location, "/")
	for i := 0; i < len(parts); i++ {
		if parts[i] == "wiki" && i+1 < len(parts) {
			token := parts[i+1]
			if idx := strings.Index(token, "?"); idx >= 0 {
				token = token[:idx]
			}
			return token
		}
	}
	return strings.TrimSpace(location)
}

func extractFeishuDocToken(location string) string {
	parts := strings.Split(location, "/")
	for i := 0; i < len(parts); i++ {
		if parts[i] == "docx" || parts[i] == "docs" {
			if i+1 < len(parts) {
				token := parts[i+1]
				if idx := strings.Index(token, "?"); idx >= 0 {
					token = token[:idx]
				}
				return token
			}
		}
	}
	return strings.TrimSpace(location)
}

func extractFeishuDocContent(body []byte) string {
	var root struct {
		Data struct {
			Content string `json:"content"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		return ""
	}
	return root.Data.Content
}

func (s *FeishuSource) metaFromHeaders(rawURL string, header http.Header) DocumentMeta {
	fileName := firstNonEmpty(s.cfg.FileName, filenameFromContentDisposition(header.Get("Content-Disposition")), pathBase(rawURL))
	mimeType := normalizeContentType(header.Get("Content-Type"))
	size := int64(0)
	if contentLength := strings.TrimSpace(header.Get("Content-Length")); contentLength != "" {
		if parsed, err := strconv.ParseInt(contentLength, 10, 64); err == nil {
			size = parsed
		}
	}
	return DocumentMeta{
		ID:         rawURL,
		Title:      fileName,
		URL:        rawURL,
		MimeType:   mimeType,
		Size:       size,
		SourceName: s.Name(),
		Extra:      map[string]string{"source_type": "feishu"},
	}
}

func (s *FeishuSource) checkSize(size int64) error {
	if s.cfg.MaxBytes > 0 && size > s.cfg.MaxBytes {
		return fmt.Errorf("feishu source document exceeds max size: %d > %d", size, s.cfg.MaxBytes)
	}
	return nil
}

func pathBase(rawURL string) string {
	if idx := strings.LastIndex(rawURL, "/"); idx >= 0 && idx+1 < len(rawURL) {
		return rawURL[idx+1:]
	}
	return rawURL
}
