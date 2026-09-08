package parser

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

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
	styles := readXLSXStyles(zr)
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
		sheetData, err := readXLSXSheet(sheet, sharedStrings, hyperlinks, styles)
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

func readXLSXSheet(r io.Reader, sharedStrings []string, hyperlinks map[string]string, styles *xlsxStyles) (xlsxSheetData, error) {
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
	var currentCellStyle int
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
				currentCellStyle = -1
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
					case "s":
						if idx, err := strconv.Atoi(strings.TrimSpace(attr.Value)); err == nil && idx >= 0 {
							currentCellStyle = idx
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
				value = applyXLSXCellStyle(value, currentCellType, currentCellStyle, styles)
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

// xlsxXF cellXfs 中一条样式记录：数字格式 id 与字体（删除线）标记。
type xlsxXF struct {
	numFmtID int
	strike   bool
}

// xlsxStyles 从 xl/styles.xml 解析出的单元格样式表：
// cell 的 s 属性索引 cellXfs，xf 的 numFmtId 决定数字/日期渲染，fontId 关联字体删除线。
type xlsxStyles struct {
	customNumFmts map[int]string
	xfs           []xlsxXF
}

// readXLSXStyles 解析 xl/styles.xml；文件缺失或结构异常时返回 nil，样式渲染整体降级为原样值。
func readXLSXStyles(zr *zip.Reader) *xlsxStyles {
	rc, err := openZipFile(zr, "xl/styles.xml")
	if err != nil {
		return nil
	}
	defer rc.Close()

	decoder := xml.NewDecoder(rc)
	styles := &xlsxStyles{customNumFmts: make(map[int]string)}
	fonts := make([]bool, 0, 4)
	section := ""
	fontStrike := false
	inFont := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil
		}
		switch t := token.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "numFmts":
				section = "numFmts"
			case "fonts":
				section = "fonts"
			case "cellXfs":
				section = "cellXfs"
			case "numFmt":
				var id int
				var code string
				for _, attr := range t.Attr {
					switch attr.Name.Local {
					case "numFmtId":
						id, _ = strconv.Atoi(strings.TrimSpace(attr.Value))
					case "formatCode":
						code = attr.Value
					}
				}
				if id != 0 && strings.TrimSpace(code) != "" {
					styles.customNumFmts[id] = code
				}
			case "font":
				if section == "fonts" {
					inFont = true
					fontStrike = false
				}
			case "strike":
				if inFont {
					fontStrike = true
				}
			case "xf":
				if section != "cellXfs" {
					continue
				}
				xf := xlsxXF{}
				for _, attr := range t.Attr {
					switch attr.Name.Local {
					case "numFmtId":
						xf.numFmtID, _ = strconv.Atoi(strings.TrimSpace(attr.Value))
					case "fontId":
						fontID, _ := strconv.Atoi(strings.TrimSpace(attr.Value))
						if fontID >= 0 && fontID < len(fonts) {
							xf.strike = fonts[fontID]
						}
					}
				}
				styles.xfs = append(styles.xfs, xf)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "font":
				if inFont {
					fonts = append(fonts, fontStrike)
					inFont = false
				}
			case "numFmts", "fonts", "cellXfs":
				section = ""
			}
		}
	}
	return styles
}

// applyXLSXCellStyle 按单元格样式渲染值（对齐 Java ExcelValueFormatter + ExcelTableNormalizer）：
// 数字 cell 按数字格式码渲染（日期序列转日期文本、百分比、千分位、固定位小数）；
// 字体带删除线的非空 cell 用 GFM 删除线 ~~值~~ 包裹原值（软删除约定：保留文本并显式标注）。
func applyXLSXCellStyle(value, cellType string, styleIdx int, styles *xlsxStyles) string {
	if styles == nil || styleIdx < 0 || styleIdx >= len(styles.xfs) {
		return value
	}
	xf := styles.xfs[styleIdx]
	if (cellType == "" || cellType == "n") && value != "" {
		if rendered, ok := formatXLSXNumber(value, xf.numFmtID, styles.customNumFmts); ok {
			value = rendered
		}
	}
	if value != "" && xf.strike {
		value = "~~" + value + "~~"
	}
	return value
}

// builtinXLSXNumFmts 内置数字格式码（截取 POI BuiltinFormats 常用子集）。
var builtinXLSXNumFmts = map[int]string{
	0: "General", 1: "0", 2: "0.00", 3: "#,##0", 4: "#,##0.00",
	9: "0%", 10: "0.00%", 11: "0.00E+00",
	14: "m/d/yy", 15: "d-mmm-yy", 16: "d-mmm", 17: "mmm-yy",
	18: "h:mm AM/PM", 19: "h:mm:ss AM/PM", 20: "h:mm", 21: "h:mm:ss", 22: "m/d/yy h:mm",
	37: "#,##0 ;(#,##0)", 38: "#,##0 ;[Red](#,##0)", 39: "#,##0.00;(#,##0.00)", 40: "#,##0.00;[Red](#,##0.00)",
	45: "mm:ss", 46: "[h]:mm:ss", 47: "mmss.0", 48: "##0.0E+0", 49: "@",
}

// formatXLSXNumber 按 (内置或自定义) 数字格式码渲染数字 cell；识别不了的格式返回 false 由调用方保留原值。
func formatXLSXNumber(raw string, numFmtID int, customNumFmts map[int]string) (string, bool) {
	code, ok := customNumFmts[numFmtID]
	if !ok {
		code, ok = builtinXLSXNumFmts[numFmtID]
	}
	if !ok {
		return "", false
	}
	code = strings.TrimSpace(code)
	if code == "" || strings.EqualFold(code, "General") || code == "@" {
		return "", false
	}
	number, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return "", false
	}
	if isXLSXDateFormat(numFmtID, code) {
		moment := excelSerialToTime(number)
		if layout, ok := xlsxDateLayout(code); ok {
			return moment.Format(layout), true
		}
		return moment.Format("2006-01-02 15:04:05"), true
	}
	if strings.Contains(code, "%") {
		return strconv.FormatFloat(number*100, 'f', xlsxDecimalDigits(code), 64) + "%", true
	}
	decimals := xlsxDecimalDigits(code)
	rendered := strconv.FormatFloat(number, 'f', decimals, 64)
	if strings.Contains(code, ",") {
		rendered = groupXLSXThousands(rendered, decimals)
	}
	return rendered, true
}

// xlsxColorCodePattern Excel 格式码中的颜色段（[Red] 等），判定日期格式时需剥除。
var xlsxColorCodePattern = regexp.MustCompile(`\[(?:RED|GREEN|BLUE|BLACK|WHITE|MAGENTA|CYAN|YELLOW)\]`)

// isXLSXDateFormat 判断格式码是否为日期/时间格式。
func isXLSXDateFormat(numFmtID int, code string) bool {
	switch {
	case numFmtID >= 14 && numFmtID <= 22, numFmtID >= 45 && numFmtID <= 47:
		return true
	}
	// 剥颜色码（[Red] 等），避免颜色名里的字母被误认成日期 token
	code = xlsxColorCodePattern.ReplaceAllString(strings.ToUpper(code), "")
	if !strings.ContainsAny(code, "YMDHS") {
		return false
	}
	// 数字占位与日期 token 混用（如 "0.0E+00"）不是日期；y/d 与 h:s 组合才是
	return strings.ContainsAny(code, "YD") || strings.ContainsAny(code, "HS")
}

// excelSerialToTime 把 Excel 日期序列值转为时间（基准 1899-12-30，含 1900 假闰日补偿）。
func excelSerialToTime(serial float64) time.Time {
	days := int(serial)
	frac := serial - float64(days)
	moment := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC).AddDate(0, 0, days)
	return moment.Add(time.Duration(frac * 86400 * float64(time.Second)))
}

// xlsxDateLayout 把 Excel 日期格式码翻译成 Go time 布局（有限支持：y/m/d/h/s 连串 token、
// AM/PM、引号字面量）。m 跟在小时后按分钟处理，否则按月份。
func xlsxDateLayout(code string) (string, bool) {
	hasMeridiem := strings.Contains(strings.ToUpper(code), "AM/PM")
	var out strings.Builder
	sawHour := false
	runLen := func(from int, char byte) int {
		n := 0
		for i := from; i < len(code); i++ {
			if code[i] != char && code[i] != char+'A'-'a' {
				break
			}
			n++
		}
		return n
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		switch c {
		case '"':
			end := i + 1
			for end < len(code) && code[end] != '"' {
				end++
			}
			if end < len(code) {
				out.WriteString(code[i+1 : end])
				i = end
			}
		case '\\':
			if i+1 < len(code) {
				out.WriteByte(code[i+1])
				i++
			}
		case 'y', 'Y':
			n := runLen(i, 'y')
			if n >= 3 {
				out.WriteString("2006")
			} else {
				out.WriteString("06")
			}
			i += n - 1
		case 'm', 'M':
			n := runLen(i, 'm')
			if sawHour {
				if n >= 2 {
					out.WriteString("04")
				} else {
					out.WriteString("4")
				}
			} else if n >= 3 {
				out.WriteString("Jan")
			} else if n == 2 {
				out.WriteString("01")
			} else {
				out.WriteString("1")
			}
			i += n - 1
		case 'd', 'D':
			n := runLen(i, 'd')
			switch {
			case n >= 4:
				out.WriteString("Monday")
			case n == 3:
				out.WriteString("Mon")
			case n == 2:
				out.WriteString("02")
			default:
				out.WriteString("2")
			}
			i += n - 1
		case 'h', 'H':
			n := runLen(i, 'h')
			sawHour = true
			if hasMeridiem {
				if n >= 2 {
					out.WriteString("03")
				} else {
					out.WriteString("3")
				}
			} else {
				out.WriteString("15")
			}
			i += n - 1
		case 's', 'S':
			n := runLen(i, 's')
			if n >= 2 {
				out.WriteString("05")
			} else {
				out.WriteString("5")
			}
			i += n - 1
		case 'A', 'a':
			if strings.HasPrefix(strings.ToUpper(code[i:]), "AM/PM") {
				out.WriteString("PM")
				i += 4
			}
		default:
			out.WriteByte(c)
		}
	}
	layout := out.String()
	if !strings.ContainsAny(layout, "1234560") {
		return "", false
	}
	return layout, true
}

// xlsxDecimalDigits 取格式码中小数位数（"0.00" → 2；"#,##0" → 0）。
func xlsxDecimalDigits(code string) int {
	idx := strings.LastIndexAny(strings.ToUpper(code), ".")
	if idx < 0 {
		return 0
	}
	digits := 0
	for i := idx + 1; i < len(code); i++ {
		if code[i] == '0' || code[i] == '#' {
			digits++
			continue
		}
		break
	}
	return digits
}

// groupXLSXThousands 给整数部分加千分位分组。
func groupXLSXThousands(value string, decimals int) string {
	intPart := value
	if decimals > 0 && len(value) > decimals+1 {
		intPart = value[:len(value)-decimals-1]
	} else {
		decimals = 0
	}
	negative := strings.HasPrefix(intPart, "-")
	if negative {
		intPart = intPart[1:]
	}
	grouped := make([]byte, 0, len(intPart)+len(intPart)/3)
	for i, digit := range []byte(intPart) {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			grouped = append(grouped, ',')
		}
		grouped = append(grouped, digit)
	}
	result := string(grouped)
	if negative {
		result = "-" + result
	}
	if decimals > 0 {
		result += "." + value[len(value)-decimals:]
	}
	return result
}
