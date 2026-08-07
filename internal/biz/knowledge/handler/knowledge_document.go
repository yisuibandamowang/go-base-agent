package handler

import (
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	coreparser "go-base-agent/internal/biz/core/parser"
	"go-base-agent/internal/biz/crawler"
	"go-base-agent/internal/biz/knowledge/dto"
	"go-base-agent/internal/biz/knowledge/service"
	"go-base-agent/internal/biz/rag"
	"go-base-agent/internal/framework/convention"
	"go-base-agent/internal/framework/ratelimit"

	"github.com/gin-gonic/gin"
)

// DocumentHandler 文档管理 HTTP 处理层。
type DocumentHandler struct {
	svc                *service.DocumentService
	fileStore          *FileStore
	internalURLFetcher internalURLFetcher
	uploadLimiter      uploadLimiter
	uploadMaxWait      time.Duration
}

type uploadLimiter interface {
	Acquire(ctx context.Context, req ratelimit.AcquireRequest) error
}

type internalURLFetcher interface {
	FetchDocuments(ctx context.Context, rawURL string) ([]crawler.Document, error)
}

// NewDocumentHandler 创建 DocumentHandler。
func NewDocumentHandler(svc *service.DocumentService, fs *FileStore) *DocumentHandler {
	return &DocumentHandler{svc: svc, fileStore: fs}
}

// SetUploadLimiter 为文档上传接口设置并发限流器。
func (h *DocumentHandler) SetUploadLimiter(limiter uploadLimiter, maxWait time.Duration) {
	h.uploadLimiter = limiter
	h.uploadMaxWait = maxWait
}

// SetInternalURLFetcher 设置内部 URL 拉取器。
func (h *DocumentHandler) SetInternalURLFetcher(fetcher internalURLFetcher) {
	h.internalURLFetcher = fetcher
}

// Upload POST /knowledge-base/:id/docs/upload
func (h *DocumentHandler) Upload(c *gin.Context) {
	if h.uploadLimiter == nil {
		h.uploadDocument(c)
		return
	}
	var once sync.Once
	done := make(chan struct{})
	finish := func() {
		once.Do(func() {
			close(done)
		})
	}
	err := h.uploadLimiter.Acquire(c.Request.Context(), ratelimit.AcquireRequest{
		MaxWait: h.uploadMaxWait,
		OnAcquire: func() {
			defer finish()
			h.uploadDocument(c)
		},
		OnTimeout: func() {
			defer finish()
			c.JSON(http.StatusOK, convention.Failure("A000001", "文档上传请求过于频繁，请稍后再试"))
		},
	})
	if err != nil {
		select {
		case <-done:
		case <-c.Request.Context().Done():
		}
		return
	}
	<-done
}

func (h *DocumentHandler) uploadDocument(c *gin.Context) {
	kbID := c.Param("id")

	// Parse multipart form (max 50MB)
	if err := c.Request.ParseMultipartForm(50 << 20); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", fmt.Sprintf("解析上传表单失败: %v", err)))
		return
	}

	form := c.Request.MultipartForm
	var req dto.CreateDocumentReq
	var file multipart.File
	var header *multipart.FileHeader
	var remoteData []byte
	var remoteName string

	// File upload
	if files := form.File["file"]; len(files) > 0 {
		header = files[0]
		f, err := header.Open()
		if err != nil {
			c.JSON(http.StatusOK, convention.Failure("A000001", fmt.Sprintf("读取文件失败: %v", err)))
			return
		}
		file = f
		defer file.Close()
		req.DocName = header.Filename
		req.FileSize = header.Size
		req.FileType = strings.TrimPrefix(filepath.Ext(header.Filename), ".")
		req.FileURL = fmt.Sprintf("upload://%s", header.Filename)
	}

	// Source type (file or url)
	if vals, ok := form.Value["sourceType"]; ok && len(vals) > 0 {
		req.SourceType = vals[0]
	}

	// Source location for URL type
	if vals, ok := form.Value["sourceLocation"]; ok && len(vals) > 0 {
		req.SourceLocation = vals[0]
		req.FileURL = vals[0]
		if req.DocName == "" {
			req.DocName = vals[0]
		}
	}

	if file == nil && strings.EqualFold(strings.TrimSpace(req.SourceType), "url") {
		data, name, fileType, err := fetchRemoteUploadFile(c.Request.Context(), req.SourceLocation)
		if err != nil {
			c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
			return
		}
		remoteData = data
		remoteName = name
		req.DocName = name
		req.FileType = fileType
		req.FileSize = int64(len(data))
		req.FileURL = req.SourceLocation
	}

	if vals, ok := form.Value["scheduleEnabled"]; ok && len(vals) > 0 {
		req.ScheduleEnabled = parseInt16(vals[0])
	}
	if vals, ok := form.Value["scheduleCron"]; ok && len(vals) > 0 {
		req.ScheduleCron = vals[0]
	}
	if vals, ok := form.Value["enabled"]; ok && len(vals) > 0 {
		enabled := parseInt16(vals[0])
		req.Enabled = &enabled
	}

	// Process mode & chunk strategy
	if vals, ok := form.Value["processMode"]; ok && len(vals) > 0 {
		req.ProcessMode = vals[0]
		if vals[0] == "pipeline" {
			if pvals, ok := form.Value["pipelineId"]; ok && len(pvals) > 0 {
				req.ChunkStrategy = "pipeline"
				req.PipelineID = pvals[0]
				req.ChunkConfig = fmt.Sprintf(`{"pipelineId":"%s"}`, pvals[0])
			}
		}
	}
	if vals, ok := form.Value["chunkStrategy"]; ok && len(vals) > 0 {
		req.ChunkStrategy = vals[0]
	}
	if vals, ok := form.Value["chunkConfig"]; ok && len(vals) > 0 {
		req.ChunkConfig = vals[0]
	}
	if vals, ok := form.Value["pipelineId"]; ok && len(vals) > 0 && req.PipelineID == "" {
		req.PipelineID = vals[0]
	}

	if strings.EqualFold(strings.TrimSpace(req.SourceType), "internal_url") {
		h.uploadInternalURLDocuments(c, kbID, req, userID(c))
		return
	}

	if req.DocName == "" {
		c.JSON(http.StatusOK, convention.Failure("A000001", "缺少文件名"))
		return
	}
	if req.FileType == "" {
		req.FileType = "unknown"
	}

	kb, err := h.svc.GetKnowledgeBase(c.Request.Context(), kbID)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", "查询知识库失败: "+err.Error()))
		return
	}
	if strings.TrimSpace(kb.CollectionName) != "" {
		req.FileURL = fmt.Sprintf("upload://%s/%s", kb.CollectionName, req.DocName)
	}

	_ = file // processed in pipeline

	resp, err := h.svc.CreateDocument(c.Request.Context(), kbID, req, userID(c))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}

	// Save file content for later retrieval
	if file != nil {
		data, _ := io.ReadAll(file)
		if err := h.fileStore.PutWithCollection(c.Request.Context(), kb.CollectionName, resp.ID, header.Filename, data); err != nil {
			c.JSON(http.StatusOK, convention.Failure("B000001", "保存上传文件失败: "+err.Error()))
			return
		}
	}
	if remoteData != nil {
		if err := h.fileStore.PutWithCollection(c.Request.Context(), kb.CollectionName, resp.ID, remoteName, remoteData); err != nil {
			c.JSON(http.StatusOK, convention.Failure("B000001", "保存远程文件失败: "+err.Error()))
			return
		}
	}

	c.JSON(http.StatusOK, convention.Success(resp))
}

type internalURLUploadResp struct {
	Total     int                 `json:"total"`
	Success   int                 `json:"success"`
	Failed    int                 `json:"failed"`
	Documents []*dto.DocumentResp `json:"documents"`
	Errors    []string            `json:"errors,omitempty"`
}

func (h *DocumentHandler) uploadInternalURLDocuments(c *gin.Context, kbID string, req dto.CreateDocumentReq, operator string) {
	if h.internalURLFetcher == nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", "内部URL拉取器未配置"))
		return
	}
	kb, err := h.svc.GetKnowledgeBase(c.Request.Context(), kbID)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", "查询知识库失败: "+err.Error()))
		return
	}
	docs, err := h.internalURLFetcher.FetchDocuments(c.Request.Context(), req.SourceLocation)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	if len(docs) == 0 {
		c.JSON(http.StatusOK, convention.Failure("B000001", "未读取到内部文档内容"))
		return
	}
	if h.fileStore == nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", "文件存储未配置"))
		return
	}
	resp := internalURLUploadResp{Total: len(docs)}
	parentURL := strings.TrimSpace(req.SourceLocation)
	for idx, doc := range docs {
		docReq := req
		docReq.SourceType = "internal_url"
		docReq.SourceLocation = firstNonEmpty(doc.Meta.URL, req.SourceLocation)
		if req.ScheduleEnabled == 1 {
			docReq.SourceLocation = firstNonEmpty(parentURL, doc.Meta.URL)
		}
		docReq.DocName = firstNonEmpty(doc.Meta.Title, doc.Meta.ID)
		docReq.FileURL = firstNonEmpty(doc.Meta.URL, req.SourceLocation)
		docReq.FileType = internalURLFileType(doc.Meta.MimeType, docReq.DocName)
		docReq.FileSize = int64(len(doc.Content))
		if req.ScheduleEnabled == 1 && idx > 0 {
			docReq.ScheduleEnabled = 0
			docReq.ScheduleCron = ""
		}
		created, err := h.svc.CreateDocument(c.Request.Context(), kbID, docReq, operator)
		if err != nil {
			resp.Failed++
			resp.Errors = append(resp.Errors, err.Error())
			continue
		}
		if err := h.fileStore.PutWithCollection(c.Request.Context(), kb.CollectionName, created.ID, docReq.DocName, doc.Content); err != nil {
			resp.Failed++
			resp.Errors = append(resp.Errors, "保存内部文档失败: "+err.Error())
			continue
		}
		resp.Success++
		resp.Documents = append(resp.Documents, created)
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

func internalURLFileType(mimeType, docName string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "text/markdown":
		return "md"
	case "text/plain":
		if strings.HasSuffix(strings.ToLower(docName), ".md") {
			return "md"
		}
		return "txt"
	case "text/html":
		return "html"
	case "application/pdf":
		return "pdf"
	}
	if ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(docName)), "."); ext != "" {
		return ext
	}
	return "md"
}

func fetchRemoteUploadFile(ctx context.Context, rawURL string) ([]byte, string, string, error) {
	location := strings.TrimSpace(rawURL)
	if location == "" {
		return nil, "", "", fmt.Errorf("来源地址不能为空")
	}
	parsed, err := url.Parse(location)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, "", "", fmt.Errorf("来源地址不合法")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, "", "", fmt.Errorf("来源地址仅支持 http/https")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, "", "", fmt.Errorf("创建远程文件请求失败: %w", err)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", "", fmt.Errorf("获取远程文件失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, "", "", fmt.Errorf("获取远程文件失败: HTTP %d", resp.StatusCode)
	}
	const maxUploadBytes = 50 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxUploadBytes+1))
	if err != nil {
		return nil, "", "", fmt.Errorf("读取远程文件失败: %w", err)
	}
	if len(data) > maxUploadBytes {
		return nil, "", "", fmt.Errorf("远程文件超过 50MB 限制")
	}
	name := remoteUploadFilename(resp.Header.Get("Content-Disposition"), parsed.Path)
	fileType := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	if fileType == "" {
		fileType = uploadFileTypeFromContentType(resp.Header.Get("Content-Type"))
	}
	if fileType == "" {
		fileType = "unknown"
	}
	if err := validateRemoteUploadContent(ctx, data, fileType, resp.Header.Get("Content-Type"), location); err != nil {
		return nil, "", "", err
	}
	return data, name, fileType, nil
}

func validateRemoteUploadContent(ctx context.Context, data []byte, fileType, contentType, sourceURL string) error {
	if strings.TrimSpace(string(data)) == "" {
		return fmt.Errorf("读取远程文档内容失败: 未读取到有效文档内容")
	}
	mimeType := remoteUploadMIMEType(fileType, contentType)
	registry := coreparser.DefaultRegistry()
	if !registry.Supports(mimeType) {
		return fmt.Errorf("读取远程文档内容失败: 暂不支持的远程文件类型 %s", strings.TrimSpace(fileType))
	}
	parsed, err := registry.Parse(ctx, data, mimeType, map[string]string{
		"sourceURL": sourceURL,
	})
	if err != nil {
		return fmt.Errorf("读取远程文档内容失败: %w", err)
	}
	text := strings.Join(strings.Fields(rag.RenderBlocks(parsed.Blocks)), " ")
	if !hasMeaningfulRemoteDocumentText(text) {
		return fmt.Errorf("读取远程文档内容失败: 未读取到有效文档内容")
	}
	if looksLikeRemoteAccessGate(text) {
		return fmt.Errorf("读取远程文档内容失败: 未读取到有效文档内容，可能需要登录或没有访问权限")
	}
	return nil
}

func remoteUploadMIMEType(fileType, contentType string) string {
	if mediaType, _, err := mime.ParseMediaType(contentType); err == nil && strings.TrimSpace(mediaType) != "" {
		return strings.ToLower(strings.TrimSpace(mediaType))
	}
	switch strings.ToLower(strings.TrimPrefix(strings.TrimSpace(fileType), ".")) {
	case "md", "markdown":
		return "text/markdown"
	case "txt", "text":
		return "text/plain"
	case "csv":
		return "text/csv"
	case "html", "htm":
		return "text/html"
	case "pdf":
		return "application/pdf"
	case "docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case "pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case "xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "xml":
		return "application/xml"
	case "json":
		return "application/json"
	default:
		return "application/octet-stream"
	}
}

func hasMeaningfulRemoteDocumentText(text string) bool {
	compact := strings.Join(strings.Fields(text), "")
	return len([]rune(compact)) >= 8
}

func looksLikeRemoteAccessGate(text string) bool {
	compact := strings.Join(strings.Fields(text), "")
	compactLen := len([]rune(compact))
	lower := strings.ToLower(strings.Join(strings.Fields(text), " "))
	strongPhrases := []string{
		"请登录",
		"未登录",
		"无权限",
		"没有权限",
		"访问受限",
		"禁止访问",
		"登录后",
		"access denied",
		"forbidden",
		"unauthorized",
		"permission denied",
		"please enable javascript",
		"redirecting",
	}
	for _, phrase := range strongPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	if compactLen >= 30 {
		return false
	}
	weakPhrases := []string{"登录", "sign in", "log in", "login"}
	for _, phrase := range weakPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

func remoteUploadFilename(disposition, urlPath string) string {
	if disposition != "" {
		if _, params, err := mime.ParseMediaType(disposition); err == nil {
			if filename := strings.TrimSpace(params["filename"]); filename != "" {
				return filepath.Base(filename)
			}
		}
	}
	name := filepath.Base(urlPath)
	if name == "." || name == "/" || name == "" {
		return "remote-document"
	}
	return name
}

func uploadFileTypeFromContentType(contentType string) string {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.TrimSpace(strings.ToLower(contentType))
	}
	switch mediaType {
	case "text/markdown":
		return "md"
	case "text/plain":
		return "txt"
	case "text/csv":
		return "csv"
	case "application/pdf":
		return "pdf"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return "docx"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return "xlsx"
	case "application/vnd.ms-excel":
		return "xls"
	case "text/html":
		return "html"
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/svg+xml":
		return "svg"
	default:
		return ""
	}
}

func parseInt16(value string) int16 {
	switch strings.TrimSpace(value) {
	case "1", "true", "TRUE", "True":
		return 1
	default:
		return 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// ListDocs GET /knowledge-base/:id/docs
func (h *DocumentHandler) ListDocs(c *gin.Context) {
	kbID := c.Param("id")
	page, size := pagination(c)
	records, total, err := h.svc.ListDocumentsByKB(c.Request.Context(), kbID, page, size, c.Query("status"), c.Query("keyword"))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(convention.NewPageResp(records, total, page, size)))
}

// GetDoc GET /knowledge-base/docs/:docId
func (h *DocumentHandler) GetDoc(c *gin.Context) {
	id := c.Param("docId")
	resp, err := h.svc.GetDocument(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// UpdateDoc PUT /knowledge-base/docs/:docId
func (h *DocumentHandler) UpdateDoc(c *gin.Context) {
	id := c.Param("docId")
	var req dto.UpdateDocumentReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", fmt.Sprintf("参数校验失败: %v", err)))
		return
	}
	_, err := h.svc.UpdateDocument(c.Request.Context(), id, req, userID(c))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// DeleteDoc DELETE /knowledge-base/docs/:docId
func (h *DocumentHandler) DeleteDoc(c *gin.Context) {
	id := c.Param("docId")
	if err := h.svc.DeleteDocument(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// SearchDocs GET /knowledge-base/docs/search
func (h *DocumentHandler) SearchDocs(c *gin.Context) {
	keyword := c.Query("keyword")
	limit := parseSearchLimit(c)
	records, err := h.svc.SearchDocuments(c.Request.Context(), keyword, limit)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	if len(records) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"code":    "0",
			"data":    []dto.DocumentResp{},
			"message": "",
		})
		return
	}
	c.JSON(http.StatusOK, convention.Success(records))
}

// ToggleDoc PATCH /knowledge-base/docs/:docId/enable
func (h *DocumentHandler) ToggleDoc(c *gin.Context) {
	id := c.Param("docId")
	enabled, ok, err := enabledFromValueQuery(c)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", err.Error()))
		return
	}
	if !ok {
		var req dto.ToggleChunkReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusOK, convention.Failure("A000001", fmt.Sprintf("参数校验失败: %v", err)))
			return
		}
		enabled = req.Enabled
	}
	if err := h.svc.ToggleDocument(c.Request.Context(), id, enabled); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// ListChunks GET /knowledge-base/docs/:docId/chunks
func (h *DocumentHandler) ListChunks(c *gin.Context) {
	docID := c.Param("docId")
	page, size := pagination(c)
	enabled, err := optionalInt16Query(c, "enabled")
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", err.Error()))
		return
	}
	records, total, err := h.svc.ListChunks(c.Request.Context(), docID, page, size, enabled)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(convention.NewPageResp(records, total, page, size)))
}

// UpdateChunk PUT /knowledge-base/docs/:docId/chunks/:chunkId
func (h *DocumentHandler) UpdateChunk(c *gin.Context) {
	docID := c.Param("docId")
	chunkID := c.Param("chunkId")
	var req dto.UpdateChunkReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", fmt.Sprintf("参数校验失败: %v", err)))
		return
	}
	_, err := h.svc.UpdateChunk(c.Request.Context(), docID, chunkID, req, userID(c))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// DeleteChunk DELETE /knowledge-base/docs/:docId/chunks/:chunkId
func (h *DocumentHandler) DeleteChunk(c *gin.Context) {
	docID := c.Param("docId")
	chunkID := c.Param("chunkId")
	if err := h.svc.DeleteChunk(c.Request.Context(), docID, chunkID); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// ToggleChunk PATCH /knowledge-base/docs/:docId/chunks/:chunkId/enable
func (h *DocumentHandler) ToggleChunk(c *gin.Context) {
	docID := c.Param("docId")
	chunkID := c.Param("chunkId")
	enabled, ok, err := enabledFromValueQuery(c)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", err.Error()))
		return
	}
	if !ok {
		var req dto.ToggleChunkReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusOK, convention.Failure("A000001", fmt.Sprintf("参数校验失败: %v", err)))
			return
		}
		enabled = req.Enabled
	}
	if err := h.svc.ToggleChunk(c.Request.Context(), docID, chunkID, enabled); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// BatchToggleChunks PATCH /knowledge-base/docs/:docId/chunks/batch-enable
func (h *DocumentHandler) BatchToggleChunks(c *gin.Context) {
	docID := c.Param("docId")
	var req dto.BatchEnableChunksReq
	if err := c.ShouldBindJSON(&req); err != nil && err != io.EOF {
		c.JSON(http.StatusOK, convention.Failure("A000001", fmt.Sprintf("参数校验失败: %v", err)))
		return
	}
	enabled, ok, err := enabledFromValueQuery(c)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", err.Error()))
		return
	}
	if !ok {
		enabled = req.Enabled
	}
	if len(req.IDs) == 0 {
		req.IDs = req.ChunkIDs
	}
	if err := h.svc.BatchToggleChunks(c.Request.Context(), docID, req.IDs, enabled); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

func enabledFromValueQuery(c *gin.Context) (int16, bool, error) {
	raw, ok := c.GetQuery("value")
	if !ok {
		return 0, false, nil
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true":
		return 1, true, nil
	case "0", "false":
		return 0, true, nil
	default:
		return 0, true, fmt.Errorf("value参数必须为true或false")
	}
}

func parseSearchLimit(c *gin.Context) int {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "8"))
	if err != nil || limit < 1 {
		return 8
	}
	return limit
}

func optionalInt16Query(c *gin.Context, name string) (*int16, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return nil, fmt.Errorf("%s参数必须为数字", name)
	}
	v := int16(value)
	return &v, nil
}

// ChunkLogs GET /knowledge-base/docs/:docId/chunk-logs
func (h *DocumentHandler) ChunkLogs(c *gin.Context) {
	docID := c.Param("docId")
	page, size := pagination(c)
	logs, total, err := h.svc.GetChunkLogs(c.Request.Context(), docID, page, size)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(convention.NewPageResp(logs, total, page, size)))
}

// ChunkDoc POST /knowledge-base/docs/:docId/chunk
func (h *DocumentHandler) ChunkDoc(c *gin.Context) {
	docID := c.Param("docId")
	if err := h.svc.StartChunk(c.Request.Context(), docID, userID(c)); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(map[string]string(nil)))
}

// CreateChunk POST /knowledge-base/docs/:docId/chunks
func (h *DocumentHandler) CreateChunk(c *gin.Context) {
	docID := c.Param("docId")
	var req dto.CreateChunkReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", fmt.Sprintf("参数校验失败: %v", err)))
		return
	}
	resp, err := h.svc.CreateChunk(c.Request.Context(), docID, req, userID(c))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// Preview GET /knowledge-base/docs/:docId/preview
func (h *DocumentHandler) Preview(c *gin.Context) {
	docID := c.Param("docId")
	content, err := h.svc.PreviewDocument(c.Request.Context(), docID)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(content))
}

// File GET /knowledge-base/docs/:docId/file
func (h *DocumentHandler) File(c *gin.Context) {
	docID := c.Param("docId")
	if h.fileStore != nil {
		if h.svc != nil {
			if doc, err := h.svc.GetDocument(c.Request.Context(), docID); err == nil && doc != nil {
				if kb, err := h.svc.GetKnowledgeBase(c.Request.Context(), doc.KbID); err == nil && kb != nil {
					if f, ok, err := h.fileStore.GetWithCollection(c.Request.Context(), kb.CollectionName, docID); err == nil && ok {
						sendStoredFile(c, f)
						return
					}
				}
			}
		}
		if f, ok := h.fileStore.Get(docID); ok {
			sendStoredFile(c, f)
			return
		}
	}
	c.JSON(http.StatusOK, convention.Failure("B000001", "读取文档源文件失败"))
}

func sendStoredFile(c *gin.Context, f *storedFile) {
	contentType := f.ContentType
	if contentType == "" || contentType == "application/octet-stream" {
		contentType = detectMIME(f.Name)
	}
	if strings.TrimSpace(f.Name) != "" {
		c.Header("Content-Disposition", `inline; filename="`+url.QueryEscape(f.Name)+`"`)
	}
	c.Data(http.StatusOK, contentType, f.Data)
}

func detectMIME(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case ".ppt":
		return "application/vnd.ms-powerpoint"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".md", ".markdown":
		return "text/markdown; charset=utf-8"
	case ".xml":
		return "application/xml; charset=utf-8"
	case ".csv":
		return "text/csv; charset=utf-8"
	case ".xls":
		return "application/vnd.ms-excel"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".txt", ".json":
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}
