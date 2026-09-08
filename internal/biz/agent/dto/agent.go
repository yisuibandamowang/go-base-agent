package dto

import "time"

// CreateAgentProfileReq 创建智能体请求。
type CreateAgentProfileReq struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
	Avatar      string `json:"avatar"`
}

// UpdateAgentProfileReq 更新智能体请求。
type UpdateAgentProfileReq struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Avatar      *string `json:"avatar"`
}

// SaveAgentPromptReq 保存提示词请求。
type SaveAgentPromptReq struct {
	Content string `json:"content"`
}

// AgentProfileListResp 智能体列表响应。
type AgentProfileListResp struct {
	Mode               string             `json:"mode"`
	EffectiveSlotTotal int                `json:"effectiveSlotTotal"`
	Agents             []AgentProfileResp `json:"agents"`
}

// AgentProfileResp 智能体响应。
type AgentProfileResp struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	Avatar         string    `json:"avatar"`
	Builtin        bool      `json:"builtin"`
	Active         bool      `json:"active"`
	EffectiveSlots int       `json:"effectiveSlots"`
	InactiveSlots  int       `json:"inactiveSlots"`
	CreateTime     time.Time `json:"createTime"`
	UpdateTime     time.Time `json:"updateTime"`
}

// AgentPromptConfigResp 某智能体的槽位配置。
type AgentPromptConfigResp struct {
	AgentID          string                `json:"agentId"`
	AgentName        string                `json:"agentName"`
	Builtin          bool                  `json:"builtin"`
	DefaultAgentName string                `json:"defaultAgentName"`
	Mode             string                `json:"mode"`
	Slots            []AgentPromptSlotResp `json:"slots"`
}

// AgentPromptSlotResp 槽位响应。
type AgentPromptSlotResp struct {
	SlotKey              string   `json:"slotKey"`
	DisplayName          string   `json:"displayName"`
	Group                string   `json:"group"`
	GroupName            string   `json:"groupName"`
	Effective            bool     `json:"effective"`
	InactiveReason       string   `json:"inactiveReason"`
	EditorHint           string   `json:"editorHint"`
	RequiredPlaceholders []string `json:"requiredPlaceholders"`
	Content              string   `json:"content"`
}
