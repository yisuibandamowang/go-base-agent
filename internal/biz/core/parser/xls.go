package parser

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"go-base-agent/internal/biz/rag"

	legacyxls "github.com/shakinm/xlsReader/xls"
)

// XLSParser 解析旧版 BIFF XLS 文件中的工作表为表格块。
type XLSParser struct{}

// Type 返回解析器类型。
func (p *XLSParser) Type() rag.ParserType { return rag.ParserExcelPOI }

// Supports 判断 MIME 类型是否为旧版 XLS。
func (p *XLSParser) Supports(mimeType string) bool {
	return normalizeMIMEType(mimeType) == "application/vnd.ms-excel"
}

// Parse 解析 XLS 文件中所有工作表，并复用 XLSX 的表格规范化规则。
func (p *XLSParser) Parse(ctx context.Context, data []byte, mimeType string, options map[string]string) (*rag.ParsedDocument, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("open xls: empty content")
	}
	headerRows, err := parseXLSXHeaderRows(options)
	if err != nil {
		return nil, err
	}
	workbook, err := legacyxls.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("open xls workbook: %w", err)
	}
	blocks := make([]rag.Block, 0, workbook.GetNumberSheets())
	for index := 0; index < workbook.GetNumberSheets(); index++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		sheet, err := workbook.GetSheet(index)
		if err != nil {
			return nil, fmt.Errorf("open xls sheet %d: %w", index, err)
		}
		records, err := readXLSSheet(sheet)
		if err != nil {
			return nil, fmt.Errorf("parse xls sheet %d: %w", index, err)
		}
		records = normalizeXLSXSheet(xlsxSheetData{Records: records}, headerRows)
		if len(records) == 0 {
			continue
		}
		provenance := rag.Provenance{SourceFile: parseOption(options, "sourceFile"), SheetName: sheet.GetName()}
		blocks = append(blocks, tableBlockFromRecords(records, provenance))
	}
	if len(blocks) == 0 {
		return nil, fmt.Errorf("parse xls: empty content")
	}
	return &rag.ParsedDocument{
		Blocks: blocks,
		Metadata: map[string]string{
			"mime":   mimeType,
			"method": "xls",
		},
	}, nil
}

func readXLSSheet(sheet *legacyxls.Sheet) ([][]string, error) {
	if sheet == nil {
		return nil, fmt.Errorf("sheet is nil")
	}
	rowCount := sheet.GetNumberRows()
	if rowCount == 0 {
		return nil, nil
	}
	records := make([][]string, rowCount)
	maxCol := 0
	for rowIndex := 0; rowIndex < rowCount; rowIndex++ {
		row, err := sheet.GetRow(rowIndex)
		if err != nil {
			return nil, fmt.Errorf("read row %d: %w", rowIndex, err)
		}
		cols := row.GetCols()
		records[rowIndex] = make([]string, len(cols))
		for colIndex, cell := range cols {
			records[rowIndex][colIndex] = strings.TrimSpace(cell.GetString())
		}
		if len(cols) > maxCol {
			maxCol = len(cols)
		}
	}
	for rowIndex := range records {
		for len(records[rowIndex]) < maxCol {
			records[rowIndex] = append(records[rowIndex], "")
		}
	}
	return records, nil
}
