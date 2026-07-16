package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// ConfluenceSourceConfig configures a Confluence document source.
type ConfluenceSourceConfig struct {
	Name        string
	URL         string
	FileName    string
	Username    string
	APIKey      string
	AccessToken string
	BaseURL     string
	Headers     map[string]string
	MaxBytes    int64
	Client      *http.Client
}

// ConfluenceSource fetches Confluence pages.
type ConfluenceSource struct {
	cfg    ConfluenceSourceConfig
	client *http.Client
}

// NewConfluenceSource creates a Confluence source.
func NewConfluenceSource(cfg ConfluenceSourceConfig) *ConfluenceSource {
	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		if parsed, err := url.Parse(strings.TrimSpace(cfg.URL)); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			cfg.BaseURL = parsed.Scheme + "://" + parsed.Host
		}
	}
	return &ConfluenceSource{cfg: cfg, client: client}
}

func (s *ConfluenceSource) Name() string {
	if strings.TrimSpace(s.cfg.Name) != "" {
		return s.cfg.Name
	}
	return "confluence"
}

func (s *ConfluenceSource) ListDocuments(ctx context.Context) ([]DocumentMeta, error) {
	doc, err := s.FetchDocument(ctx, s.cfg.URL)
	if err != nil {
		return nil, err
	}
	return []DocumentMeta{doc.Meta}, nil
}

func (s *ConfluenceSource) FetchDocument(ctx context.Context, id string) (*Document, error) {
	target := strings.TrimSpace(id)
	if target == "" {
		target = strings.TrimSpace(s.cfg.URL)
	}
	if target == "" {
		return nil, fmt.Errorf("confluence source url is empty")
	}
	pageID := extractConfluencePageID(target)
	if pageID == "" {
		return nil, fmt.Errorf("confluence page id is empty")
	}
	baseURL := strings.TrimRight(s.cfg.BaseURL, "/")
	if baseURL == "" {
		parsed, err := url.Parse(target)
		if err != nil {
			return nil, fmt.Errorf("parse confluence url: %w", err)
		}
		baseURL = parsed.Scheme + "://" + parsed.Host
	}
	apiURL := baseURL + "/wiki/rest/api/content/" + pageID + "?expand=body.storage,version"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build confluence content request: %w", err)
	}
	s.applyHeaders(req)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("confluence content request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("confluence content status: %d", resp.StatusCode)
	}
	if err := s.checkSize(resp.ContentLength); err != nil {
		return nil, err
	}
	var body []byte
	if s.cfg.MaxBytes > 0 {
		body, err = io.ReadAll(io.LimitReader(resp.Body, s.cfg.MaxBytes+1))
	} else {
		body, err = io.ReadAll(resp.Body)
	}
	if err != nil {
		return nil, fmt.Errorf("read confluence response: %w", err)
	}
	if s.cfg.MaxBytes > 0 && int64(len(body)) > s.cfg.MaxBytes {
		return nil, fmt.Errorf("confluence source document exceeds max size: %d > %d", len(body), s.cfg.MaxBytes)
	}
	var decoded struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Body  struct {
			Storage struct {
				Value string `json:"value"`
			} `json:"storage"`
		} `json:"body"`
		Links struct {
			WebUI string `json:"webui"`
		} `json:"_links"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("parse confluence response: %w", err)
	}
	content := decoded.Body.Storage.Value
	if strings.TrimSpace(content) == "" {
		content = string(body)
	}
	meta := DocumentMeta{
		ID:         target,
		Title:      firstNonEmpty(s.cfg.FileName, decoded.Title, path.Base(target)),
		URL:        target,
		MimeType:   "text/html",
		Size:       int64(len(content)),
		SourceName: s.Name(),
		Extra: map[string]string{
			"source_type": "confluence",
			"page_id":     pageID,
			"webui":       decoded.Links.WebUI,
		},
	}
	return &Document{Meta: meta, Content: []byte(content)}, nil
}

func (s *ConfluenceSource) WatchChanges(ctx context.Context, since time.Time) (<-chan ChangeEvent, error) {
	ch := make(chan ChangeEvent)
	close(ch)
	_ = ctx
	_ = since
	return ch, nil
}

func (s *ConfluenceSource) applyHeaders(req *http.Request) {
	for k, v := range s.cfg.Headers {
		if strings.TrimSpace(k) != "" {
			req.Header.Set(k, v)
		}
	}
	if strings.TrimSpace(s.cfg.AccessToken) != "" && req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.AccessToken)
		return
	}
	if strings.TrimSpace(s.cfg.Username) != "" && strings.TrimSpace(s.cfg.APIKey) != "" {
		req.SetBasicAuth(s.cfg.Username, s.cfg.APIKey)
	}
}

func (s *ConfluenceSource) checkSize(size int64) error {
	if s.cfg.MaxBytes > 0 && size > s.cfg.MaxBytes {
		return fmt.Errorf("confluence source document exceeds max size: %d > %d", size, s.cfg.MaxBytes)
	}
	return nil
}

func extractConfluencePageID(location string) string {
	parsed, err := url.Parse(strings.TrimSpace(location))
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := 0; i < len(parts); i++ {
		if parts[i] == "pages" && i+1 < len(parts) {
			pageID := parts[i+1]
			if idx := strings.Index(pageID, "?"); idx >= 0 {
				pageID = pageID[:idx]
			}
			return pageID
		}
	}
	return ""
}
