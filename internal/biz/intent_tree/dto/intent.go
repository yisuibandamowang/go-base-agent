package dto

import (
	"encoding/json"
	"fmt"
	"time"
)

// EnabledValue 兼容 Java Boolean 与 Go 数值形态的启用标记。
type EnabledValue int16

// UnmarshalJSON 支持 enabled 传入布尔值或数字。
func (e *EnabledValue) UnmarshalJSON(data []byte) error {
	switch string(data) {
	case "null":
		*e = 0
		return nil
	case "true":
		*e = 1
		return nil
	case "false":
		*e = 0
		return nil
	}
	var value int16
	if err := json.Unmarshal(data, &value); err == nil {
		*e = EnabledValue(value)
		return nil
	}
	return fmt.Errorf("enabled must be boolean or integer")
}

// IntentExamples 兼容 Java 侧 examples 数组与 Go 侧历史字符串形态。
type IntentExamples string

// UnmarshalJSON 支持 examples 传入字符串或字符串数组。
func (e *IntentExamples) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*e = ""
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*e = IntentExamples(text)
		return nil
	}
	var examples []string
	if err := json.Unmarshal(data, &examples); err == nil {
		raw, err := json.Marshal(examples)
		if err != nil {
			return fmt.Errorf("marshal examples: %w", err)
		}
		*e = IntentExamples(raw)
		return nil
	}
	return fmt.Errorf("examples must be string or string array")
}

// IntentNodeResp 意图节点响应。
type IntentNodeResp struct {
	ID                  string            `json:"id"`
	KbID                string            `json:"kbId"`
	IntentCode          string            `json:"intentCode"`
	Name                string            `json:"name"`
	Level               int16             `json:"level"`
	ParentCode          string            `json:"parentCode"`
	Description         string            `json:"description"`
	Examples            string            `json:"examples"`
	CollectionName      string            `json:"collectionName"`
	TopK                int               `json:"topK"`
	McpToolID           string            `json:"mcpToolId"`
	Kind                int16             `json:"kind"`
	PromptSnippet       string            `json:"promptSnippet"`
	PromptTemplate      string            `json:"promptTemplate"`
	ParamPromptTemplate string            `json:"paramPromptTemplate"`
	SortOrder           int               `json:"sortOrder"`
	Enabled             int16             `json:"enabled"`
	CreateTime          time.Time         `json:"createTime"`
	UpdateTime          time.Time         `json:"updateTime"`
	Children            []*IntentNodeResp `json:"children,omitempty"`
}

// CreateIntentReq 创建意图节点请求。
type CreateIntentReq struct {
	KbID                string         `json:"kbId"`
	IntentCode          string         `json:"intentCode" binding:"required"`
	Name                string         `json:"name" binding:"required"`
	Level               int16          `json:"level"`
	ParentCode          string         `json:"parentCode"`
	Description         string         `json:"description"`
	Examples            IntentExamples `json:"examples"`
	CollectionName      string         `json:"collectionName"`
	TopK                int            `json:"topK"`
	McpToolID           string         `json:"mcpToolId"`
	Kind                int16          `json:"kind"`
	PromptSnippet       string         `json:"promptSnippet"`
	PromptTemplate      string         `json:"promptTemplate"`
	ParamPromptTemplate string         `json:"paramPromptTemplate"`
	SortOrder           int            `json:"sortOrder"`
	Enabled             int16          `json:"enabled"`
	TopKSet             bool           `json:"-"`
	EnabledSet          bool           `json:"-"`
}

// UnmarshalJSON 记录 topK 是否由请求显式传入。
func (r *CreateIntentReq) UnmarshalJSON(data []byte) error {
	var raw struct {
		KbID                string         `json:"kbId"`
		IntentCode          string         `json:"intentCode"`
		Name                string         `json:"name"`
		Level               int16          `json:"level"`
		ParentCode          string         `json:"parentCode"`
		Description         string         `json:"description"`
		Examples            IntentExamples `json:"examples"`
		CollectionName      string         `json:"collectionName"`
		TopK                *int           `json:"topK"`
		McpToolID           string         `json:"mcpToolId"`
		Kind                int16          `json:"kind"`
		PromptSnippet       string         `json:"promptSnippet"`
		PromptTemplate      string         `json:"promptTemplate"`
		ParamPromptTemplate string         `json:"paramPromptTemplate"`
		SortOrder           int            `json:"sortOrder"`
		Enabled             int16          `json:"enabled"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.KbID = raw.KbID
	r.IntentCode = raw.IntentCode
	r.Name = raw.Name
	r.Level = raw.Level
	r.ParentCode = raw.ParentCode
	r.Description = raw.Description
	r.Examples = raw.Examples
	r.CollectionName = raw.CollectionName
	if raw.TopK != nil {
		r.TopK = *raw.TopK
		r.TopKSet = true
	}
	r.McpToolID = raw.McpToolID
	r.Kind = raw.Kind
	r.PromptSnippet = raw.PromptSnippet
	r.PromptTemplate = raw.PromptTemplate
	r.ParamPromptTemplate = raw.ParamPromptTemplate
	r.SortOrder = raw.SortOrder
	r.Enabled = raw.Enabled
	if string(data) != "" {
		var enabledProbe struct {
			Enabled *int16 `json:"enabled"`
		}
		if err := json.Unmarshal(data, &enabledProbe); err == nil && enabledProbe.Enabled != nil {
			r.EnabledSet = true
		}
	}
	return nil
}

// UpdateIntentReq 更新意图节点请求。
type UpdateIntentReq struct {
	KbID                *string         `json:"kbId"`
	IntentCode          *string         `json:"intentCode"`
	Name                *string         `json:"name"`
	Level               *int16          `json:"level"`
	ParentCode          *string         `json:"parentCode"`
	Description         *string         `json:"description"`
	Examples            *IntentExamples `json:"examples"`
	CollectionName      *string         `json:"collectionName"`
	TopK                *int            `json:"topK"`
	McpToolID           *string         `json:"mcpToolId"`
	Kind                *int16          `json:"kind"`
	PromptSnippet       *string         `json:"promptSnippet"`
	PromptTemplate      *string         `json:"promptTemplate"`
	ParamPromptTemplate *string         `json:"paramPromptTemplate"`
	SortOrder           *int            `json:"sortOrder"`
	Enabled             *int16          `json:"enabled"`
}

// TermMappingResp 关键词映射响应。
type TermMappingResp struct {
	ID         string    `json:"id"`
	Domain     string    `json:"domain"`
	SourceTerm string    `json:"sourceTerm"`
	TargetTerm string    `json:"targetTerm"`
	MatchType  int16     `json:"matchType"`
	Priority   int       `json:"priority"`
	Enabled    bool      `json:"enabled"`
	Remark     string    `json:"remark"`
	CreateTime time.Time `json:"createTime"`
}

// CreateTermMappingReq 创建关键词映射请求。
type CreateTermMappingReq struct {
	Domain     string       `json:"domain"`
	SourceTerm string       `json:"sourceTerm" binding:"required"`
	TargetTerm string       `json:"targetTerm" binding:"required"`
	MatchType  int16        `json:"matchType"`
	Priority   int          `json:"priority"`
	Enabled    EnabledValue `json:"enabled"`
	Remark     string       `json:"remark"`
	EnabledSet bool         `json:"-"`
}

// UnmarshalJSON 记录 enabled 是否由请求显式传入。
func (r *CreateTermMappingReq) UnmarshalJSON(data []byte) error {
	var raw struct {
		Domain     string        `json:"domain"`
		SourceTerm string        `json:"sourceTerm"`
		TargetTerm string        `json:"targetTerm"`
		MatchType  int16         `json:"matchType"`
		Priority   int           `json:"priority"`
		Enabled    *EnabledValue `json:"enabled"`
		Remark     string        `json:"remark"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.Domain = raw.Domain
	r.SourceTerm = raw.SourceTerm
	r.TargetTerm = raw.TargetTerm
	r.MatchType = raw.MatchType
	r.Priority = raw.Priority
	r.Remark = raw.Remark
	if raw.Enabled != nil {
		r.Enabled = *raw.Enabled
		r.EnabledSet = true
	}
	return nil
}

// UpdateTermMappingReq 更新关键词映射请求。
type UpdateTermMappingReq struct {
	Domain     *string       `json:"domain"`
	SourceTerm *string       `json:"sourceTerm"`
	TargetTerm *string       `json:"targetTerm"`
	MatchType  *int16        `json:"matchType"`
	Priority   *int          `json:"priority"`
	Enabled    *EnabledValue `json:"enabled"`
	Remark     *string       `json:"remark"`
}
