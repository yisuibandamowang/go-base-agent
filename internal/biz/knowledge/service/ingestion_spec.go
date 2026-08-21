package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"go-base-agent/internal/biz/rag"
)

const (
	defaultIngestionSpecVersion  = 2
	defaultIngestionMaxChars     = 1024
	defaultIngestionRowsPerChunk = 50
	defaultIngestionTolerance    = 3
	maxIngestionChars            = 8192
	maxIngestionRows             = 1000
	maxIngestionTolerance        = 8
	wholeDocumentSentinel        = -1
)

type ingestionSpecWire struct {
	Version      int                     `json:"version"`
	ParseProfile string                  `json:"parseProfile"`
	Budget       ingestionSpecBudgetWire `json:"budget"`
}

type ingestionSpecBudgetWire struct {
	MaxChars        int `json:"maxChars"`
	OverlapChars    int `json:"overlapChars"`
	RowsPerChunk    int `json:"rowsPerChunk"`
	ToleranceFactor int `json:"toleranceFactor"`
}

type ingestionSpecValues struct {
	profile         string
	maxChars        int
	overlapChars    int
	rowsPerChunk    int
	toleranceFactor int
}

// NormalizeIngestionSpec 校验并归一化 Java 兼容的文档级摄取配置。
func NormalizeIngestionSpec(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	values, err := parseIngestionSpec(raw)
	if err != nil {
		return "", err
	}
	return marshalIngestionSpec(values), nil
}

func parseIngestionSpec(raw string) (ingestionSpecValues, error) {
	var input map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &input); err != nil {
		return ingestionSpecValues{}, fmt.Errorf("摄取配置 JSON 格式不合法: %w", err)
	}
	values := ingestionSpecValues{
		profile:         "fast",
		maxChars:        defaultIngestionMaxChars,
		overlapChars:    rag.DefaultOverlapFor(defaultIngestionMaxChars),
		rowsPerChunk:    defaultIngestionRowsPerChunk,
		toleranceFactor: defaultIngestionTolerance,
	}
	if profile := strings.ToLower(strings.TrimSpace(stringValue(input["parseProfile"]))); profile != "" {
		if profile != "fast" && profile != "fidelity" {
			return ingestionSpecValues{}, fmt.Errorf("未知解析档位: %s", profile)
		}
		values.profile = profile
	}
	budget := input
	if nested, ok := input["budget"].(map[string]any); ok {
		budget = nested
	}
	maxCharsSet := false
	if number, ok := intValue(budget["maxChars"]); ok {
		if number == wholeDocumentSentinel || number > 0 {
			values.maxChars = number
			maxCharsSet = true
		}
	}
	overlapSet := false
	if number, ok := intValue(budget["overlapChars"]); ok {
		if number >= 0 {
			values.overlapChars = number
			overlapSet = true
		}
	}
	if maxCharsSet && !overlapSet && values.maxChars > 0 {
		values.overlapChars = rag.DefaultOverlapFor(values.maxChars)
	}
	if number, ok := intValue(budget["rowsPerChunk"]); ok {
		if number > 0 {
			values.rowsPerChunk = number
		}
	}
	if number, ok := intValue(budget["toleranceFactor"]); ok {
		if number > 0 {
			values.toleranceFactor = number
		}
	}
	if values.maxChars == wholeDocumentSentinel {
		values.overlapChars = 0
		values.rowsPerChunk = wholeDocumentSentinel
	} else {
		if values.maxChars <= 0 || values.maxChars > maxIngestionChars {
			return ingestionSpecValues{}, fmt.Errorf("maxChars 必须落在 [1, %d] 区间，实际 %d", maxIngestionChars, values.maxChars)
		}
		if values.overlapChars < 0 || values.overlapChars >= values.maxChars {
			return ingestionSpecValues{}, fmt.Errorf("overlapChars 必须落在 [0, maxChars) 区间，实际 %d", values.overlapChars)
		}
		if values.rowsPerChunk <= 0 || values.rowsPerChunk > maxIngestionRows {
			return ingestionSpecValues{}, fmt.Errorf("rowsPerChunk 必须落在 [1, %d] 区间，实际 %d", maxIngestionRows, values.rowsPerChunk)
		}
	}
	if values.toleranceFactor < 1 || values.toleranceFactor > maxIngestionTolerance {
		return ingestionSpecValues{}, fmt.Errorf("toleranceFactor 必须落在 [1, %d] 区间，实际 %d", maxIngestionTolerance, values.toleranceFactor)
	}
	return values, nil
}

func marshalIngestionSpec(values ingestionSpecValues) string {
	data, _ := json.Marshal(ingestionSpecWire{
		Version:      defaultIngestionSpecVersion,
		ParseProfile: values.profile,
		Budget: ingestionSpecBudgetWire{
			MaxChars:        values.maxChars,
			OverlapChars:    values.overlapChars,
			RowsPerChunk:    values.rowsPerChunk,
			ToleranceFactor: values.toleranceFactor,
		},
	})
	return string(data)
}

func chunkingOptionsForIngestionSpec(raw string) (rag.ChunkingOptions, string, error) {
	if strings.TrimSpace(raw) == "" {
		return rag.DefaultChunkingOptions(), "fast", nil
	}
	values, err := parseIngestionSpec(raw)
	if err != nil {
		return rag.ChunkingOptions{}, "", err
	}
	if values.maxChars == wholeDocumentSentinel {
		opts := rag.DefaultChunkingOptions()
		opts.ChunkSize = wholeDocumentSentinel
		opts.OverlapSize = 0
		opts.ToleranceSize = wholeDocumentSentinel
		return opts, values.profile, nil
	}
	opts := rag.DefaultChunkingOptions()
	opts.ChunkSize = values.maxChars
	opts.OverlapSize = values.overlapChars
	opts.RowsPerChunk = values.rowsPerChunk
	opts.ToleranceSize = values.maxChars * values.toleranceFactor
	if opts.ToleranceSize > maxIngestionChars {
		opts.ToleranceSize = maxIngestionChars
	}
	return opts, values.profile, nil
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func intValue(value any) (int, bool) {
	switch number := value.(type) {
	case float64:
		return int(number), number == float64(int(number))
	case int:
		return number, true
	case json.Number:
		parsed, err := number.Int64()
		return int(parsed), err == nil
	case string:
		var parsed int
		if _, err := fmt.Sscanf(strings.TrimSpace(number), "%d", &parsed); err == nil {
			return parsed, true
		}
	}
	return 0, false
}
