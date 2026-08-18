package model

import "go-base-agent/internal/framework/db"

// AgentProfile 对应 t_agent_profile 表。
type AgentProfile struct {
	db.BaseModel

	Name        string `gorm:"column:name;type:varchar(64);not null;uniqueIndex:uk_agent_name" json:"name"`
	Description string `gorm:"column:description;type:varchar(512)" json:"description"`
	Avatar      string `gorm:"column:avatar;type:varchar(32)" json:"avatar"`
	Builtin     int16  `gorm:"column:builtin;type:smallint;not null;default:0" json:"builtin"`
	Active      int16  `gorm:"column:active;type:smallint;not null;default:0;index:idx_agent_active" json:"active"`
}

// TableName 返回数据库表名。
func (AgentProfile) TableName() string {
	return "t_agent_profile"
}

// AgentPrompt 对应 t_agent_prompt 表。
type AgentPrompt struct {
	db.BaseModel

	AgentID string `gorm:"column:agent_id;type:varchar(20);not null;uniqueIndex:uk_agent_slot" json:"agentId"`
	SlotKey string `gorm:"column:slot_key;type:varchar(64);not null;uniqueIndex:uk_agent_slot" json:"slotKey"`
	Content string `gorm:"column:content;type:text" json:"content"`
}

// TableName 返回数据库表名。
func (AgentPrompt) TableName() string {
	return "t_agent_prompt"
}
