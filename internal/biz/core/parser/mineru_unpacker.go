package parser

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"

	"go-base-agent/internal/biz/rag"
	"go-base-agent/internal/infra/storage"
	"go-base-agent/internal/infra/vlm"
)

// MinerUUnpackerOptions 配置解包器行为。
type MinerUUnpackerOptions struct {
	// EmbeddedDescribeEnabled 内嵌图是否调 VLM 图生文（对齐 Java imageParseProperties.embeddedDescribeEnabled，默认开）
	EmbeddedDescribeEnabled bool
	// DescriptionPrompt 图生文提示词
	DescriptionPrompt string
	// MaxOutputTokens 图生文输出上限
	MaxOutputTokens int
}

// defaultMinerUDescribePrompt 内嵌图图生文默认提示词（对齐 Java ImageParseProperties 默认值）。
const defaultMinerUDescribePrompt = "请用中文详细描述这张图片的内容；若图中包含文字，请逐字识别并完整列出（OCR）。" +
	"先给出整体内容描述，再用\"图中文字：\"另起一段列出识别到的所有文字。"

// MinerUResultUnpacker 将 MinerU zip 结果解包为 ParsedDocument。
type MinerUResultUnpacker struct {
	uploader   storage.Uploader
	vlmService vlm.Service
	opts       MinerUUnpackerOptions
}

// NewMinerUResultUnpacker 创建解包器。vlmService 为 nil 或未开启内嵌描述时跳过图生文。
func NewMinerUResultUnpacker(uploader storage.Uploader, vlmService vlm.Service, opts MinerUUnpackerOptions) *MinerUResultUnpacker {
	if strings.TrimSpace(opts.DescriptionPrompt) == "" {
		opts.DescriptionPrompt = defaultMinerUDescribePrompt
	}
	if opts.MaxOutputTokens <= 0 {
		opts.MaxOutputTokens = 1024
	}
	return &MinerUResultUnpacker{uploader: uploader, vlmService: vlmService, opts: opts}
}

// Unpack 解包 zip 字节并返回结构化文档。
func (u *MinerUResultUnpacker) Unpack(ctx context.Context, zipBytes []byte, sourceFile, documentID string) (*rag.ParsedDocument, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if len(zipBytes) == 0 {
		return nil, fmt.Errorf("mineru zip bytes are empty")
	}
	markdown, images, err := readMinerUZip(zipBytes)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(markdown) == "" {
		return nil, fmt.Errorf("mineru zip missing markdown content")
	}
	rewritten, uploaded, uploadedURLs, err := u.rewriteImages(ctx, markdown, images, documentID)
	if err != nil {
		return nil, err
	}
	descriptions := u.describeImages(ctx, images)

	parsed, err := (&MarkdownParser{}).Parse(ctx, []byte(rewritten), "text/markdown", map[string]string{
		"sourceFile": sourceFile,
		"documentId": documentID,
	})
	if err != nil {
		return nil, fmt.Errorf("parse mineru markdown: %w", err)
	}
	enrichMinerUImageBlocks(parsed.Blocks, uploadedURLs, descriptions)
	if parsed.Metadata == nil {
		parsed.Metadata = make(map[string]string)
	}
	parsed.Metadata["parser"] = string(rag.ParserMinerU)
	parsed.Metadata["sourceFile"] = sourceFile
	parsed.Metadata["documentId"] = documentID
	parsed.Metadata["imagesUploaded"] = fmt.Sprintf("%d", uploaded)
	parsed.Metadata["imagesDescribed"] = fmt.Sprintf("%d", len(descriptions))
	return parsed, nil
}

// enrichMinerUImageBlocks 把 zip 内路径的 VLM 描述回填到对应 ImageBlock：
// Markdown 解析产出的 ImageBlock 持有的是资产桶 URL，经上传映射（URL → zip 路径）反查再取描述。
func enrichMinerUImageBlocks(blocks []rag.Block, uploadedURLs map[string]string, descriptions map[string]string) {
	if len(uploadedURLs) == 0 || len(descriptions) == 0 {
		return
	}
	for i := range blocks {
		if blocks[i].Type != rag.BlockImage {
			continue
		}
		zipPath, ok := uploadedURLs[blocks[i].Asset.PublicURL]
		if !ok {
			continue
		}
		if desc := descriptions[zipPath]; desc != "" {
			blocks[i].Description = desc
		}
	}
}

func readMinerUZip(zipBytes []byte) (string, map[string][]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return "", nil, fmt.Errorf("open mineru zip: %w", err)
	}
	var markdown string
	images := make(map[string][]byte)
	for _, f := range reader.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", nil, fmt.Errorf("open mineru entry %s: %w", f.Name, err)
		}
		data, readErr := io.ReadAll(rc)
		_ = rc.Close()
		if readErr != nil {
			return "", nil, fmt.Errorf("read mineru entry %s: %w", f.Name, readErr)
		}
		lower := strings.ToLower(f.Name)
		switch {
		case strings.HasSuffix(lower, ".md") && markdown == "":
			markdown = string(data)
		case isMinerUImage(lower):
			copyData := make([]byte, len(data))
			copy(copyData, data)
			images[f.Name] = copyData
		}
	}
	return markdown, images, nil
}

func isMinerUImage(name string) bool {
	switch {
	case strings.HasSuffix(name, ".png"),
		strings.HasSuffix(name, ".jpg"),
		strings.HasSuffix(name, ".jpeg"),
		strings.HasSuffix(name, ".webp"),
		strings.HasSuffix(name, ".gif"),
		strings.HasSuffix(name, ".bmp"):
		return true
	default:
		return false
	}
}

// rewriteImages 改写 markdown 里的图片地址为资产桶 URL，返回改写结果、上传张数与
// 上传映射（资产桶 URL → zip 内路径，供描述回填反查）。
func (u *MinerUResultUnpacker) rewriteImages(ctx context.Context, markdown string, images map[string][]byte, documentID string) (string, int, map[string]string, error) {
	if len(images) == 0 || u == nil || u.uploader == nil {
		return markdown, 0, nil, nil
	}
	re := regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
	uploadURLs := make(map[string]string, len(images))
	uploadedByURL := make(map[string]string, len(images))
	uploaded := 0
	var uploadErr error
	result := re.ReplaceAllStringFunc(markdown, func(match string) string {
		sub := re.FindStringSubmatch(match)
		if len(sub) != 3 {
			return match
		}
		altText := sub[1]
		ref := strings.TrimSpace(sub[2])
		if ref == "" || strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "data:") {
			return match
		}
		zipPath, url, cached := "", "", false
		if url, cached = uploadURLs[ref]; !cached {
			var resolved bool
			zipPath, resolved = resolveMinerUZipPath(ref, images)
			if !resolved {
				return match
			}
			data := images[zipPath]
			key := imageAssetKey(documentID, inferImageMime(zipPath))
			publicURL, err := u.uploader.Upload(ctx, key, data, inferImageMime(zipPath))
			if err != nil {
				uploadErr = err
				return match
			}
			uploadURLs[ref] = publicURL
			uploadedByURL[publicURL] = zipPath
			uploaded++
			url = publicURL
		}
		if altText == "" {
			return "![](" + url + ")"
		}
		return "![" + altText + "](" + url + ")"
	})
	if uploadErr != nil {
		return markdown, uploaded, uploadedByURL, fmt.Errorf("upload mineru image failed: %w", uploadErr)
	}
	return result, uploaded, uploadedByURL, nil
}

// describeImages 逐张图生文，键为 zip 内路径。
// 单张失败只记日志不中断，一张插图召不回来远好过整篇文档入库失败；串行调用，
// 多图文档解析会明显变慢，但 MinerU 云端解析本就以分钟计（对齐 Java describeImages）。
func (u *MinerUResultUnpacker) describeImages(ctx context.Context, images map[string][]byte) map[string]string {
	if u.vlmService == nil || !u.opts.EmbeddedDescribeEnabled || len(images) == 0 {
		return nil
	}
	result := make(map[string]string, len(images))
	for zipPath, data := range images {
		description, err := u.vlmService.DescribeImage(ctx, data, inferImageMime(zipPath), u.opts.DescriptionPrompt, u.opts.MaxOutputTokens)
		if err != nil {
			slog.Warn("MinerU 内嵌图图生文失败，该图向量文本将只剩链接", "zipPath", zipPath, "err", err)
			continue
		}
		if description = strings.TrimSpace(description); description != "" {
			result[zipPath] = description
		}
	}
	return result
}

// resolveMinerUZipPath 把 markdown 里的图片地址还原成 zip 内路径。
// 优先精确匹配；MinerU markdown 里可能写 ./images/xxx 也可能写 images/xxx，剥掉 ./ 前缀再匹配；
// 最后用文件名兜底匹配（对齐 Java resolveZipPath）。
func resolveMinerUZipPath(rawDest string, images map[string][]byte) (string, bool) {
	if rawDest == "" {
		return "", false
	}
	if _, ok := images[rawDest]; ok {
		return rawDest, true
	}
	norm := strings.TrimPrefix(rawDest, "./")
	if _, ok := images[norm]; ok {
		return norm, true
	}
	idx := strings.LastIndex(norm, "/")
	fileName := norm
	if idx >= 0 {
		fileName = norm[idx+1:]
	}
	for key := range images {
		if key == fileName || strings.HasSuffix(key, "/"+fileName) {
			return key, true
		}
	}
	return "", false
}

func inferImageMime(name string) string {
	switch {
	case strings.HasSuffix(strings.ToLower(name), ".jpg"), strings.HasSuffix(strings.ToLower(name), ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(strings.ToLower(name), ".svg"):
		return "image/svg+xml"
	default:
		return "image/png"
	}
}
