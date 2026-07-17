package parser

import (
	"archive/zip"
	"bytes"
	"context"
	"strings"
	"testing"

	"go-base-agent/internal/biz/rag"

	"github.com/jung-kurt/gofpdf"
)

func TestPDFParserExtractsText(t *testing.T) {
	var buf bytes.Buffer
	doc := gofpdf.New("P", "mm", "A4", "")
	doc.AddPage()
	doc.SetFont("Arial", "", 12)
	doc.Cell(40, 10, "member agent supports troubleshooting")
	if err := doc.Output(&buf); err != nil {
		t.Fatalf("create pdf: %v", err)
	}

	parsed, err := (&PDFParser{}).Parse(context.Background(), buf.Bytes(), "application/pdf", nil)
	if err != nil {
		t.Fatalf("parse pdf: %v", err)
	}
	text := rag.RenderBlocks(parsed.Blocks)
	if !strings.Contains(text, "member agent supports troubleshooting") {
		t.Fatalf("expected extracted pdf text, got %q", text)
	}
	if strings.Contains(text, "requires") {
		t.Fatalf("pdf parser returned placeholder text: %q", text)
	}
}

func TestDOCXParserExtractsDocumentText(t *testing.T) {
	data := zipBytes(t, map[string]string{
		"word/document.xml": `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>会员 Agent 支持</w:t></w:r><w:r><w:t>权益查询</w:t></w:r></w:p>
    <w:p><w:r><w:t>支持积分查询</w:t></w:r></w:p>
  </w:body>
</w:document>`,
	})

	parsed, err := (&DOCXParser{}).Parse(context.Background(), data, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", nil)
	if err != nil {
		t.Fatalf("parse docx: %v", err)
	}
	text := rag.RenderBlocks(parsed.Blocks)
	if !strings.Contains(text, "会员 Agent 支持权益查询") || !strings.Contains(text, "支持积分查询") {
		t.Fatalf("expected docx text, got %q", text)
	}
	if strings.Contains(text, "requires") {
		t.Fatalf("docx parser returned placeholder text: %q", text)
	}
}

func TestCSVParserProducesTableBlock(t *testing.T) {
	parsed, err := (&CSVParser{}).Parse(context.Background(), []byte("能力,说明\n权益查询,\"查看等级, 权益\"\n积分查询,查看积分"), "text/csv", nil)
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(parsed.Blocks) != 1 || parsed.Blocks[0].Type != rag.BlockTable {
		t.Fatalf("expected one table block, got %+v", parsed.Blocks)
	}
	if got := parsed.Blocks[0].Rows[0][1]; got != "查看等级, 权益" {
		t.Fatalf("unexpected csv cell: %q", got)
	}
}

func TestXLSXParserProducesTableBlock(t *testing.T) {
	data := zipBytes(t, map[string]string{
		"xl/sharedStrings.xml": `<?xml version="1.0" encoding="UTF-8"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <si><t>能力</t></si><si><t>说明</t></si><si><t>权益查询</t></si><si><t>查看等级</t></si>
</sst>`,
		"xl/worksheets/sheet1.xml": `<?xml version="1.0" encoding="UTF-8"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <sheetData>
    <row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>
    <row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2" t="s"><v>3</v></c></row>
  </sheetData>
</worksheet>`,
	})

	parsed, err := (&XLSXParser{}).Parse(context.Background(), data, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", nil)
	if err != nil {
		t.Fatalf("parse xlsx: %v", err)
	}
	if len(parsed.Blocks) != 1 || parsed.Blocks[0].Type != rag.BlockTable {
		t.Fatalf("expected one table block, got %+v", parsed.Blocks)
	}
	if got := parsed.Blocks[0].Rows[0][0]; got != "权益查询" {
		t.Fatalf("unexpected xlsx cell: %q", got)
	}
}

func TestMarkdownParserProducesStructuredBlocks(t *testing.T) {
	md := "# 会员 Agent\n\n- 权益查询\n- 积分查询\n\n| 能力 | 说明 |\n| --- | --- |\n| 错误排查 | 支持 |\n"
	parsed, err := (&MarkdownParser{}).Parse(context.Background(), []byte(md), "text/markdown", nil)
	if err != nil {
		t.Fatalf("parse markdown: %v", err)
	}
	var hasList, hasTable bool
	for _, block := range parsed.Blocks {
		if block.Type == rag.BlockList && len(block.Items) == 2 {
			hasList = true
		}
		if block.Type == rag.BlockTable && len(block.Headers) == 2 && len(block.Rows) == 1 {
			hasTable = true
		}
	}
	if !hasList || !hasTable {
		t.Fatalf("expected list and table blocks, got %+v", parsed.Blocks)
	}
}

func TestDefaultRegistryRegistersConcreteParsers(t *testing.T) {
	reg := DefaultRegistry()
	if !reg.Supports("text/csv") || !reg.Supports("application/vnd.openxmlformats-officedocument.spreadsheetml.sheet") {
		t.Fatalf("default registry should support csv and xlsx")
	}
}

func TestImageParserParsesImageWithDescriptionAndAsset(t *testing.T) {
	vlmSvc := &fakeVLMService{desc: "这是一张会员能力说明图片"}
	uploader := &fakeUploader{url: "https://assets.example.com/image.png"}
	p := NewImageParser(vlmSvc, uploader)

	parsed, err := p.Parse(context.Background(), []byte("fake-image"), "image/png", map[string]string{
		"sourceFile": "会员能力.png",
		"documentId": "doc-1",
	})
	if err != nil {
		t.Fatalf("parse image: %v", err)
	}
	if len(parsed.Blocks) != 1 || parsed.Blocks[0].Type != rag.BlockImage {
		t.Fatalf("expected one image block, got %+v", parsed.Blocks)
	}
	if got := parsed.Blocks[0].Description; got != "这是一张会员能力说明图片" {
		t.Fatalf("unexpected description: %q", got)
	}
	if got := parsed.Blocks[0].Asset.PublicURL; got != uploader.url {
		t.Fatalf("unexpected asset url: %q", got)
	}
	if vlmSvc.calls != 1 {
		t.Fatalf("expected one VLM call, got %d", vlmSvc.calls)
	}
	if uploader.calls != 1 {
		t.Fatalf("expected one upload call, got %d", uploader.calls)
	}
}

func TestMinerUResultUnpackerRewritesImageLinks(t *testing.T) {
	data := zipBytes(t, map[string]string{
		"result.md":       "## 标题\n\n![图 1](images/fig1.png)\n\n正文说明",
		"images/fig1.png": "png-bytes",
	})
	uploader := &fakeUploader{url: "https://assets.example.com/fig1.png"}
	unpacker := NewMinerUResultUnpacker(uploader)

	parsed, err := unpacker.Unpack(context.Background(), data, "会员能力说明.md", "doc-1")
	if err != nil {
		t.Fatalf("unpack mineru zip: %v", err)
	}
	text := rag.RenderBlocks(parsed.Blocks)
	if !strings.Contains(text, uploader.url) {
		t.Fatalf("expected rewritten image url, got %q", text)
	}
	if parsed.Metadata["imagesUploaded"] != "1" {
		t.Fatalf("expected one uploaded image, got %+v", parsed.Metadata)
	}
}

type fakeVLMService struct {
	desc  string
	calls int
}

func (f *fakeVLMService) DescribeImage(ctx context.Context, image []byte, mimeType, prompt string) (string, error) {
	f.calls++
	return f.desc, nil
}

type fakeUploader struct {
	url   string
	calls int
}

func (f *fakeUploader) Upload(ctx context.Context, key string, data []byte, contentType string) (string, error) {
	f.calls++
	return f.url, nil
}

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}
