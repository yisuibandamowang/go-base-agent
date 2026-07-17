package parser

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	"go-base-agent/internal/biz/rag"
	"go-base-agent/internal/infra/storage"
)

// MinerUResultUnpacker 将 MinerU zip 结果解包为 ParsedDocument。
type MinerUResultUnpacker struct {
	uploader storage.Uploader
}

// NewMinerUResultUnpacker 创建解包器。
func NewMinerUResultUnpacker(uploader storage.Uploader) *MinerUResultUnpacker {
	return &MinerUResultUnpacker{uploader: uploader}
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
	rewritten, uploaded, err := u.rewriteImages(ctx, markdown, images, documentID)
	if err != nil {
		return nil, err
	}

	parsed, err := (&MarkdownParser{}).Parse(ctx, []byte(rewritten), "text/markdown", map[string]string{
		"sourceFile": sourceFile,
		"documentId": documentID,
	})
	if err != nil {
		return nil, fmt.Errorf("parse mineru markdown: %w", err)
	}
	if parsed.Metadata == nil {
		parsed.Metadata = make(map[string]string)
	}
	parsed.Metadata["parser"] = string(rag.ParserMinerU)
	parsed.Metadata["sourceFile"] = sourceFile
	parsed.Metadata["documentId"] = documentID
	parsed.Metadata["imagesUploaded"] = fmt.Sprintf("%d", uploaded)
	return parsed, nil
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

func (u *MinerUResultUnpacker) rewriteImages(ctx context.Context, markdown string, images map[string][]byte, documentID string) (string, int, error) {
	if len(images) == 0 || u == nil || u.uploader == nil {
		return markdown, 0, nil
	}
	re := regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
	uploadURLs := make(map[string]string, len(images))
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
		url, ok := uploadURLs[ref]
		if !ok {
			data, exists := images[ref]
			if !exists {
				return match
			}
			key := imageAssetKey(documentID, inferImageMime(ref))
			publicURL, err := u.uploader.Upload(ctx, key, data, inferImageMime(ref))
			if err != nil {
				uploadErr = err
				return match
			}
			uploadURLs[ref] = publicURL
			url = publicURL
			uploaded++
		}
		if altText == "" {
			return "![](" + url + ")"
		}
		return "![" + altText + "](" + url + ")"
	})
	if uploadErr != nil {
		return markdown, uploaded, fmt.Errorf("upload mineru image failed: %w", uploadErr)
	}
	return result, uploaded, nil
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
