package model

import "go-base-agent/internal/framework/db"

// IntentNode 对应 t_intent_node 表（意图树节点）。
type IntentNode struct {
	db.BaseModel

	KbID                string `gorm:"column:kb_id;type:varchar(20)" json:"kbId"`
	IntentCode          string `gorm:"column:intent_code;type:varchar(64);not null" json:"intentCode"`
	Name                string `gorm:"column:name;type:varchar(64);not null" json:"name"`
	Level               int16  `gorm:"column:level;type:smallint;not null" json:"level"`
	ParentCode          string `gorm:"column:parent_code;type:varchar(64)" json:"parentCode"`
	Description         string `gorm:"column:description;type:varchar(512)" json:"description"`
	Examples            string `gorm:"column:examples;type:text" json:"examples"`
	CollectionName      string `gorm:"column:collection_name;type:varchar(128)" json:"collectionName"`
	TopK                int    `gorm:"column:top_k;type:integer" json:"topK"`
	McpToolID           string `gorm:"column:mcp_tool_id;type:varchar(128)" json:"mcpToolId"`
	Kind                int16  `gorm:"column:kind;type:smallint;not null;default:0" json:"kind"`
	PromptSnippet       string `gorm:"column:prompt_snippet;type:text" json:"promptSnippet"`
	PromptTemplate      string `gorm:"column:prompt_template;type:text" json:"promptTemplate"`
	ParamPromptTemplate string `gorm:"column:param_prompt_template;type:text" json:"paramPromptTemplate"`
	SortOrder           int    `gorm:"column:sort_order;type:integer;not null;default:0" json:"sortOrder"`
	Enabled             int16  `gorm:"column:enabled;type:smallint;not null;default:1" json:"enabled"`
	CreateBy            string `gorm:"column:create_by;type:varchar(20)" json:"createBy"`
	UpdateBy            string `gorm:"column:update_by;type:varchar(20)" json:"updateBy"`
}

func (IntentNode) TableName() string {
	return "t_intent_node"
}

// QueryTermMapping 对应 t_query_term_mapping 表（关键词归一化映射）。
type QueryTermMapping struct {
	db.BaseModel

	Domain     string `gorm:"column:domain;type:varchar(64);index:idx_domain" json:"domain"`
	SourceTerm string `gorm:"column:source_term;type:varchar(128);not null;index:idx_source" json:"sourceTerm"`
	TargetTerm string `gorm:"column:target_term;type:varchar(128);not null" json:"targetTerm"`
	MatchType  int16  `gorm:"column:match_type;type:smallint;not null;default:1" json:"matchType"`
	Priority   int    `gorm:"column:priority;type:integer;not null;default:100" json:"priority"`
	Enabled    int16  `gorm:"column:enabled;type:smallint;not null;default:1" json:"enabled"`
	Remark     string `gorm:"column:remark;type:varchar(255)" json:"remark"`
	CreateBy   string `gorm:"column:create_by;type:varchar(20)" json:"createBy"`
	UpdateBy   string `gorm:"column:update_by;type:varchar(20)" json:"updateBy"`
}

func (QueryTermMapping) TableName() string {
	return "t_query_term_mapping"
}
