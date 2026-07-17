package parser

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"go-base-agent/internal/biz/rag"
	"go-base-agent/internal/infra/storage"
	"go-base-agent/internal/infra/vlm"

	"github.com/google/uuid"
)

// ImageParser 解析独立图片文档并产出可检索描述。
type ImageParser struct {
	vlmService vlm.Service
	uploader   storage.Uploader
	prompt     string
}

// NewImageParser 创建图片解析器。
func NewImageParser(vlmService vlm.Service, uploader storage.Uploader) *ImageParser {
	return &ImageParser{
		vlmService: vlmService,
		uploader:   uploader,
		prompt:     "请用中文准确描述这张图片的内容，优先提取图片中的文字、表格、流程和关键事实，输出适合检索的简洁描述。",
	}
}

func (p *ImageParser) Type() rag.ParserType { return rag.ParserImage }

func (p *ImageParser) Supports(mimeType string) bool {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png", "image/jpeg", "image/jpg", "image/svg+xml":
		return true
	default:
		return false
	}
}

func (p *ImageParser) Parse(ctx context.Context, data []byte, mimeType string, options map[string]string) (*rag.ParsedDocument, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("image parser input is empty")
	}
	if p.vlmService == nil {
		return nil, fmt.Errorf("vlm service is not configured")
	}

	sourceFile := parseOption(options, "sourceFile")
	documentID := parseOption(options, "documentId")
	sourceURL := parseOption(options, "sourceURL")
	if documentID == "" {
		documentID = uuid.NewString()
	}
	if sourceFile == "" {
		sourceFile = documentID
	}

	description, err := p.vlmService.DescribeImage(ctx, data, mimeType, p.prompt)
	if err != nil {
		return nil, fmt.Errorf("describe image failed: %w", err)
	}
	description = strings.TrimSpace(description)
	if description == "" {
		return nil, fmt.Errorf("vlm returned empty description")
	}

	publicURL := sourceURL
	if p.uploader != nil {
		key := imageAssetKey(documentID, mimeType)
		publicURL, err = p.uploader.Upload(ctx, key, data, mimeType)
		if err != nil {
			return nil, fmt.Errorf("upload image asset failed: %w", err)
		}
	}
	blockID := uuid.NewString()
	caption := stripExt(filepath.Base(sourceFile))
	block := rag.Block{
		Type:        rag.BlockImage,
		Asset:       rag.AssetRef{PublicURL: publicURL, Mime: mimeType, SourceBlockID: blockID},
		AltText:     caption,
		Description: description,
		Content:     description,
	}

	return &rag.ParsedDocument{
		Blocks: []rag.Block{block},
		Metadata: map[string]string{
			"parser":           string(rag.ParserImage),
			"mimeType":         mimeType,
			"sourceFile":       sourceFile,
			"documentId":       documentID,
			"sourceURL":        sourceURL,
			"assetURL":         publicURL,
			"descriptionChars": fmt.Sprintf("%d", len([]rune(description))),
			"parsedAt":         time.Now().Format(time.RFC3339),
		},
	}, nil
}

func imageAssetKey(documentID, mimeType string) string {
	ext := "png"
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg", "image/jpg":
		ext = "jpg"
	case "image/svg+xml":
		ext = "svg"
	}
	return "assets/" + documentID + "/" + uuid.NewString() + "." + ext
}

func stripExt(name string) string {
	if name == "" {
		return ""
	}
	ext := filepath.Ext(name)
	if ext == "" {
		return name
	}
	return strings.TrimSuffix(name, ext)
}

func parseOption(options map[string]string, key string) string {
	if len(options) == 0 {
		return ""
	}
	return strings.TrimSpace(options[key])
}
