package parser

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"

	"go-base-agent/internal/biz/rag"
)

// XLSXParser 解析 XLSX 文件的首个工作表为表格块。
type XLSXParser struct{}

func (p *XLSXParser) Type() rag.ParserType { return rag.ParserExcelPOI }

func (p *XLSXParser) Supports(mimeType string) bool {
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
	sheet, err := openZipFile(zr, "xl/worksheets/sheet1.xml")
	if err != nil {
		return nil, fmt.Errorf("open xlsx sheet1.xml: %w", err)
	}
	defer sheet.Close()
	records, err := readXLSXSheet(sheet, sharedStrings)
	if err != nil {
		return nil, fmt.Errorf("parse xlsx sheet: %w", err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("parse xlsx: empty content")
	}
	return &rag.ParsedDocument{
		Blocks:   []rag.Block{tableBlockFromRecords(records)},
		Metadata: map[string]string{"mime": mimeType, "method": "xlsx"},
	}, nil
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

func readXLSXSheet(r io.Reader, sharedStrings []string) ([][]string, error) {
	decoder := xml.NewDecoder(r)
	var records [][]string
	var currentRow []string
	var currentCellType string
	var currentValue strings.Builder
	inCell := false
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
				for _, attr := range t.Attr {
					if attr.Name.Local == "t" {
						currentCellType = attr.Value
						break
					}
				}
			case "v", "t":
				if inCell {
					inValue = true
					currentValue.Reset()
				}
			}
		case xml.CharData:
			if inValue {
				currentValue.Write([]byte(t))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v", "t":
				inValue = false
			case "c":
				currentRow = append(currentRow, resolveXLSXCellValue(currentValue.String(), currentCellType, sharedStrings))
				currentValue.Reset()
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

func resolveXLSXCellValue(value, cellType string, sharedStrings []string) string {
	value = strings.TrimSpace(value)
	if cellType != "s" {
		return value
	}
	idx, err := strconv.Atoi(value)
	if err != nil || idx < 0 || idx >= len(sharedStrings) {
		return value
	}
	return sharedStrings[idx]
}

func openZipFile(zr *zip.Reader, name string) (io.ReadCloser, error) {
	for _, f := range zr.File {
		if f.Name == name {
			return f.Open()
		}
	}
	return nil, fmt.Errorf("%s not found", name)
}
