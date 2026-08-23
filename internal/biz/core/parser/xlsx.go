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

// XLSXParser 解析 XLSX 文件中所有可见工作表为表格块。
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
	headerRows, err := parseXLSXHeaderRows(options)
	if err != nil {
		return nil, err
	}
	sheetRefs, err := readXLSXSheetRefs(zr)
	if err != nil {
		return nil, err
	}
	blocks := make([]rag.Block, 0, len(sheetRefs))
	for _, sheetRef := range sheetRefs {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
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
		sheetData, err := readXLSXSheet(sheet, sharedStrings, hyperlinks)
		sheet.Close()
		if err != nil {
			return nil, fmt.Errorf("parse xlsx sheet %s: %w", sheetRef.Name, err)
		}
		records := normalizeXLSXSheet(sheetData, headerRows)
		if len(records) == 0 {
			continue
		}
		provenance := rag.Provenance{SourceFile: parseOption(options, "sourceFile"), SheetName: sheetRef.Name}
		blocks = append(blocks, tableBlockFromRecords(records, provenance))
	}
	if len(blocks) == 0 {
		return nil, fmt.Errorf("parse xlsx: empty content")
	}
	return &rag.ParsedDocument{
		Blocks:   blocks,
		Metadata: map[string]string{"mime": mimeType, "method": "xlsx"},
	}, nil
}

type xlsxSheetRef struct {
	Name string
	Path string
}

type xlsxSheetData struct {
	Records      [][]string
	MergedRanges []xlsxCellRange
}

type xlsxCellRange struct {
	FirstRow int
	FirstCol int
	LastRow  int
	LastCol  int
}

func defaultXLSXSheetRef() xlsxSheetRef {
	return xlsxSheetRef{Name: "sheet1", Path: "xl/worksheets/sheet1.xml"}
}

func parseXLSXHeaderRows(options map[string]string) (int, error) {
	value := parseOption(options, "headerRows")
	if value == "" {
		return 1, nil
	}
	headerRows, err := strconv.Atoi(value)
	if err != nil {
		return 1, nil
	}
	if headerRows < 1 {
		return 0, fmt.Errorf("parse xlsx: headerRows must be >= 1")
	}
	return headerRows, nil
}

func readXLSXSheetRefs(zr *zip.Reader) ([]xlsxSheetRef, error) {
	rc, err := openZipFile(zr, "xl/workbook.xml")
	if err != nil {
		return []xlsxSheetRef{defaultXLSXSheetRef()}, nil
	}
	defer rc.Close()

	relationships, err := readXLSXWorkbookRelationships(zr)
	if err != nil {
		return nil, err
	}

	refs := make([]xlsxSheetRef, 0)
	decoder := xml.NewDecoder(rc)
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse xlsx workbook.xml: %w", err)
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
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		return []xlsxSheetRef{defaultXLSXSheetRef()}, nil
	}
	return refs, nil
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

func readXLSXSheet(r io.Reader, sharedStrings []string, hyperlinks map[string]string) (xlsxSheetData, error) {
	decoder := xml.NewDecoder(r)
	cells := make(map[int]map[int]string)
	mergedRanges := make([]xlsxCellRange, 0)
	currentRowIndex := -1
	nextRowIndex := 0
	currentCellRow := -1
	currentCellCol := -1
	nextCellCol := 0
	maxRow := -1
	maxCol := -1
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
			return xlsxSheetData{}, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				currentRowIndex = nextRowIndex
				for _, attr := range t.Attr {
					if attr.Name.Local != "r" {
						continue
					}
					rowNumber, err := strconv.Atoi(strings.TrimSpace(attr.Value))
					if err == nil && rowNumber > 0 {
						currentRowIndex = rowNumber - 1
					}
				}
				nextRowIndex = currentRowIndex + 1
				nextCellCol = 0
			case "c":
				inCell = true
				currentCellType = ""
				currentCellRef = ""
				currentCellRow = currentRowIndex
				currentCellCol = nextCellCol
				currentFormula.Reset()
				currentValue.Reset()
				for _, attr := range t.Attr {
					switch attr.Name.Local {
					case "t":
						currentCellType = attr.Value
					case "r":
						currentCellRef = attr.Value
						if row, col, ok := parseXLSXCellPosition(attr.Value); ok {
							currentCellRow = row
							currentCellCol = col
						}
					}
				}
				nextCellCol = currentCellCol + 1
			case "f":
				if inCell {
					inFormula = true
					currentFormula.Reset()
				}
			case "v", "t":
				if inCell {
					inValue = true
				}
			case "mergeCell":
				for _, attr := range t.Attr {
					if attr.Name.Local != "ref" {
						continue
					}
					mergedRange, ok := parseXLSXCellRange(attr.Value)
					if !ok {
						continue
					}
					mergedRanges = append(mergedRanges, mergedRange)
					maxRow = max(maxRow, mergedRange.LastRow)
					maxCol = max(maxCol, mergedRange.LastCol)
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
				if currentCellRow >= 0 && currentCellCol >= 0 {
					if cells[currentCellRow] == nil {
						cells[currentCellRow] = make(map[int]string)
					}
					cells[currentCellRow][currentCellCol] = value
					maxRow = max(maxRow, currentCellRow)
					maxCol = max(maxCol, currentCellCol)
				}
				currentValue.Reset()
				currentFormula.Reset()
				inCell = false
			}
		}
	}
	if maxRow < 0 || maxCol < 0 {
		return xlsxSheetData{MergedRanges: mergedRanges}, nil
	}
	records := make([][]string, maxRow+1)
	for row := range records {
		records[row] = make([]string, maxCol+1)
		for col, value := range cells[row] {
			records[row][col] = value
		}
	}
	return xlsxSheetData{Records: records, MergedRanges: mergedRanges}, nil
}

func parseXLSXCellPosition(ref string) (int, int, bool) {
	ref = strings.ReplaceAll(strings.TrimSpace(ref), "$", "")
	index := 0
	column := 0
	for index < len(ref) {
		char := ref[index]
		if char >= 'a' && char <= 'z' {
			char -= 'a' - 'A'
		}
		if char < 'A' || char > 'Z' {
			break
		}
		column = column*26 + int(char-'A'+1)
		index++
	}
	if index == 0 || index == len(ref) {
		return 0, 0, false
	}
	row, err := strconv.Atoi(ref[index:])
	if err != nil || row < 1 {
		return 0, 0, false
	}
	return row - 1, column - 1, true
}

func parseXLSXCellRange(ref string) (xlsxCellRange, bool) {
	startRef, endRef, found := strings.Cut(strings.TrimSpace(ref), ":")
	if !found {
		endRef = startRef
	}
	firstRow, firstCol, ok := parseXLSXCellPosition(startRef)
	if !ok {
		return xlsxCellRange{}, false
	}
	lastRow, lastCol, ok := parseXLSXCellPosition(endRef)
	if !ok {
		return xlsxCellRange{}, false
	}
	return xlsxCellRange{
		FirstRow: min(firstRow, lastRow),
		FirstCol: min(firstCol, lastCol),
		LastRow:  max(firstRow, lastRow),
		LastCol:  max(firstCol, lastCol),
	}, true
}

func normalizeXLSXSheet(sheet xlsxSheetData, headerRows int) [][]string {
	if len(sheet.Records) == 0 {
		return nil
	}
	for _, mergedRange := range sheet.MergedRanges {
		value := sheet.Records[mergedRange.FirstRow][mergedRange.FirstCol]
		if value == "" {
			continue
		}
		for row := mergedRange.FirstRow; row <= mergedRange.LastRow; row++ {
			for col := mergedRange.FirstCol; col <= mergedRange.LastCol; col++ {
				sheet.Records[row][col] = value
			}
		}
	}

	columns := selectXLSXNonEmptyColumns(sheet.Records)
	if len(columns) == 0 {
		return nil
	}
	effectiveHeaderRows := min(headerRows, len(sheet.Records))
	headers := make([]string, len(columns))
	for index, col := range columns {
		parts := make([]string, 0, effectiveHeaderRows)
		previous := ""
		for row := 0; row < effectiveHeaderRows; row++ {
			value := sheet.Records[row][col]
			if value == "" || value == previous {
				continue
			}
			parts = append(parts, value)
			previous = value
		}
		headers[index] = strings.Join(parts, "|")
	}

	records := make([][]string, 0, len(sheet.Records)-effectiveHeaderRows+1)
	records = append(records, headers)
	for row := effectiveHeaderRows; row < len(sheet.Records); row++ {
		values := make([]string, len(columns))
		nonEmpty := false
		for index, col := range columns {
			values[index] = sheet.Records[row][col]
			if values[index] != "" {
				nonEmpty = true
			}
		}
		if nonEmpty {
			records = append(records, values)
		}
	}
	return records
}

func selectXLSXNonEmptyColumns(records [][]string) []int {
	columns := make([]int, 0, len(records[0]))
	for col := range len(records[0]) {
		for row := range records {
			if records[row][col] != "" {
				columns = append(columns, col)
				break
			}
		}
	}
	return columns
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
