package service

import "strings"

const (
	// ModeWorkflow 对应 Workflow 编排模式。
	ModeWorkflow = "WORKFLOW"
	// ModeAgent 对应 Agent 编排模式。
	ModeAgent = "AGENT"
)

// PromptSlot 智能体提示词槽位元数据。
type PromptSlot struct {
	Key                  string
	DisplayName          string
	Group                string
	GroupName            string
	EffectiveModes       map[string]struct{}
	InactiveReason       string
	RequiredPlaceholders []string
}

// EffectiveIn 返回槽位在指定模式下是否生效。
func (s PromptSlot) EffectiveIn(mode string) bool {
	_, ok := s.EffectiveModes[strings.ToUpper(strings.TrimSpace(mode))]
	return ok
}

// AllPromptSlots 返回全部槽位元数据。
func AllPromptSlots() []PromptSlot {
	return []PromptSlot{
		{
			Key:         "SYSTEM_CHAT",
			DisplayName: "闲聊 / 关于助手",
			Group:       "WORKFLOW",
			GroupName:   "WorkFlow 专属",
			EffectiveModes: map[string]struct{}{
				ModeWorkflow: {},
			},
			InactiveReason: "Agent 模式下由主 Agent 直接应答",
		},
		{
			Key:         "MCP_ANSWER",
			DisplayName: "MCP 问答",
			Group:       "WORKFLOW",
			GroupName:   "WorkFlow 专属",
			EffectiveModes: map[string]struct{}{
				ModeWorkflow: {},
			},
			InactiveReason: "Agent 模式下改用原生工具调用，无独立的数据合成环节",
		},
		{
			Key:         "MIXED_ANSWER",
			DisplayName: "混合问答",
			Group:       "WORKFLOW",
			GroupName:   "WorkFlow 专属",
			EffectiveModes: map[string]struct{}{
				ModeWorkflow: {},
			},
			InactiveReason: "Agent 模式下由主 Agent 综合多个工具的结果",
		},
		{
			Key:         "AGENT_MAIN",
			DisplayName: "Agent 人设",
			Group:       "AGENT",
			GroupName:   "Agent 专属",
			EffectiveModes: map[string]struct{}{
				ModeAgent: {},
			},
			InactiveReason: "WorkFlow 模式不经过 ReAct 架构",
		},
		{
			Key:         "KB_ANSWER",
			DisplayName: "知识库问答",
			Group:       "COMMON",
			GroupName:   "通用",
			EffectiveModes: map[string]struct{}{
				ModeWorkflow: {},
				ModeAgent:    {},
			},
		},
		{
			Key:         "CONVERSATION_SUMMARY",
			DisplayName: "会话压缩",
			Group:       "COMMON",
			GroupName:   "通用",
			EffectiveModes: map[string]struct{}{
				ModeWorkflow: {},
				ModeAgent:    {},
			},
			RequiredPlaceholders: []string{"{summary_max_chars}"},
		},
		{
			Key:         "RECOMMENDED_QUESTIONS",
			DisplayName: "推荐问题",
			Group:       "COMMON",
			GroupName:   "通用",
			EffectiveModes: map[string]struct{}{
				ModeWorkflow: {},
				ModeAgent:    {},
			},
			RequiredPlaceholders: []string{"{chunks}", "{count}", "{question}", "{answer}"},
		},
	}
}

// EffectivePromptSlots 返回当前模式下生效的槽位。
func EffectivePromptSlots(mode string) []PromptSlot {
	all := AllPromptSlots()
	slots := make([]PromptSlot, 0, len(all))
	for _, slot := range all {
		if slot.EffectiveIn(mode) {
			slots = append(slots, slot)
		}
	}
	return slots
}

// FindPromptSlot 按 key 查找槽位。
func FindPromptSlot(key string) (PromptSlot, bool) {
	trimmed := strings.ToUpper(strings.TrimSpace(key))
	for _, slot := range AllPromptSlots() {
		if slot.Key == trimmed {
			return slot, true
		}
	}
	return PromptSlot{}, false
}
