package parser

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"go-base-agent/internal/biz/rag"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// CSVParser 解析 CSV 文件为表格块。
type CSVParser struct{}

func (p *CSVParser) Type() rag.ParserType { return rag.ParserCSV }

func (p *CSVParser) Supports(mimeType string) bool {
	switch normalizeMIMEType(mimeType) {
	case "text/csv", "application/csv", "text/comma-separated-values":
		return true
	default:
		return false
	}
}

func (p *CSVParser) Parse(ctx context.Context, data []byte, mimeType string, options map[string]string) (*rag.ParsedDocument, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	text, err := decodeCSVText(data)
	if err != nil {
		return nil, fmt.Errorf("decode csv: %w", err)
	}
	reader := csv.NewReader(strings.NewReader(text))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("parse csv: %w", err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("parse csv: empty content")
	}
	records = normalizeCSVRecords(records)
	if len(records) == 0 {
		return nil, fmt.Errorf("parse csv: empty content")
	}
	block := tableBlockFromRecords(records, rag.Provenance{SourceFile: parseOption(options, "sourceFile")})
	return &rag.ParsedDocument{
		Blocks:   []rag.Block{block},
		Metadata: map[string]string{"mime": mimeType, "method": "csv"},
	}, nil
}

func decodeCSVText(data []byte) (string, error) {
	data = stripUTF8BOM(data)
	if utf8.Valid(data) {
		return string(data), nil
	}
	if text, ok := decodeUTF16CSV(data); ok {
		return text, nil
	}
	if text, _, err := transform.Bytes(simplifiedchinese.GBK.NewDecoder(), data); err == nil {
		return string(text), nil
	}
	return string(data), nil
}

func stripUTF8BOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return data[3:]
	}
	return data
}

func decodeUTF16CSV(data []byte) (string, bool) {
	if len(data) < 2 {
		return "", false
	}
	// UTF-16LE BOM
	if len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE {
		return decodeUTF16Units(data[2:], true), true
	}
	// UTF-16BE BOM
	if len(data) >= 2 && data[0] == 0xFE && data[1] == 0xFF {
		return decodeUTF16Units(data[2:], false), true
	}
	return "", false
}

func decodeUTF16Units(data []byte, littleEndian bool) string {
	if len(data)%2 != 0 {
		data = data[:len(data)-1]
	}
	units := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		if littleEndian {
			units = append(units, uint16(data[i])|uint16(data[i+1])<<8)
		} else {
			units = append(units, uint16(data[i+1])|uint16(data[i])<<8)
		}
	}
	return string(utf16.Decode(units))
}

func normalizeCSVRecords(records [][]string) [][]string {
	normalized := make([][]string, 0, len(records))
	for _, row := range records {
		cleaned := make([]string, 0, len(row))
		blank := true
		for _, cell := range row {
			cleaned = append(cleaned, cell)
			if strings.TrimSpace(cell) != "" {
				blank = false
			}
		}
		if blank {
			continue
		}
		normalized = append(normalized, cleaned)
	}
	if len(normalized) == 0 {
		return normalized
	}
	width := len(normalized[0])
	if width == 0 {
		return normalized
	}
	for i := 1; i < len(normalized); i++ {
		row := normalized[i]
		if len(row) >= width {
			continue
		}
		padded := make([]string, 0, width)
		padded = append(padded, row...)
		for len(padded) < width {
			padded = append(padded, "")
		}
		normalized[i] = padded
	}
	return normalized
}

func tableBlockFromRecords(records [][]string, provenance rag.Provenance) rag.Block {
	block := rag.Block{Type: rag.BlockTable, Provenance: provenance}
	if len(records) > 0 {
		block.Headers = records[0]
	}
	if len(records) > 1 {
		block.Rows = records[1:]
	}
	return block
}
