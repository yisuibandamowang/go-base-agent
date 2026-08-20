package rag

import (
	"strconv"
	"strings"

	"go-base-agent/internal/infra/chat"
)

// PromptContext holds all inputs needed to build a chat prompt.
// Aligns with Java PromptContext (minimal subset for 2B-4).
type PromptContext struct {
	Question     string
	SubQuestions []string
	History      []chat.Message
	KbContext    string
	McpContext   string
	CodeContext  string
	// KbIntents KB 通道命中的意图候选（含分数）。
	KbIntents []NodeScore
	// EligibleIntentIds 允许参与模板选择的意图 ID：按库推导后真正有证据归属的意图。
	// 区分"知识定向检索未命中意图"与"全局回退"：定向检索命中但证据为空时该意图不参与模板选择。
	EligibleIntentIds map[string]struct{}
}

// PromptBuilder builds a chat.Request from a PromptContext.
type PromptBuilder interface {
	Build(ctx PromptContext) chat.Request
}

// RuntimePromptResolver resolves runtime prompt templates from mutable sources such as the agent tables.
type RuntimePromptResolver interface {
	Resolve(slotKey string) string
	Render(slotKey string, data any) (string, error)
}

// DefaultPromptBuilder constructs prompts using a template loader.
type DefaultPromptBuilder struct {
	loader     *PromptLoader
	systemFile string // e.g. "default_system.txt"
	resolver   RuntimePromptResolver
	engineMode string
	citationOn bool
}

const citationRulesFile = "answer_citation_rules.txt"

// NewDefaultPromptBuilder creates a builder using embedded prompt templates.
func NewDefaultPromptBuilder(resolver ...RuntimePromptResolver) *DefaultPromptBuilder {
	return NewPromptBuilder("", "default_system.txt", resolver...)
}

// NewPromptBuilder creates a builder with an optional external template directory.
// If externalDir is empty, embedded prompts are used.
func NewPromptBuilder(externalDir, systemFile string, resolver ...RuntimePromptResolver) *DefaultPromptBuilder {
	var promptResolver RuntimePromptResolver
	if len(resolver) > 0 {
		promptResolver = resolver[0]
	}
	return &DefaultPromptBuilder{
		loader:     NewPromptLoader(externalDir),
		systemFile: systemFile,
		resolver:   promptResolver,
	}
}

// SetEngineMode configures the current orchestration mode for prompt selection.
func (b *DefaultPromptBuilder) SetEngineMode(mode string) {
	if b == nil {
		return
	}
	b.engineMode = strings.ToUpper(strings.TrimSpace(mode))
}

// SetCitationEnabled 设置知识库回答的行内引用规则开关。
func (b *DefaultPromptBuilder) SetCitationEnabled(enabled bool) {
	if b != nil {
		b.citationOn = enabled
	}
}

// Build constructs a chat.Request from the prompt context.
func (b *DefaultPromptBuilder) Build(ctx PromptContext) chat.Request {
	messages := make([]chat.Message, 0, len(ctx.History)+2)

	sysPrompt := b.resolveSystemPrompt(ctx)
	sysPrompt = b.appendCitationRulesIfNeeded(ctx, sysPrompt)
	if sysPrompt != "" {
		messages = append(messages, chat.NewSystemMessage(sysPrompt))
	}

	messages = append(messages, ctx.History...)

	messages = append(messages, chat.NewUserMessage(buildPromptUserContent(ctx)))
	maxTokens := 1024
	return chat.Request{Messages: messages, MaxTokens: &maxTokens}
}

func (b *DefaultPromptBuilder) resolveSystemPrompt(ctx PromptContext) string {
	// KB 单意图且该意图有证据归属时，优先使用意图节点配置的提示词模板
	// 对齐 Java planPrompt：只有有证据支撑的意图才允许贡献模板
	if tpl := singleKbIntentPromptTemplate(ctx); tpl != "" {
		return tpl
	}
	if b != nil && b.resolver != nil {
		for _, slotKey := range systemPromptSlotCandidates(ctx, b.engineMode) {
			if prompt := strings.TrimSpace(b.resolver.Resolve(slotKey)); prompt != "" {
				return prompt
			}
		}
	}
	if b == nil || b.loader == nil {
		return "你是一个有帮助的AI助手。"
	}
	sysPrompt, err := b.loader.Render(b.systemFile, nil)
	if err != nil {
		return "你是一个有帮助的AI助手。"
	}
	return strings.TrimSpace(sysPrompt)
}

// singleKbIntentPromptTemplate 返回唯一有证据归属的 KB 意图的提示词模板。
// 多个意图都有归属或都没有归属时返回空串，走默认槽位解析。
func singleKbIntentPromptTemplate(ctx PromptContext) string {
	if len(ctx.EligibleIntentIds) == 0 || len(ctx.KbIntents) == 0 {
		return ""
	}
	var eligible []NodeScore
	seen := make(map[string]struct{}, len(ctx.KbIntents))
	for _, ns := range ctx.KbIntents {
		if ns.Node.Kind != IntentKindKB {
			continue
		}
		id := strings.TrimSpace(ns.Node.ID)
		if id == "" {
			continue
		}
		if _, ok := ctx.EligibleIntentIds[id]; !ok {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		eligible = append(eligible, ns)
	}
	if len(eligible) != 1 {
		return ""
	}
	return strings.TrimSpace(eligible[0].Node.PromptTemplate)
}

func systemPromptSlotCandidates(ctx PromptContext, mode string) []string {
	if strings.EqualFold(strings.TrimSpace(mode), "agent") {
		return []string{"AGENT_MAIN", "KB_ANSWER", "SYSTEM_CHAT"}
	}
	hasMcp := strings.TrimSpace(ctx.McpContext) != ""
	hasKb := strings.TrimSpace(ctx.KbContext) != ""
	hasCode := strings.TrimSpace(ctx.CodeContext) != ""
	switch {
	case hasMcp && (hasKb || hasCode):
		return []string{"MIXED_ANSWER", "SYSTEM_CHAT"}
	case hasMcp:
		return []string{"MCP_ANSWER", "SYSTEM_CHAT"}
	case hasKb || hasCode:
		return []string{"KB_ANSWER", "SYSTEM_CHAT"}
	default:
		return []string{"SYSTEM_CHAT"}
	}
}

func (b *DefaultPromptBuilder) appendCitationRulesIfNeeded(ctx PromptContext, sysPrompt string) string {
	if b == nil || !b.citationOn || strings.TrimSpace(ctx.KbContext) == "" {
		return sysPrompt
	}
	if b == nil || b.loader == nil {
		return sysPrompt
	}
	rules, err := b.loader.Render(citationRulesFile, nil)
	if err != nil {
		return sysPrompt
	}
	rules = strings.TrimSpace(rules)
	if rules == "" {
		return sysPrompt
	}
	if strings.TrimSpace(sysPrompt) == "" {
		return rules
	}
	return strings.TrimSpace(sysPrompt) + "\n\n" + rules
}

func buildPromptUserContent(ctx PromptContext) string {
	evidence := buildPromptEvidence(ctx.McpContext, ctx.KbContext, ctx.CodeContext)
	question := buildPromptQuestion(ctx.Question, ctx.SubQuestions)
	if evidence == "" {
		return ctx.Question
	}

	instruction := "只能依据以下知识库内容回答用户问题；如果知识库内容不足以回答，或只命中文档标题、目录或链接但没有正文细节，请直接说明知识库中没有相关信息，不要使用模型自身知识补充。"
	if strings.TrimSpace(ctx.McpContext) != "" && strings.TrimSpace(ctx.KbContext) != "" {
		instruction = "请结合以下MCP工具结果和知识库内容回答用户问题；如果工具结果与知识库内容冲突，请优先说明冲突并给出可追溯依据。"
	} else if strings.TrimSpace(ctx.McpContext) != "" {
		instruction = "请结合以下MCP工具结果回答用户问题；如果工具结果不足以回答，请直接说明工具结果中没有相关信息。"
	}
	if strings.TrimSpace(ctx.CodeContext) != "" {
		if strings.TrimSpace(ctx.McpContext) != "" || strings.TrimSpace(ctx.KbContext) != "" {
			instruction = "请结合以下代码仓库证据、MCP工具结果和知识库内容回答用户问题；如果证据之间冲突，请优先说明冲突并给出可追溯依据。"
		} else {
			instruction = "请结合以下代码仓库证据回答用户问题；如果代码仓库证据不足以回答，请直接说明缺少相关源码、调用链或表结构信息，不要使用模型自身知识补充。"
		}
	}

	if question == "" {
		return instruction + "\n\n" + evidence
	}
	return instruction + "\n\n" + evidence + "\n\n" + question
}

func buildPromptEvidence(mcpContext, kbContext, codeContext string) string {
	sections := make([]string, 0, 3)
	if mcp := strings.TrimSpace(mcpContext); mcp != "" {
		sections = append(sections, "<tool-data>\n"+mcp+"\n</tool-data>")
	}
	if kb := strings.TrimSpace(kbContext); kb != "" {
		sections = append(sections, "<documents>\n"+kb+"\n</documents>")
	}
	if code := strings.TrimSpace(codeContext); code != "" {
		sections = append(sections, "<code-documents>\n"+code+"\n</code-documents>")
	}
	return strings.Join(sections, "\n\n")
}

func buildPromptQuestion(question string, subQuestions []string) string {
	normalized := normalizePromptSubQuestions(subQuestions)
	if len(normalized) > 1 {
		numbered := make([]string, 0, len(normalized))
		for i, item := range normalized {
			numbered = append(numbered, strconv.Itoa(i+1)+". "+item)
		}
		return "<questions>\n" + strings.Join(numbered, "\n") + "\n</questions>"
	}
	if strings.TrimSpace(question) == "" {
		return ""
	}
	return "<question>" + question + "</question>"
}

func normalizePromptSubQuestions(subQuestions []string) []string {
	normalized := make([]string, 0, len(subQuestions))
	for _, question := range subQuestions {
		question = strings.TrimSpace(question)
		if question != "" {
			normalized = append(normalized, question)
		}
	}
	return normalized
}

// DeriveIntentAttribution 按库推导意图归属：最终存活 chunk 的 collection 属于某命中意图的
// 绑定库即归属该意图。归属与证据经由哪条通道到达无关；同一库被多个意图绑定时全部归属
// （确定性多归属）；未命中任何意图绑定库的 chunk（如全局补充路证据）天然无归属。
// 对齐 Java KnowledgeRetrievalResult.deriveAttribution。
func DeriveIntentAttribution(chunks []RetrievedChunk, kbIntents []NodeScore) map[string]struct{} {
	intentIDs := make(map[string]struct{})
	if len(chunks) == 0 || len(kbIntents) == 0 {
		return intentIDs
	}

	// 库名 -> 绑定该库的意图 ID 集合
	intentIDsByCollection := make(map[string]map[string]struct{})
	for _, ns := range kbIntents {
		if ns.Node.Kind != IntentKindKB {
			continue
		}
		intentID := strings.TrimSpace(ns.Node.ID)
		if intentID == "" {
			continue
		}
		for _, collection := range ns.Node.EffectiveCollectionNames() {
			if intentIDsByCollection[collection] == nil {
				intentIDsByCollection[collection] = make(map[string]struct{})
			}
			intentIDsByCollection[collection][intentID] = struct{}{}
		}
	}
	if len(intentIDsByCollection) == 0 {
		return intentIDs
	}

	for _, chunk := range chunks {
		collection := strings.TrimSpace(chunk.Metadata["collection_name"])
		if collection == "" {
			continue
		}
		for intentID := range intentIDsByCollection[collection] {
			intentIDs[intentID] = struct{}{}
		}
	}
	return intentIDs
}
