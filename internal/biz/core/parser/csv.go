package parser

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"

	"go-base-agent/internal/biz/rag"
)

// CSVParser 解析 CSV 文件为表格块。
type CSVParser struct{}

func (p *CSVParser) Type() rag.ParserType { return rag.ParserCSV }

func (p *CSVParser) Supports(mimeType string) bool {
	return mimeType == "text/csv" || mimeType == "application/csv"
}

func (p *CSVParser) Parse(ctx context.Context, data []byte, mimeType string, options map[string]string) (*rag.ParsedDocument, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("parse csv: %w", err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("parse csv: empty content")
	}
	block := tableBlockFromRecords(records)
	return &rag.ParsedDocument{
		Blocks:   []rag.Block{block},
		Metadata: map[string]string{"mime": mimeType, "method": "csv"},
	}, nil
}

func tableBlockFromRecords(records [][]string) rag.Block {
	block := rag.Block{Type: rag.BlockTable}
	if len(records) > 0 {
		block.Headers = records[0]
	}
	if len(records) > 1 {
		block.Rows = records[1:]
	}
	return block
}
