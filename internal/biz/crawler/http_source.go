package crawler

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

// HTTPSourceConfig configures a single HTTP/HTTPS document source.
type HTTPSourceConfig struct {
	Name     string
	URL      string
	FileName string
	Token    string
	Headers  map[string]string
	MaxBytes int64
	Client   *http.Client
}

// HTTPSource fetches one document from an HTTP/HTTPS URL.
type HTTPSource struct {
	cfg    HTTPSourceConfig
	client *http.Client
}

// NewHTTPSource creates an HTTP URL document source.
func NewHTTPSource(cfg HTTPSourceConfig) *HTTPSource {
	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}
	return &HTTPSource{cfg: cfg, client: client}
}

func (s *HTTPSource) Name() string {
	if strings.TrimSpace(s.cfg.Name) != "" {
		return s.cfg.Name
	}
	return "http"
}

func (s *HTTPSource) ListDocuments(ctx context.Context) ([]DocumentMeta, error) {
	if strings.TrimSpace(s.cfg.URL) == "" {
		return nil, fmt.Errorf("http source url is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, s.cfg.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("build http head request: %w", err)
	}
	s.applyHeaders(req)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http source head failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http source head status: %d", resp.StatusCode)
	}
	meta := s.metaFromHeaders(s.cfg.URL, resp.Header)
	return []DocumentMeta{meta}, nil
}

func (s *HTTPSource) FetchDocument(ctx context.Context, id string) (*Document, error) {
	targetURL := strings.TrimSpace(id)
	if targetURL == "" {
		targetURL = strings.TrimSpace(s.cfg.URL)
	}
	if targetURL == "" {
		return nil, fmt.Errorf("http source url is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build http get request: %w", err)
	}
	s.applyHeaders(req)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http source fetch failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http source fetch status: %d", resp.StatusCode)
	}
	if err := s.checkSize(resp.ContentLength); err != nil {
		return nil, err
	}
	body, err := readWithLimit(resp.Body, s.cfg.MaxBytes)
	if err != nil {
		return nil, err
	}
	meta := s.metaFromHeaders(targetURL, resp.Header)
	meta.Size = int64(len(body))
	return &Document{Meta: meta, Content: body}, nil
}

func (s *HTTPSource) WatchChanges(ctx context.Context, since time.Time) (<-chan ChangeEvent, error) {
	ch := make(chan ChangeEvent)
	close(ch)
	_ = ctx
	_ = since
	return ch, nil
}

func (s *HTTPSource) applyHeaders(req *http.Request) {
	for k, v := range s.cfg.Headers {
		if strings.TrimSpace(k) != "" {
			req.Header.Set(k, v)
		}
	}
	if strings.TrimSpace(s.cfg.Token) != "" && req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.Token)
	}
}

func (s *HTTPSource) metaFromHeaders(rawURL string, header http.Header) DocumentMeta {
	fileName := firstNonEmpty(s.cfg.FileName, filenameFromContentDisposition(header.Get("Content-Disposition")), path.Base(rawURL))
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
		Extra:      map[string]string{"source_type": "url"},
	}
}

func (s *HTTPSource) checkSize(size int64) error {
	if s.cfg.MaxBytes > 0 && size > s.cfg.MaxBytes {
		return fmt.Errorf("http source document exceeds max size: %d > %d", size, s.cfg.MaxBytes)
	}
	return nil
}

func readWithLimit(r io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		data, err := io.ReadAll(r)
		if err != nil {
			return nil, fmt.Errorf("read http source body: %w", err)
		}
		return data, nil
	}
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read http source body: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("http source document exceeds max size: %d > %d", len(data), maxBytes)
	}
	return data, nil
}

func filenameFromContentDisposition(value string) string {
	_, params, err := mime.ParseMediaType(value)
	if err != nil {
		return ""
	}
	return params["filename"]
}

func normalizeContentType(contentType string) string {
	if contentType == "" {
		return ""
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err == nil {
		return mediaType
	}
	if idx := strings.Index(contentType, ";"); idx >= 0 {
		return strings.TrimSpace(contentType[:idx])
	}
	return strings.TrimSpace(contentType)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" && value != "." && value != "/" {
			return strings.TrimSpace(value)
		}
	}
	return "remote-file"
}
