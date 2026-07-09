package dto

import "time"

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
	KbID                string `json:"kbId"`
	IntentCode          string `json:"intentCode" binding:"required"`
	Name                string `json:"name" binding:"required"`
	Level               int16  `json:"level"`
	ParentCode          string `json:"parentCode"`
	Description         string `json:"description"`
	Examples            string `json:"examples"`
	CollectionName      string `json:"collectionName"`
	TopK                int    `json:"topK"`
	McpToolID           string `json:"mcpToolId"`
	Kind                int16  `json:"kind"`
	PromptSnippet       string `json:"promptSnippet"`
	PromptTemplate      string `json:"promptTemplate"`
	ParamPromptTemplate string `json:"paramPromptTemplate"`
	SortOrder           int    `json:"sortOrder"`
	Enabled             int16  `json:"enabled"`
}

// UpdateIntentReq 更新意图节点请求。
type UpdateIntentReq struct {
	KbID                *string `json:"kbId"`
	IntentCode          *string `json:"intentCode"`
	Name                *string `json:"name"`
	Level               *int16  `json:"level"`
	ParentCode          *string `json:"parentCode"`
	Description         *string `json:"description"`
	Examples            *string `json:"examples"`
	CollectionName      *string `json:"collectionName"`
	TopK                *int    `json:"topK"`
	McpToolID           *string `json:"mcpToolId"`
	Kind                *int16  `json:"kind"`
	PromptSnippet       *string `json:"promptSnippet"`
	PromptTemplate      *string `json:"promptTemplate"`
	ParamPromptTemplate *string `json:"paramPromptTemplate"`
	SortOrder           *int    `json:"sortOrder"`
	Enabled             *int16  `json:"enabled"`
}

// TermMappingResp 关键词映射响应。
type TermMappingResp struct {
	ID         string    `json:"id"`
	Domain     string    `json:"domain"`
	SourceTerm string    `json:"sourceTerm"`
	TargetTerm string    `json:"targetTerm"`
	MatchType  int16     `json:"matchType"`
	Priority   int       `json:"priority"`
	Enabled    int16     `json:"enabled"`
	Remark     string    `json:"remark"`
	CreateTime time.Time `json:"createTime"`
}

// CreateTermMappingReq 创建关键词映射请求。
type CreateTermMappingReq struct {
	Domain     string `json:"domain"`
	SourceTerm string `json:"sourceTerm" binding:"required"`
	TargetTerm string `json:"targetTerm" binding:"required"`
	MatchType  int16  `json:"matchType"`
	Priority   int    `json:"priority"`
	Enabled    int16  `json:"enabled"`
	Remark     string `json:"remark"`
}

// UpdateTermMappingReq 更新关键词映射请求。
type UpdateTermMappingReq struct {
	Domain     *string `json:"domain"`
	SourceTerm *string `json:"sourceTerm"`
	TargetTerm *string `json:"targetTerm"`
	MatchType  *int16  `json:"matchType"`
	Priority   *int    `json:"priority"`
	Enabled    *int16  `json:"enabled"`
	Remark     *string `json:"remark"`
}
