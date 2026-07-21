package parser

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"go-base-agent/internal/biz/rag"
)

// XLSXParser 解析 XLSX 文件的首个工作表为表格块。
type XLSXParser struct{}

func (p *XLSXParser) Type() rag.ParserType { return rag.ParserExcelPOI }

func (p *XLSXParser) Supports(mimeType string) bool {
	mimeType = normalizeMIMEType(mimeType)
	return mimeType == "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" ||
		strings.Contains(mimeType, "spreadsheetml")
}

func (p *XLSXParser) Parse(ctx context.Context, data []byte, mimeType string, options map[string]string) (*rag.ParsedDocument, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open xlsx zip: %w", err)
	}
	sharedStrings, err := readXLSXSharedStrings(zr)
	if err != nil {
		return nil, err
	}
	sheetRef, err := readXLSXFirstSheetRef(zr)
	if err != nil {
		return nil, err
	}
	hyperlinkTargets, err := readXLSXHyperlinkTargets(zr, sheetRef.Path)
	if err != nil {
		return nil, err
	}
	hyperlinks, err := collectXLSXSheetHyperlinks(zr, sheetRef.Path, hyperlinkTargets)
	if err != nil {
		return nil, err
	}
	sheet, err := openZipFile(zr, sheetRef.Path)
	if err != nil {
		return nil, fmt.Errorf("open xlsx sheet %s: %w", sheetRef.Path, err)
	}
	defer sheet.Close()
	records, err := readXLSXSheet(sheet, sharedStrings, hyperlinks)
	if err != nil {
		return nil, fmt.Errorf("parse xlsx sheet: %w", err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("parse xlsx: empty content")
	}
	provenance := rag.Provenance{SourceFile: parseOption(options, "sourceFile"), SheetName: sheetRef.Name}
	return &rag.ParsedDocument{
		Blocks:   []rag.Block{tableBlockFromRecords(records, provenance)},
		Metadata: map[string]string{"mime": mimeType, "method": "xlsx"},
	}, nil
}

type xlsxSheetRef struct {
	Name string
	Path string
}

func defaultXLSXSheetRef() xlsxSheetRef {
	return xlsxSheetRef{Name: "sheet1", Path: "xl/worksheets/sheet1.xml"}
}

func readXLSXFirstSheetRef(zr *zip.Reader) (xlsxSheetRef, error) {
	rc, err := openZipFile(zr, "xl/workbook.xml")
	if err != nil {
		return defaultXLSXSheetRef(), nil
	}
	defer rc.Close()

	relationships, err := readXLSXWorkbookRelationships(zr)
	if err != nil {
		return xlsxSheetRef{}, err
	}

	decoder := xml.NewDecoder(rc)
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return xlsxSheetRef{}, fmt.Errorf("parse xlsx workbook.xml: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "sheet" {
			continue
		}
		ref := defaultXLSXSheetRef()
		var relID string
		hidden := false
		for _, attr := range start.Attr {
			switch attr.Name.Local {
			case "name":
				if strings.TrimSpace(attr.Value) != "" {
					ref.Name = strings.TrimSpace(attr.Value)
				}
			case "id":
				relID = strings.TrimSpace(attr.Value)
			case "state":
				state := strings.ToLower(strings.TrimSpace(attr.Value))
				hidden = state == "hidden" || state == "veryhidden"
			}
		}
		if hidden {
			continue
		}
		if target := relationships[relID]; target != "" {
			ref.Path = normalizeXLSXWorkbookTarget(target)
		}
		return ref, nil
	}
	return defaultXLSXSheetRef(), nil
}

func readXLSXWorkbookRelationships(zr *zip.Reader) (map[string]string, error) {
	rc, err := openZipFile(zr, "xl/_rels/workbook.xml.rels")
	if err != nil {
		return nil, nil
	}
	defer rc.Close()

	decoder := xml.NewDecoder(rc)
	relationships := make(map[string]string)
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse xlsx workbook rels: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "Relationship" {
			continue
		}
		var id, target, typ string
		for _, attr := range start.Attr {
			switch attr.Name.Local {
			case "Id":
				id = attr.Value
			case "Target":
				target = strings.TrimSpace(attr.Value)
			case "Type":
				typ = attr.Value
			}
		}
		if id == "" || target == "" || !strings.Contains(typ, "/worksheet") {
			continue
		}
		relationships[id] = target
	}
	return relationships, nil
}

func normalizeXLSXWorkbookTarget(target string) string {
	target = strings.TrimSpace(target)
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(target, "/")
	}
	if strings.HasPrefix(target, "xl/") {
		return path.Clean(target)
	}
	return path.Clean(path.Join("xl", target))
}

func readXLSXHyperlinkTargets(zr *zip.Reader, sheetPath string) (map[string]string, error) {
	rc, err := openZipFile(zr, xlsxSheetRelsPath(sheetPath))
	if err != nil {
		return nil, nil
	}
	defer rc.Close()

	decoder := xml.NewDecoder(rc)
	hyperlinks := make(map[string]string)
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse xlsx sheet rels: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "Relationship" {
			continue
		}
		var id, target, typ string
		for _, attr := range start.Attr {
			switch attr.Name.Local {
			case "Id":
				id = attr.Value
			case "Target":
				target = strings.TrimSpace(attr.Value)
			case "Type":
				typ = attr.Value
			}
		}
		if id == "" || target == "" || !strings.Contains(typ, "/hyperlink") {
			continue
		}
		hyperlinks[id] = target
	}
	return hyperlinks, nil
}

func collectXLSXSheetHyperlinks(zr *zip.Reader, sheetPath string, hyperlinkTargets map[string]string) (map[string]string, error) {
	rc, err := openZipFile(zr, sheetPath)
	if err != nil {
		return nil, fmt.Errorf("open xlsx sheet %s for hyperlinks: %w", sheetPath, err)
	}
	defer rc.Close()

	decoder := xml.NewDecoder(rc)
	hyperlinks := make(map[string]string)
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse xlsx sheet hyperlinks: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "hyperlink" {
			continue
		}
		var ref, relID string
		for _, attr := range start.Attr {
			switch attr.Name.Local {
			case "ref":
				ref = strings.TrimSpace(attr.Value)
			case "id":
				relID = strings.TrimSpace(attr.Value)
			}
		}
		if ref == "" || relID == "" {
			continue
		}
		if url := hyperlinkTargets[relID]; url != "" {
			hyperlinks[ref] = url
		}
	}
	return hyperlinks, nil
}

func xlsxSheetRelsPath(sheetPath string) string {
	return path.Join(path.Dir(sheetPath), "_rels", path.Base(sheetPath)+".rels")
}

func readXLSXSharedStrings(zr *zip.Reader) ([]string, error) {
	rc, err := openZipFile(zr, "xl/sharedStrings.xml")
	if err != nil {
		return nil, nil
	}
	defer rc.Close()

	decoder := xml.NewDecoder(rc)
	var values []string
	var current strings.Builder
	inSI := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse xlsx sharedStrings.xml: %w", err)
		}
		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Local == "si" {
				inSI = true
				current.Reset()
			}
		case xml.CharData:
			if inSI {
				current.Write([]byte(t))
			}
		case xml.EndElement:
			if t.Name.Local == "si" && inSI {
				values = append(values, current.String())
				inSI = false
			}
		}
	}
	return values, nil
}

func readXLSXSheet(r io.Reader, sharedStrings []string, hyperlinks map[string]string) ([][]string, error) {
	decoder := xml.NewDecoder(r)
	var records [][]string
	var currentRow []string
	var currentCellType string
	var currentCellRef string
	var currentFormula strings.Builder
	var currentValue strings.Builder
	inCell := false
	inFormula := false
	inValue := false

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				currentRow = nil
			case "c":
				inCell = true
				currentCellType = ""
				currentCellRef = ""
				currentFormula.Reset()
				currentValue.Reset()
				for _, attr := range t.Attr {
					switch attr.Name.Local {
					case "t":
						currentCellType = attr.Value
					case "r":
						currentCellRef = attr.Value
					}
				}
			case "f":
				if inCell {
					inFormula = true
					currentFormula.Reset()
				}
			case "v", "t":
				if inCell {
					inValue = true
				}
			}
		case xml.CharData:
			if inFormula {
				currentFormula.Write([]byte(t))
			}
			if inValue {
				currentValue.Write([]byte(t))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v", "t":
				inValue = false
			case "f":
				inFormula = false
			case "c":
				value := resolveXLSXCellValue(currentValue.String(), currentFormula.String(), currentCellType, sharedStrings)
				if hyperlink := hyperlinks[currentCellRef]; hyperlink != "" {
					value = wrapXLSXHyperlink(value, hyperlink)
				}
				currentRow = append(currentRow, value)
				currentValue.Reset()
				currentFormula.Reset()
				inCell = false
			case "row":
				if len(currentRow) > 0 {
					records = append(records, currentRow)
				}
			}
		}
	}
	return records, nil
}

func resolveXLSXCellValue(value, formula, cellType string, sharedStrings []string) string {
	value = strings.TrimSpace(value)
	formula = strings.TrimSpace(formula)
	if value == "" && formula != "" {
		return formula
	}
	if cellType != "s" {
		return value
	}
	idx, err := strconv.Atoi(value)
	if err != nil || idx < 0 || idx >= len(sharedStrings) {
		return value
	}
	return sharedStrings[idx]
}

func wrapXLSXHyperlink(cellText, url string) string {
	url = strings.TrimSpace(url)
	if url == "" {
		return cellText
	}
	visible := strings.TrimSpace(cellText)
	if visible == "" {
		visible = url
	}
	return "[" + visible + "](" + url + ")"
}

func openZipFile(zr *zip.Reader, name string) (io.ReadCloser, error) {
	for _, f := range zr.File {
		if f.Name == name {
			return f.Open()
		}
	}
	return nil, fmt.Errorf("%s not found", name)
}
