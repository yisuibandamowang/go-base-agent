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
	// EditorHint 编辑器提示语（对齐 Java AgentPromptSlot.editorHint）：
	// 告诉提示词作者这个槽位的产物去哪、怎么写才对，仅部分槽位需要。
	EditorHint string
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
			Key:         "KNOWLEDGE_TOOL_DESCRIPTION",
			DisplayName: "知识库工具声明",
			Group:       "AGENT",
			GroupName:   "Agent 专属",
			EffectiveModes: map[string]struct{}{
				ModeAgent: {},
			},
			InactiveReason: "WorkFlow 模式不注册原生知识库工具",
			EditorHint:     "模型靠它判断要不要查知识库，此时还看不到检索结果；写清这个库覆盖哪类问题，不必在这里规定回答风格",
		},
		{
			Key:         "AGENT_CONTEXT_COMPACTION",
			DisplayName: "Agent上下文压缩",
			Group:       "AGENT",
			GroupName:   "Agent 专属",
			EffectiveModes: map[string]struct{}{
				ModeAgent: {},
			},
			InactiveReason:       "WorkFlow 模式不做上下文压缩，长会话走「历史对话摘要」",
			RequiredPlaceholders: []string{"{summary_max_chars}"},
			EditorHint:           "产物会以历史消息的身份回填进后续每一轮，而被它替代的原文届时已经删除；要求写清调用过哪些工具、得到什么结论、还剩什么没做，不必在这里规定回答风格",
		},
		{
			Key:         "AGENT_MEMORY_EXTRACTION",
			DisplayName: "长期记忆抽取",
			Group:       "AGENT",
			GroupName:   "Agent 专属",
			EffectiveModes: map[string]struct{}{
				ModeAgent: {},
			},
			InactiveReason:       "WorkFlow 模式不沉淀跨会话事实",
			RequiredPlaceholders: []string{"{existing_memories}", "{recent_turns}", "{memory_max_chars}"},
			EditorHint:           "产物不进对话，由代码按 JSON 数组解析后直接写库，解析失败整批作废；写清什么该记、什么不该记、以及怎么指认已有条目，不要在这里规定回答风格",
		},
		{
			Key:         "AGENT_MEMORY_CONSOLIDATION",
			DisplayName: "长期记忆受限合并",
			Group:       "AGENT",
			GroupName:   "Agent 专属",
			EffectiveModes: map[string]struct{}{
				ModeAgent: {},
			},
			InactiveReason:       "WorkFlow 模式不沉淀跨会话事实",
			RequiredPlaceholders: []string{"{existing_memories}", "{target_chars}"},
			EditorHint:           "仅当记忆总量顶到上限时才调用一次；产物不进对话，由代码按 JSON 数组解析后整组替换旧条目；写清什么算同一件事，以及绝不许为了压体量删掉一条独立记忆",
		},
		{
			Key:         "AGENT_MEMORY_TOOL_DESCRIPTION",
			DisplayName: "记忆整理工具声明",
			Group:       "AGENT",
			GroupName:   "Agent 专属",
			EffectiveModes: map[string]struct{}{
				ModeAgent: {},
			},
			InactiveReason: "WorkFlow 模式不注册记忆整理工具",
			EditorHint:     "模型靠它判断这轮要不要整理记忆，此时还看不到整理结果；「记住」和「忘掉」两类场景都要写到，具体记什么忘什么由抽取环节判断",
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
			// 与「Agent 上下文压缩」不可合并：这份是话题索引不含结论，那份必须留结论
			Key:         "CONVERSATION_SUMMARY",
			DisplayName: "历史对话摘要",
			Group:       "WORKFLOW",
			GroupName:   "WorkFlow 专属",
			EffectiveModes: map[string]struct{}{
				ModeWorkflow: {},
			},
			InactiveReason:       "Agent 模式的长会话改由「Agent 上下文压缩」承接",
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
