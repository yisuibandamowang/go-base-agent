package parser

import (
	"archive/zip"
	"bytes"
	"context"
	"image/png"
	"strings"
	"testing"

	"go-base-agent/internal/biz/rag"

	"github.com/jung-kurt/gofpdf"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
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

func TestCSVParserCarriesSourceFileProvenance(t *testing.T) {
	parsed, err := (&CSVParser{}).Parse(context.Background(), []byte("能力,说明\n权益查询,查看等级\n"), "text/csv", map[string]string{
		"sourceFile": "会员能力.csv",
	})
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if got := parsed.Blocks[0].Provenance.SourceFile; got != "会员能力.csv" {
		t.Fatalf("expected csv source file provenance, got %q", got)
	}
}

func TestCSVParserStripsBOMAndPadsShortRows(t *testing.T) {
	data := append([]byte{0xEF, 0xBB, 0xBF}, []byte("能力,说明\n积分查询\n")...)
	parsed, err := (&CSVParser{}).Parse(context.Background(), data, "text/csv; charset=utf-8", nil)
	if err != nil {
		t.Fatalf("parse csv with bom: %v", err)
	}
	if got := parsed.Blocks[0].Headers[0]; got != "能力" {
		t.Fatalf("expected BOM to be stripped, got %q", got)
	}
	if got := parsed.Blocks[0].Rows[0]; len(got) != 2 || got[0] != "积分查询" || got[1] != "" {
		t.Fatalf("expected short row to be padded, got %+v", got)
	}
}

func TestCSVParserDecodesGBK(t *testing.T) {
	data, _, err := transform.Bytes(simplifiedchinese.GBK.NewEncoder(), []byte("能力,说明\n积分查询,支持\n"))
	if err != nil {
		t.Fatalf("encode gbk fixture: %v", err)
	}
	parsed, err := (&CSVParser{}).Parse(context.Background(), data, "text/comma-separated-values; charset=gbk", nil)
	if err != nil {
		t.Fatalf("parse gbk csv: %v", err)
	}
	if got := parsed.Blocks[0].Rows[0][0]; got != "积分查询" {
		t.Fatalf("expected decoded gbk text, got %q", got)
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

func TestXLSXParserCarriesSourceFileAndSheetProvenance(t *testing.T) {
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

	parsed, err := (&XLSXParser{}).Parse(context.Background(), data, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", map[string]string{
		"sourceFile": "会员能力.xlsx",
	})
	if err != nil {
		t.Fatalf("parse xlsx: %v", err)
	}
	provenance := parsed.Blocks[0].Provenance
	if provenance.SourceFile != "会员能力.xlsx" || provenance.SheetName != "sheet1" {
		t.Fatalf("expected xlsx provenance, got %+v", provenance)
	}
}

func TestXLSXParserCarriesWorkbookSheetNameProvenance(t *testing.T) {
	data := zipBytes(t, map[string]string{
		"xl/workbook.xml": `<?xml version="1.0" encoding="UTF-8"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <sheets><sheet name="权益表" sheetId="1" r:id="rId1"/></sheets>
</workbook>`,
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

	parsed, err := (&XLSXParser{}).Parse(context.Background(), data, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", map[string]string{
		"sourceFile": "会员能力.xlsx",
	})
	if err != nil {
		t.Fatalf("parse xlsx: %v", err)
	}
	if got := parsed.Blocks[0].Provenance.SheetName; got != "权益表" {
		t.Fatalf("expected workbook sheet name provenance, got %q", got)
	}
}

func TestXLSXParserUsesWorkbookRelationshipForFirstSheet(t *testing.T) {
	data := zipBytes(t, map[string]string{
		"xl/workbook.xml": `<?xml version="1.0" encoding="UTF-8"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <sheets>
    <sheet name="权益表" sheetId="7" r:id="rId2"/>
    <sheet name="旧表" sheetId="1" r:id="rId1"/>
  </sheets>
</workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/>
</Relationships>`,
		"xl/sharedStrings.xml": `<?xml version="1.0" encoding="UTF-8"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <si><t>能力</t></si><si><t>说明</t></si><si><t>旧数据</t></si><si><t>不应读取</t></si><si><t>权益查询</t></si><si><t>查看等级</t></si>
</sst>`,
		"xl/worksheets/sheet1.xml": `<?xml version="1.0" encoding="UTF-8"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <sheetData>
    <row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>
    <row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2" t="s"><v>3</v></c></row>
  </sheetData>
</worksheet>`,
		"xl/worksheets/sheet2.xml": `<?xml version="1.0" encoding="UTF-8"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <sheetData>
    <row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>
    <row r="2"><c r="A2" t="s"><v>4</v></c><c r="B2" t="s"><v>5</v></c></row>
  </sheetData>
</worksheet>`,
	})

	parsed, err := (&XLSXParser{}).Parse(context.Background(), data, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", map[string]string{
		"sourceFile": "会员能力.xlsx",
	})
	if err != nil {
		t.Fatalf("parse xlsx: %v", err)
	}
	block := parsed.Blocks[0]
	if block.Provenance.SheetName != "权益表" {
		t.Fatalf("expected first workbook sheet name, got %+v", block.Provenance)
	}
	if got := block.Rows[0][0]; got != "权益查询" {
		t.Fatalf("expected first workbook relationship sheet data, got %q", got)
	}
}

func TestXLSXParserPreservesFormulaResultsAndHyperlinks(t *testing.T) {
	data := zipBytes(t, map[string]string{
		"xl/sharedStrings.xml": `<?xml version="1.0" encoding="UTF-8"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <si><t>能力</t></si><si><t>说明</t></si><si><t>积分查询</t></si><si><t>打开会员中心</t></si>
</sst>`,
		"xl/worksheets/sheet1.xml": `<?xml version="1.0" encoding="UTF-8"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <sheetData>
    <row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>
    <row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2"><f>2+2</f><v>4</v></c></row>
    <row r="3"><c r="A3" t="s"><v>3</v></c><c r="B3" t="s"><v>3</v></c></row>
  </sheetData>
  <hyperlinks>
    <hyperlink ref="B3" r:id="rId1"/>
  </hyperlinks>
</worksheet>`,
		"xl/worksheets/_rels/sheet1.xml.rels": `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="https://example.com/member" TargetMode="External"/>
</Relationships>`,
	})

	parsed, err := (&XLSXParser{}).Parse(context.Background(), data, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", nil)
	if err != nil {
		t.Fatalf("parse xlsx: %v", err)
	}
	text := rag.RenderBlocks(parsed.Blocks)
	if !strings.Contains(text, "4") {
		t.Fatalf("expected cached formula result, got %q", text)
	}
	if !strings.Contains(text, "[打开会员中心](https://example.com/member)") {
		t.Fatalf("expected hyperlink markdown, got %q", text)
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

func TestHTMLParserExtractsVisibleText(t *testing.T) {
	htmlData := `<!doctype html><html><head><title>会员中心</title><style>.x{}</style></head><body><h1>权益总览</h1><p>支持积分查询</p><script>ignore()</script><table><tr><th>能力</th><th>说明</th></tr><tr><td>会员查询</td><td>实时</td></tr></table></body></html>`
	parsed, err := (&HTMLParser{}).Parse(context.Background(), []byte(htmlData), "text/html", nil)
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}
	text := rag.RenderBlocks(parsed.Blocks)
	for _, want := range []string{"会员中心", "权益总览", "支持积分查询", "能力 | 说明", "会员查询 | 实时"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected html text %q, got %q", want, text)
		}
	}
}

func TestXMLParserExtractsVisibleText(t *testing.T) {
	xmlData := `<?xml version="1.0" encoding="UTF-8"?><root><title>会员中心</title><section><p>支持积分查询</p><item>实时等级</item></section></root>`
	parsed, err := (&XMLParser{}).Parse(context.Background(), []byte(xmlData), "application/xml", nil)
	if err != nil {
		t.Fatalf("parse xml: %v", err)
	}
	text := rag.RenderBlocks(parsed.Blocks)
	for _, want := range []string{"会员中心", "支持积分查询", "实时等级"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected xml text %q, got %q", want, text)
		}
	}
}

func TestPPTXParserExtractsSlideText(t *testing.T) {
	data := zipBytes(t, map[string]string{
		"ppt/slides/slide1.xml": `<?xml version="1.0" encoding="UTF-8"?>
<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld>
    <p:spTree>
      <p:sp><p:txBody><a:p><a:r><a:t>会员权益</a:t></a:r></a:p><a:p><a:r><a:t>积分查询</a:t></a:r></a:p></p:txBody></p:sp>
    </p:spTree>
  </p:cSld>
</p:sld>`,
		"ppt/slides/slide2.xml": `<?xml version="1.0" encoding="UTF-8"?>
<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld>
    <p:spTree>
      <p:sp><p:txBody><a:p><a:r><a:t>会员等级</a:t></a:r></a:p></p:txBody></p:sp>
    </p:spTree>
  </p:cSld>
</p:sld>`,
	})
	parsed, err := (&PPTXParser{}).Parse(context.Background(), data, "application/vnd.openxmlformats-officedocument.presentationml.presentation", nil)
	if err != nil {
		t.Fatalf("parse pptx: %v", err)
	}
	text := rag.RenderBlocks(parsed.Blocks)
	for _, want := range []string{"会员权益", "积分查询", "会员等级"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected pptx text %q, got %q", want, text)
		}
	}
}

func TestDefaultRegistryRegistersConcreteParsers(t *testing.T) {
	reg := DefaultRegistry()
	for _, mime := range []string{
		"text/csv; charset=utf-8",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"text/html; charset=utf-8",
		"application/xml; charset=utf-8",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation",
	} {
		if !reg.Supports(mime) {
			t.Fatalf("default registry should support %s", mime)
		}
	}
}

func TestImageParserParsesImageWithDescriptionAndAsset(t *testing.T) {
	vlmSvc := &fakeVLMService{desc: "这是一张会员能力说明图片"}
	uploader := &fakeUploader{url: "https://assets.example.com/image.png"}
	p := NewImageParser(vlmSvc, uploader, "", 0)

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

func TestImageParserPassesMaxOutputTokensToVLM(t *testing.T) {
	vlmSvc := &fakeVLMService{desc: "这是一张会员能力说明图片"}
	p := NewImageParser(vlmSvc, nil, "自定义提示", 2048)

	_, err := p.Parse(context.Background(), []byte("fake-image"), "image/png", map[string]string{
		"sourceFile": "会员能力.png",
		"documentId": "doc-1",
	})
	if err != nil {
		t.Fatalf("parse image: %v", err)
	}
	if vlmSvc.maxTokens != 2048 {
		t.Fatalf("expected max tokens to be forwarded, got %d", vlmSvc.maxTokens)
	}
}

func TestImageParserRasterizesSVGToPNG(t *testing.T) {
	vlmSvc := &fakeVLMService{desc: "这是一个 SVG 图标"}
	uploader := &fakeUploader{url: "https://assets.example.com/icon.png"}
	p := NewImageParser(vlmSvc, uploader, "", 0)

	parsed, err := p.Parse(context.Background(), []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="20" height="20"><rect width="20" height="20" fill="red"/></svg>`), "image/svg+xml", map[string]string{
		"sourceFile": "icon.svg",
		"documentId": "doc-svg",
	})
	if err != nil {
		t.Fatalf("parse svg: %v", err)
	}
	if vlmSvc.mimeType != "image/png" {
		t.Fatalf("expected rasterized mime type image/png, got %q", vlmSvc.mimeType)
	}
	if uploader.contentType != "image/png" {
		t.Fatalf("expected uploaded content type image/png, got %q", uploader.contentType)
	}
	if _, err := png.Decode(bytes.NewReader(uploader.data)); err != nil {
		t.Fatalf("expected PNG output, got decode error: %v", err)
	}
	if parsed.Metadata["mimeType"] != "image/png" {
		t.Fatalf("expected parsed mimeType image/png, got %+v", parsed.Metadata)
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
	desc      string
	calls     int
	maxTokens int
	mimeType  string
}

func (f *fakeVLMService) DescribeImage(ctx context.Context, image []byte, mimeType, prompt string, maxOutputTokens ...int) (string, error) {
	f.calls++
	f.mimeType = mimeType
	if len(maxOutputTokens) > 0 {
		f.maxTokens = maxOutputTokens[0]
	}
	return f.desc, nil
}

type fakeUploader struct {
	url         string
	calls       int
	contentType string
	data        []byte
}

func (f *fakeUploader) Upload(ctx context.Context, key string, data []byte, contentType string) (string, error) {
	f.calls++
	f.contentType = contentType
	f.data = append([]byte(nil), data...)
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
