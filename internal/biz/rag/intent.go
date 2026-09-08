package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"text/template"
	"unicode"

	intentModel "go-base-agent/internal/biz/intent_tree/model"
	"go-base-agent/internal/infra/chat"
	"go-base-agent/prompts"
)

// IntentKind 区分知识库、系统和 MCP 意图。
type IntentKind int16

const (
	IntentKindKB     IntentKind = 0
	IntentKindSystem IntentKind = 1
	IntentKindMCP    IntentKind = 2
)

// IntentNode 是 Go 侧使用的意图节点视图。
type IntentNode struct {
	ID                  string
	IntentCode          string
	Name                string
	Level               int16
	ParentCode          string
	Description         string
	Examples            string
	CollectionName      string
	CollectionNames     []string
	TopK                int
	McpToolID           string
	Kind                IntentKind
	PromptSnippet       string
	PromptTemplate      string
	ParamPromptTemplate string
	SortOrder           int
	Enabled             int16
	// FullPath 根到叶的完整路径展示（如「业务系统 > OA系统 > 系统介绍」），
	// 仅在歧义澄清链路按需填充，对齐 Java IntentNode.fullPath。
	FullPath string
}

// EffectiveCollectionNames 返回当前意图实际参与检索的 Collection 集合。
// 新字段 CollectionNames 优先，旧的单 CollectionName 字段仅作平滑升级兜底。
func (n IntentNode) EffectiveCollectionNames() []string {
	seen := make(map[string]struct{}, len(n.CollectionNames)+1)
	result := make([]string, 0, len(n.CollectionNames)+1)
	for _, name := range n.CollectionNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	if len(result) == 0 {
		if fallback := strings.TrimSpace(n.CollectionName); fallback != "" {
			result = append(result, fallback)
		}
	}
	return result
}

// NodeScore 表示一个意图节点与问题的匹配分数。
type NodeScore struct {
	Node  IntentNode
	Score float64
}

// SubQuestionIntent 表示单个子问题及其意图候选。
type SubQuestionIntent struct {
	SubQuestion string
	NodeScores  []NodeScore
}

// TopLeafID 返回当前子问题的 top-1 leaf 节点 ID。
func (s SubQuestionIntent) TopLeafID() string {
	if len(s.NodeScores) == 0 {
		return ""
	}
	return s.NodeScores[0].Node.ID
}

// IntentGroup 将意图按 MCP / KB 分组。
type IntentGroup struct {
	MCPIntents []NodeScore
	KBIntents  []NodeScore
}

// MergeIntentGroup 将多个子问题的意图候选合并分组，对齐 Java NodeScoreFilters：
// MCP 意图额外要求 mcpToolId 非空（未绑定工具的 MCP 节点无法执行）；SYSTEM 意图不参与检索分组。
func MergeIntentGroup(subIntents []SubQuestionIntent) IntentGroup {
	mcpIntents := make([]NodeScore, 0)
	kbIntents := make([]NodeScore, 0)
	for _, si := range subIntents {
		for _, ns := range si.NodeScores {
			switch ns.Node.Kind {
			case IntentKindMCP:
				if strings.TrimSpace(ns.Node.McpToolID) != "" {
					mcpIntents = append(mcpIntents, ns)
				}
			case IntentKindSystem:
				// SYSTEM 意图是纯交互应答，不参与 MCP/KB 检索分组。
			default:
				kbIntents = append(kbIntents, ns)
			}
		}
	}
	return IntentGroup{MCPIntents: mcpIntents, KBIntents: kbIntents}
}

// IntentLeafIDs returns top-1 leaf ids while preserving null slots for misses.
func IntentLeafIDs(subIntents []SubQuestionIntent) []*string {
	ids := make([]*string, 0, len(subIntents))
	for _, si := range subIntents {
		id := si.TopLeafID()
		if id == "" {
			ids = append(ids, nil)
			continue
		}
		value := id
		ids = append(ids, &value)
	}
	return ids
}

// IntentNodeLister lists intent nodes for classification.
type IntentNodeLister interface {
	ListAll(ctx context.Context) ([]intentModel.IntentNode, error)
}

// IntentResolutionService resolves questions into intent candidates.
type IntentResolutionService interface {
	ResolveQuestions(ctx context.Context, questions []string) ([]SubQuestionIntent, error)
}

// IntentResolverOptions controls intent resolution behavior.
type IntentResolverOptions struct {
	MinScore   float64
	MaxIntents int
}

// AmbiguityChecker confirms whether a borderline case should trigger clarification.
type AmbiguityChecker interface {
	CheckAmbiguity(ctx context.Context, question string, ranked []NodeScore) bool
}

// IntentResolver resolves sub-questions into scored leaf intents.
type IntentResolver struct {
	lister IntentNodeLister
	llm    chat.LLMService
	opts   IntentResolverOptions
}

// NewIntentResolver creates a new resolver.
func NewIntentResolver(lister IntentNodeLister, opts IntentResolverOptions) *IntentResolver {
	if opts.MinScore <= 0 {
		opts.MinScore = 0.1
	}
	if opts.MaxIntents <= 0 {
		opts.MaxIntents = 5
	}
	return &IntentResolver{lister: lister, opts: opts}
}

// SetLLMService injects the optional LLM intent classifier.
func (r *IntentResolver) SetLLMService(llm chat.LLMService) {
	r.llm = llm
}

// ResolveQuestions resolves one or more questions into sub-question intents.
// 多个子问题的意图分类并行执行，对齐 Java IntentResolver 的 intentClassifyExecutor 并行策略；
// 单个子问题分类失败时降级为启发式打分，不阻断其余子问题。
func (r *IntentResolver) ResolveQuestions(ctx context.Context, questions []string) ([]SubQuestionIntent, error) {
	if r == nil || r.lister == nil {
		return nil, nil
	}
	nodes, err := r.lister.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("list intent nodes: %w", err)
	}
	leafNodes := leafIntentNodes(nodes)
	if len(leafNodes) == 0 {
		return []SubQuestionIntent{}, nil
	}

	if len(questions) == 0 {
		return []SubQuestionIntent{}, nil
	}

	type questionResult struct {
		index  int
		scores []NodeScore
	}
	results := make([][]NodeScore, len(questions))
	var wg sync.WaitGroup
	for i, question := range questions {
		wg.Add(1)
		go func(idx int, q string) {
			defer wg.Done()
			scores, ok := r.classifyWithLLM(ctx, q, leafNodes, nodes)
			if !ok {
				scores = scoreIntentNodes(q, leafNodes, r.opts.MinScore, r.opts.MaxIntents)
			}
			results[idx] = scores
		}(i, question)
	}
	wg.Wait()

	result := make([]SubQuestionIntent, 0, len(questions))
	for i, question := range questions {
		result = append(result, SubQuestionIntent{SubQuestion: strings.TrimSpace(question), NodeScores: results[i]})
	}
	return capTotalIntents(result, r.opts.MaxIntents), nil
}

type intentCandidate struct {
	subQuestionIndex int
	nodeScore        NodeScore
}

func capTotalIntents(subIntents []SubQuestionIntent, maxIntents int) []SubQuestionIntent {
	if maxIntents <= 0 || len(subIntents) == 0 {
		return subIntents
	}
	total := 0
	for _, subIntent := range subIntents {
		total += len(subIntent.NodeScores)
	}
	if total <= maxIntents {
		return subIntents
	}

	candidates := collectIntentCandidates(subIntents)
	guaranteed := make([]intentCandidate, 0, len(subIntents))
	selected := make(map[int]bool, len(subIntents))
	for _, candidate := range candidates {
		if selected[candidate.subQuestionIndex] {
			continue
		}
		guaranteed = append(guaranteed, candidate)
		selected[candidate.subQuestionIndex] = true
		if len(guaranteed) >= maxIntents {
			return rebuildSubQuestionIntents(subIntents, guaranteed)
		}
	}

	retained := append([]intentCandidate(nil), guaranteed...)
	remaining := maxIntents - len(retained)
	for _, candidate := range candidates {
		if remaining <= 0 {
			break
		}
		if containsIntentCandidate(guaranteed, candidate) {
			continue
		}
		retained = append(retained, candidate)
		remaining--
	}
	return rebuildSubQuestionIntents(subIntents, retained)
}

func collectIntentCandidates(subIntents []SubQuestionIntent) []intentCandidate {
	candidates := make([]intentCandidate, 0)
	for i, subIntent := range subIntents {
		for _, nodeScore := range subIntent.NodeScores {
			candidates = append(candidates, intentCandidate{
				subQuestionIndex: i,
				nodeScore:        nodeScore,
			})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].nodeScore.Score == candidates[j].nodeScore.Score {
			return candidates[i].nodeScore.Node.ID < candidates[j].nodeScore.Node.ID
		}
		return candidates[i].nodeScore.Score > candidates[j].nodeScore.Score
	})
	return candidates
}

func containsIntentCandidate(candidates []intentCandidate, target intentCandidate) bool {
	for _, candidate := range candidates {
		if candidate.subQuestionIndex == target.subQuestionIndex && candidate.nodeScore.Node.ID == target.nodeScore.Node.ID {
			return true
		}
	}
	return false
}

func rebuildSubQuestionIntents(original []SubQuestionIntent, retained []intentCandidate) []SubQuestionIntent {
	grouped := make(map[int][]NodeScore, len(original))
	for _, candidate := range retained {
		grouped[candidate.subQuestionIndex] = append(grouped[candidate.subQuestionIndex], candidate.nodeScore)
	}
	result := make([]SubQuestionIntent, 0, len(original))
	for i, subIntent := range original {
		scores := append([]NodeScore(nil), grouped[i]...)
		sort.SliceStable(scores, func(i, j int) bool {
			if scores[i].Score == scores[j].Score {
				return scores[i].Node.ID < scores[j].Node.ID
			}
			return scores[i].Score > scores[j].Score
		})
		result = append(result, SubQuestionIntent{
			SubQuestion: subIntent.SubQuestion,
			NodeScores:  scores,
		})
	}
	return result
}

func (r *IntentResolver) classifyWithLLM(ctx context.Context, question string, leafNodes []IntentNode, rawNodes []intentModel.IntentNode) ([]NodeScore, bool) {
	if r == nil || r.llm == nil || len(leafNodes) == 0 {
		return nil, false
	}
	req := chat.Request{
		Messages: []chat.Message{
			chat.NewSystemMessage(buildIntentClassifierPrompt(leafNodes, rawNodes)),
			chat.NewUserMessage(strings.TrimSpace(question)),
		},
		Temperature: floatPtr(0.1),
		TopP:        floatPtr(0.3),
		Thinking:    boolPtr(false),
	}
	raw, err := r.llm.Chat(ctx, req)
	if err != nil {
		slog.Warn("intent classifier llm failed, fallback to heuristic", "err", err)
		return nil, false
	}
	scores, err := parseIntentClassifierResponse(raw, leafNodes, r.opts.MinScore, r.opts.MaxIntents)
	if err != nil {
		slog.Warn("intent classifier llm returned invalid response, fallback to heuristic", "raw", raw, "err", err)
		return nil, false
	}
	return scores, true
}

// promptTemplateCache 缓存已解析的内嵌提示词模板；value 为 *template.Template，
// 加载失败时缓存 nil，避免每次调用重复读内嵌文件。
var promptTemplateCache sync.Map

// loadEmbeddedPromptTemplate 惰性解析内嵌提示词模板；文件缺失或非法时返回 nil。
func loadEmbeddedPromptTemplate(name string) *template.Template {
	if cached, ok := promptTemplateCache.Load(name); ok {
		tmpl, _ := cached.(*template.Template)
		return tmpl
	}
	content, err := prompts.FS.ReadFile(name)
	if err != nil {
		slog.Warn("prompt template missing, fallback to inline prompt", "name", name, "err", err)
		promptTemplateCache.Store(name, (*template.Template)(nil))
		return nil
	}
	tmpl, err := template.New(name).Parse(string(content))
	if err != nil {
		slog.Warn("prompt template invalid, fallback to inline prompt", "name", name, "err", err)
		promptTemplateCache.Store(name, (*template.Template)(nil))
		return nil
	}
	promptTemplateCache.Store(name, tmpl)
	return tmpl
}

// renderEmbeddedPrompt 渲染内嵌提示词模板；模板不可用或渲染失败时返回 false。
func renderEmbeddedPrompt(name string, data any) (string, bool) {
	tmpl := loadEmbeddedPromptTemplate(name)
	if tmpl == nil {
		return "", false
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		slog.Warn("prompt template render failed, fallback to inline prompt", "name", name, "err", err)
		return "", false
	}
	return buf.String(), true
}

// intentClassifierTemplateFile 对齐 Java prompt/intent-classifier.st。
const intentClassifierTemplateFile = "intent_classifier.txt"

func buildIntentClassifierPrompt(leafNodes []IntentNode, rawNodes []intentModel.IntentNode) string {
	nodeList := buildIntentClassifierNodeList(leafNodes, rawNodes)
	if prompt, ok := renderEmbeddedPrompt(intentClassifierTemplateFile, map[string]string{"IntentList": nodeList}); ok {
		return prompt
	}
	// 模板不可用时的内联兜底，规则与 intent_classifier.txt 保持同一语义的精简版。
	var b strings.Builder
	b.WriteString("你是企业内部助手的意图分类器，负责将用户输入路由到正确的分类叶子节点。\n")
	b.WriteString("只输出 JSON 数组，例如 [{\"id\":\"node-id\",\"score\":0.9,\"reason\":\"...\"}]；没有匹配时输出 []。\n")
	b.WriteString("分类判断规则：\n")
	b.WriteString("- 交互导向：问候、询问助手身份或能力、致谢、评价上一轮回答等没有新业务问题的输入，只在 type=SYSTEM 节点中选择。\n")
	b.WriteString("- 实体导向：包含具体系统、产品、模块或客户名称时，必须命中关键实体名称；问题明确提到某系统时，只在该系统分类下选择。\n")
	b.WriteString("- 主题导向：没有具体实体名称时，匹配分类 path 和 description 中的主题词。\n")
	b.WriteString("不要为了有结果强行选择弱相关分类；所有候选分数都低于 0.6 时返回 []。交互导向输入与某个 type=SYSTEM 节点的交际行为一致时按强匹配打分。\n\n")
	b.WriteString("分类列表：\n")
	b.WriteString(nodeList)
	return b.String()
}

func buildIntentClassifierNodeList(leafNodes []IntentNode, rawNodes []intentModel.IntentNode) string {
	var b strings.Builder
	nodeIndex := make(map[string]IntentNode, len(rawNodes))
	for _, node := range rawNodes {
		if node.Enabled != 1 {
			continue
		}
		view := toIntentNode(node)
		nodeIndex[strings.TrimSpace(view.IntentCode)] = view
	}
	for _, node := range leafNodes {
		b.WriteString("- id=")
		b.WriteString(node.ID)
		b.WriteString("\n")
		b.WriteString("  path=")
		b.WriteString(intentFullPath(node, nodeIndex))
		b.WriteString("\n")
		b.WriteString("  description=")
		b.WriteString(strings.TrimSpace(node.Description))
		b.WriteString("\n")
		switch node.Kind {
		case IntentKindMCP:
			b.WriteString("  type=MCP\n")
			if strings.TrimSpace(node.McpToolID) != "" {
				b.WriteString("  toolId=")
				b.WriteString(strings.TrimSpace(node.McpToolID))
				b.WriteString("\n")
			}
		case IntentKindSystem:
			b.WriteString("  type=SYSTEM\n")
		default:
			b.WriteString("  type=KB\n")
		}
		if examples := formatIntentExamples(node.Examples); examples != "" {
			b.WriteString("  examples=")
			b.WriteString(examples)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func formatIntentExamples(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var values []string
	if err := json.Unmarshal([]byte(trimmed), &values); err != nil {
		return trimmed
	}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			normalized = append(normalized, value)
		}
	}
	return strings.Join(normalized, " / ")
}

func intentFullPath(node IntentNode, nodeIndex map[string]IntentNode) string {
	names := []string{strings.TrimSpace(node.Name)}
	current := node
	for {
		parent := fetchParentNode(current, nodeIndex)
		if parent.ID == "" {
			break
		}
		if strings.TrimSpace(parent.Name) != "" {
			names = append([]string{strings.TrimSpace(parent.Name)}, names...)
		}
		current = parent
	}
	return strings.Join(names, " > ")
}

func parseIntentClassifierResponse(raw string, leafNodes []IntentNode, minScore float64, maxIntents int) ([]NodeScore, error) {
	cleaned := stripCodeFence(strings.TrimSpace(raw))
	if cleaned == "" {
		return nil, fmt.Errorf("empty response")
	}
	var items []struct {
		ID     string  `json:"id"`
		Score  float64 `json:"score"`
		Reason string  `json:"reason"`
	}
	if err := json.Unmarshal([]byte(cleaned), &items); err != nil {
		var obj struct {
			Results []struct {
				ID     string  `json:"id"`
				Score  float64 `json:"score"`
				Reason string  `json:"reason"`
			} `json:"results"`
		}
		if objErr := json.Unmarshal([]byte(cleaned), &obj); objErr != nil {
			start := strings.Index(cleaned, "[")
			end := strings.LastIndex(cleaned, "]")
			if start < 0 || end <= start || json.Unmarshal([]byte(cleaned[start:end+1]), &items) != nil {
				return nil, err
			}
		} else {
			items = obj.Results
		}
	}
	nodeByID := make(map[string]IntentNode, len(leafNodes))
	for _, node := range leafNodes {
		nodeByID[strings.TrimSpace(node.ID)] = node
	}
	scores := make([]NodeScore, 0, len(items))
	for _, item := range items {
		node, ok := nodeByID[strings.TrimSpace(item.ID)]
		if !ok {
			continue
		}
		score := item.Score
		if score < minScore {
			continue
		}
		if score > 1 {
			score = 1
		}
		if score < 0 {
			score = 0
		}
		scores = append(scores, NodeScore{Node: node, Score: score})
	}
	sort.SliceStable(scores, func(i, j int) bool {
		if scores[i].Score == scores[j].Score {
			return scores[i].Node.ID < scores[j].Node.ID
		}
		return scores[i].Score > scores[j].Score
	})
	if maxIntents > 0 && len(scores) > maxIntents {
		scores = scores[:maxIntents]
	}
	return scores, nil
}

func leafIntentNodes(nodes []intentModel.IntentNode) []IntentNode {
	if len(nodes) == 0 {
		return nil
	}
	children := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		if node.Enabled != 1 {
			continue
		}
		if strings.TrimSpace(node.ParentCode) != "" {
			children[strings.TrimSpace(node.ParentCode)] = struct{}{}
		}
	}
	leaves := make([]IntentNode, 0, len(nodes))
	for _, node := range nodes {
		if node.Enabled != 1 {
			continue
		}
		if _, ok := children[strings.TrimSpace(node.IntentCode)]; ok {
			continue
		}
		leaves = append(leaves, toIntentNode(node))
	}
	sort.SliceStable(leaves, func(i, j int) bool {
		if leaves[i].SortOrder == leaves[j].SortOrder {
			return leaves[i].ID < leaves[j].ID
		}
		return leaves[i].SortOrder < leaves[j].SortOrder
	})
	return leaves
}

func scoreIntentNodes(question string, nodes []IntentNode, minScore float64, maxIntents int) []NodeScore {
	normalized := normalizeText(question)
	if normalized == "" {
		return nil
	}

	scores := make([]NodeScore, 0, len(nodes))
	for _, node := range nodes {
		score := scoreIntentNode(normalized, node)
		if score < minScore {
			continue
		}
		scores = append(scores, NodeScore{Node: node, Score: score})
	}

	sort.SliceStable(scores, func(i, j int) bool {
		if scores[i].Score == scores[j].Score {
			return scores[i].Node.ID < scores[j].Node.ID
		}
		return scores[i].Score > scores[j].Score
	})
	if maxIntents > 0 && len(scores) > maxIntents {
		scores = scores[:maxIntents]
	}
	return scores
}

func scoreIntentNode(question string, node IntentNode) float64 {
	score := 0.0
	fields := []struct {
		text string
		pts  float64
	}{
		{normalizeText(node.Name), 0.55},
		{normalizeText(node.Description), 0.30},
		{normalizeText(node.Examples), 0.20},
		{normalizeText(node.PromptSnippet), 0.12},
		{normalizeText(node.McpToolID), 0.10},
	}
	// 多库意图匹配任意一个库名即可得分，得分不叠加（多命中不表示意图更强）
	collectionField := ""
	for _, collection := range node.EffectiveCollectionNames() {
		if text := normalizeText(collection); text != "" {
			collectionField = text
			break
		}
	}
	if collectionField != "" {
		fields = append(fields, struct {
			text string
			pts  float64
		}{collectionField, 0.18})
	}
	for _, field := range fields {
		if field.text == "" {
			continue
		}
		if strings.Contains(question, field.text) {
			score += field.pts
		} else if cjkOverlapRatio(question, field.text) >= 0.35 {
			score += field.pts
		} else {
			for _, token := range tokenize(field.text) {
				if len(token) >= 2 && strings.Contains(question, token) {
					score += field.pts / 2
					break
				}
			}
		}
	}
	if node.Kind == IntentKindMCP && containsAny(question, []string{"查询", "调用", "执行", "发放", "办理", "实时"}) {
		score += 0.15
	}
	if node.Kind == IntentKindSystem && containsAny(question, []string{"你好", "帮助", "介绍", "是谁", "什么是"}) {
		score += 0.15
	}
	if score > 1 {
		score = 1
	}
	return score
}

func cjkOverlapRatio(question, field string) float64 {
	questionRunes := make(map[rune]struct{})
	for _, r := range question {
		if unicode.Is(unicode.Han, r) {
			questionRunes[r] = struct{}{}
		}
	}
	if len(questionRunes) == 0 {
		return 0
	}
	seen := make(map[rune]struct{})
	matches := 0
	total := 0
	for _, r := range field {
		if !unicode.Is(unicode.Han, r) {
			continue
		}
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		total++
		if _, ok := questionRunes[r]; ok {
			matches++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(matches) / float64(total)
}

func toIntentNode(node intentModel.IntentNode) IntentNode {
	kind := IntentKind(node.Kind)
	if kind != IntentKindKB && kind != IntentKindSystem && kind != IntentKindMCP {
		kind = IntentKindKB
	}
	return IntentNode{
		ID:                  node.ID,
		IntentCode:          node.IntentCode,
		Name:                node.Name,
		Level:               node.Level,
		ParentCode:          node.ParentCode,
		Description:         node.Description,
		Examples:            node.Examples,
		CollectionName:      node.CollectionName,
		CollectionNames:     node.CollectionNames,
		TopK:                node.TopK,
		McpToolID:           node.McpToolID,
		Kind:                kind,
		PromptSnippet:       node.PromptSnippet,
		PromptTemplate:      node.PromptTemplate,
		ParamPromptTemplate: node.ParamPromptTemplate,
		SortOrder:           node.SortOrder,
		Enabled:             node.Enabled,
	}
}

func normalizeText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		case unicode.IsPunct(r), unicode.IsSymbol(r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func tokenize(s string) []string {
	if s == "" {
		return nil
	}
	return strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
	})
}

func containsAny(text string, words []string) bool {
	for _, word := range words {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}

// GuidanceAction declares how the pipeline should proceed after ambiguity analysis.
type GuidanceAction string

const (
	// GuidanceActionNone means no clarification is needed.
	GuidanceActionNone GuidanceAction = "none"
	// GuidanceActionPrompt means the pipeline should ask a clarifying question.
	GuidanceActionPrompt GuidanceAction = "prompt"
)

// GuidanceOptions controls ambiguity detection.
type GuidanceOptions struct {
	Enabled    bool
	MaxOptions int
	// AmbiguityScoreRatio / AmbiguityMargin 对齐 Java GuidanceProperties：
	// 歧义判定已改为候选路径重名触发，这两个值当前不参与判定，保留以兼容既有 yaml。
	AmbiguityScoreRatio float64
	AmbiguityMargin     float64
}

// GuidanceDecision describes whether the pipeline should ask a clarification question.
type GuidanceDecision struct {
	Action GuidanceAction
	Prompt string
}

// NewGuidanceDecisionNone creates a no-op decision.
func NewGuidanceDecisionNone() GuidanceDecision {
	return GuidanceDecision{Action: GuidanceActionNone}
}

// NewGuidanceDecisionPrompt creates a prompt decision.
func NewGuidanceDecisionPrompt(prompt string) GuidanceDecision {
	return GuidanceDecision{Action: GuidanceActionPrompt, Prompt: prompt}
}

// IntentGuidanceService decides whether the user should be asked to clarify intent.
// 意图树层级由用户自行配置，因此只按「根到叶的节点路径」找重名分叉，
// 再由 LLM 确认是否真的需要用户选择，对齐 Java IntentGuidanceService。
type IntentGuidanceService struct {
	opts    GuidanceOptions
	lister  IntentNodeLister
	checker AmbiguityChecker
}

// NewIntentGuidanceService creates a guidance service.
func NewIntentGuidanceService(opts GuidanceOptions) *IntentGuidanceService {
	if opts.MaxOptions <= 0 {
		opts.MaxOptions = 6
	}
	return &IntentGuidanceService{opts: opts}
}

// SetIntentNodeLister injects an intent node lister for path resolution.
func (s *IntentGuidanceService) SetIntentNodeLister(lister IntentNodeLister) {
	s.lister = lister
}

// SetAmbiguityChecker injects the LLM-based ambiguity checker.
func (s *IntentGuidanceService) SetAmbiguityChecker(checker AmbiguityChecker) {
	s.checker = checker
}

// DetectAmbiguity 检查候选意图是否在路径上重名且需要用户澄清。
func (s *IntentGuidanceService) DetectAmbiguity(ctx context.Context, question string, subIntents []SubQuestionIntent) GuidanceDecision {
	if s == nil || !s.opts.Enabled || len(subIntents) != 1 {
		return NewGuidanceDecisionNone()
	}
	nodeIndex := s.loadNodeIndex(ctx)
	ranked := rankCandidates(filterCandidates(subIntents[0].NodeScores))
	if len(ranked) < 2 {
		return NewGuidanceDecisionNone()
	}
	conflict := collectPathConflicts(question, ranked, nodeIndex)
	if conflict == nil {
		return NewGuidanceDecisionNone()
	}
	if s.checker == nil || !s.checker.CheckAmbiguity(ctx, question, conflict.ranked) {
		slog.Info("LLM 判定候选路径不构成歧义, 跳过澄清", "question", question)
		return NewGuidanceDecisionNone()
	}
	return s.promptDecision(conflict.topicName, conflict.ranked)
}

// pathConflict 待 LLM 确认的路径重名候选组。
type pathConflict struct {
	topicName string
	ranked    []NodeScore
}

// collectPathConflicts 以最高分候选为主候选，收集与它构成路径重名的其它候选。
func collectPathConflicts(question string, ranked []NodeScore, nodeIndex map[string]IntentNode) *pathConflict {
	primaryPath := buildGuidanceNodePath(ranked[0].Node, nodeIndex)
	ranked[0].Node.FullPath = guidancePathString(primaryPath)
	normalizedQuestion := normalizeGuidanceName(question)

	conflicts := make([]NodeScore, 0, len(ranked))
	conflicts = append(conflicts, ranked[0])
	topicName := ""
	for _, other := range ranked[1:] {
		otherPath := buildGuidanceNodePath(other.Node, nodeIndex)
		hitName := detectConflictName(primaryPath, otherPath, normalizedQuestion)
		if hitName == "" {
			continue
		}
		other.Node.FullPath = guidancePathString(otherPath)
		conflicts = append(conflicts, other)
		if topicName == "" {
			topicName = hitName
		}
	}
	if len(conflicts) < 2 {
		return nil
	}
	slog.Info("候选意图路径重名, 调 LLM 确认是否需要澄清", "topicName", topicName, "question", question)
	return &pathConflict{topicName: topicName, ranked: conflicts}
}

// detectConflictName 返回两条路径的冲突名称，无冲突返回空串。
// 叶子重名直接算冲突；分叉后的中间节点重名还要求用户问题里提到了这个名称，
// 否则用户问的并不是这个岔路口；公共前缀是同一批真实节点，共享它不构成歧义。
func detectConflictName(primaryPath, otherPath []IntentNode, normalizedQuestion string) string {
	if len(primaryPath) == 0 || len(otherPath) == 0 {
		return ""
	}
	primaryLeaf := primaryPath[len(primaryPath)-1]
	leafName := normalizeGuidanceName(primaryLeaf.Name)
	if isComparableName(leafName) && leafName == normalizeGuidanceName(otherPath[len(otherPath)-1].Name) {
		return primaryLeaf.Name
	}
	common := commonPrefixLength(primaryPath, otherPath)
	otherNames := make(map[string]struct{})
	for _, node := range otherPath[common:] {
		if name := normalizeGuidanceName(node.Name); isComparableName(name) {
			otherNames[name] = struct{}{}
		}
	}
	for _, node := range primaryPath[common:] {
		name := normalizeGuidanceName(node.Name)
		if _, ok := otherNames[name]; ok && isComparableName(name) && strings.Contains(normalizedQuestion, name) {
			return node.Name
		}
	}
	return ""
}

// buildGuidanceNodePath 沿 ParentCode 上溯出「根 → ... → 候选」的完整节点路径。
// visited 兜住配置错误形成的父子环，父节点缺失时停在已取到的链路上。
func buildGuidanceNodePath(node IntentNode, nodeIndex map[string]IntentNode) []IntentNode {
	path := make([]IntentNode, 0, 4)
	visited := make(map[string]struct{})
	current := node
	for {
		path = append(path, current)
		visited[current.ID] = struct{}{}
		parentCode := strings.TrimSpace(current.ParentCode)
		if parentCode == "" {
			break
		}
		if _, ok := visited[parentCode]; ok {
			break
		}
		parent, ok := nodeIndex[parentCode]
		if !ok {
			break
		}
		current = parent
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

// commonPrefixLength 按节点 ID 计算最长公共前缀长度。
func commonPrefixLength(left, right []IntentNode) int {
	max := len(left)
	if len(right) < max {
		max = len(right)
	}
	index := 0
	for index < max && left[index].ID != "" && left[index].ID == right[index].ID {
		index++
	}
	return index
}

// guidancePathString 把根到叶的节点路径拼接为展示用完整路径。
func guidancePathString(path []IntentNode) string {
	names := make([]string, 0, len(path))
	for _, node := range path {
		if name := strings.TrimSpace(node.Name); name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, " > ")
}

// normalizeGuidanceName 统一名称比较形态：小写并剔除全部标点与空白。
func normalizeGuidanceName(name string) string {
	cleaned := strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	b.Grow(len(cleaned))
	for _, r := range cleaned {
		if unicode.IsPunct(r) || unicode.IsSymbol(r) || unicode.IsSpace(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isComparableName 参与重名比较的最短名称长度为 2，避免单字名把无关路径判成冲突。
func isComparableName(normalizedName string) bool {
	return len([]rune(normalizedName)) >= minComparableNameLength
}

// guidanceIntentMinScore 歧义候选准入分数，对齐 Java RAGConstant.INTENT_MIN_SCORE。
const guidanceIntentMinScore = 0.35

// minComparableNameLength 参与重名比较的最短名称长度。
const minComparableNameLength = 2

// guidancePromptTemplateFile 对齐 Java prompt/guidance-prompt.st。
const guidancePromptTemplateFile = "guidance_prompt.txt"

// filterCandidates 只保留达到准入分数的 KB 意图，对齐 Java NodeScoreFilters.kb(scores, INTENT_MIN_SCORE)。
func filterCandidates(scores []NodeScore) []NodeScore {
	out := make([]NodeScore, 0, len(scores))
	for _, score := range scores {
		if score.Node.Kind != IntentKindKB {
			continue
		}
		if score.Score < guidanceIntentMinScore {
			continue
		}
		out = append(out, score)
	}
	return out
}

// rankCandidates 按节点去重并按分数降序，同一节点重复命中时保留高分那条。
func rankCandidates(candidates []NodeScore) []NodeScore {
	if len(candidates) == 0 {
		return nil
	}
	bestByNode := make(map[string]NodeScore, len(candidates))
	for _, candidate := range candidates {
		key := strings.TrimSpace(candidate.Node.ID)
		if key == "" {
			key = strings.TrimSpace(candidate.Node.Name)
		}
		if existing, ok := bestByNode[key]; ok && existing.Score >= candidate.Score {
			continue
		}
		bestByNode[key] = candidate
	}
	ranked := make([]NodeScore, 0, len(bestByNode))
	for _, candidate := range bestByNode {
		ranked = append(ranked, candidate)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score == ranked[j].Score {
			return ranked[i].Node.ID < ranked[j].Node.ID
		}
		return ranked[i].Score > ranked[j].Score
	})
	return ranked
}

func (s *IntentGuidanceService) promptDecision(topicName string, ranked []NodeScore) GuidanceDecision {
	trimmed := ranked
	if len(trimmed) > s.opts.MaxOptions {
		trimmed = trimmed[:s.opts.MaxOptions]
	}
	options := renderGuidanceOptions(trimmed)
	if prompt, ok := renderEmbeddedPrompt(guidancePromptTemplateFile, map[string]string{
		"TopicName": strings.TrimSpace(topicName),
		"Options":   options,
	}); ok {
		return NewGuidanceDecisionPrompt(prompt)
	}
	// 模板不可用时的内联兜底，文案与 guidance_prompt.txt 同义。
	var b strings.Builder
	b.WriteString("关于")
	b.WriteString(strings.TrimSpace(topicName))
	b.WriteString("，在知识库中检索到了以下内容：\n")
	b.WriteString(options)
	b.WriteString("\n\n请问你具体想了解哪个？请回复数字选择（可多选，如 1,2），或回复“都/全部”")
	return NewGuidanceDecisionPrompt(b.String())
}

// renderGuidanceOptions 渲染澄清选项，展示优先使用完整路径，兜底节点名或 ID。
func renderGuidanceOptions(ranked []NodeScore) string {
	var b strings.Builder
	for i, candidate := range ranked {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(fmt.Sprintf("%d) %s", i+1, resolveGuidanceOptionDisplay(candidate.Node)))
	}
	return b.String()
}

func resolveGuidanceOptionDisplay(node IntentNode) string {
	if fullPath := strings.TrimSpace(node.FullPath); fullPath != "" {
		return fullPath
	}
	if name := strings.TrimSpace(node.Name); name != "" {
		return name
	}
	return node.ID
}

func (s *IntentGuidanceService) loadNodeIndex(ctx context.Context) map[string]IntentNode {
	if s == nil || s.lister == nil {
		return nil
	}
	nodes, err := s.lister.ListAll(ctx)
	if err != nil {
		slog.Warn("load intent nodes for guidance failed", "err", err)
		return nil
	}
	index := make(map[string]IntentNode, len(nodes))
	for _, node := range nodes {
		index[strings.TrimSpace(node.IntentCode)] = toIntentNode(node)
	}
	return index
}

func fetchParentNode(node IntentNode, nodeIndex map[string]IntentNode) IntentNode {
	if len(nodeIndex) == 0 {
		return IntentNode{}
	}
	parentCode := strings.TrimSpace(node.ParentCode)
	if parentCode == "" {
		return IntentNode{}
	}
	parent, ok := nodeIndex[parentCode]
	if !ok {
		return IntentNode{}
	}
	return parent
}

// LLMAmbiguityChecker uses an LLM to confirm whether ambiguous candidates truly need clarification.
// 规则层只能发现候选路径重名，是否真的要用户二选一由 LLM 判断；
// 纯 RAG 是只读流程，判不出来时一律放行联合检索，不能因为模型异常反复阻断用户。
type LLMAmbiguityChecker struct {
	llm chat.LLMService
}

// NewLLMAmbiguityChecker creates a new LLM-based ambiguity checker.
func NewLLMAmbiguityChecker(llm chat.LLMService) *LLMAmbiguityChecker {
	return &LLMAmbiguityChecker{llm: llm}
}

// CheckAmbiguity 调用 LLM 确认是否存在歧义，响应非法、缺字段或调用失败时返回 false。
func (c *LLMAmbiguityChecker) CheckAmbiguity(ctx context.Context, question string, ranked []NodeScore) bool {
	if c == nil || c.llm == nil || len(ranked) == 0 {
		return false
	}
	prompt := buildAmbiguityCheckPrompt(question, ranked)
	req := chat.Request{
		Messages:    []chat.Message{chat.NewUserMessage(prompt)},
		Temperature: floatPtr(0.1),
		TopP:        floatPtr(0.3),
		Thinking:    boolPtr(false),
	}
	raw, err := chat.ChatWithTier(ctx, c.llm, req, "fast")
	if err != nil {
		slog.Warn("歧义确认 LLM 调用失败, 降级为跳过澄清", "question", question, "err", err)
		return false
	}
	cleaned := stripCodeFence(strings.TrimSpace(raw))
	var result struct {
		Ambiguous *bool  `json:"ambiguous"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
		slog.Warn("歧义确认 LLM 返回非 JSON 对象, 降级为跳过澄清", "raw", raw, "err", err)
		return false
	}
	if result.Ambiguous == nil {
		slog.Warn("歧义确认 LLM 返回缺少 ambiguous 字段, 降级为跳过澄清", "raw", raw)
		return false
	}
	slog.Info("LLM 歧义确认结果", "ambiguous", *result.Ambiguous, "reason", result.Reason, "question", question)
	return *result.Ambiguous
}

// guidanceAmbiguityCheckTemplateFile 对齐 Java prompt/guidance-ambiguity-check.st。
const guidanceAmbiguityCheckTemplateFile = "guidance_ambiguity_check.txt"

// buildAmbiguityCheckPrompt 渲染歧义确认提示词；模板不可用时回退内联精简版。
func buildAmbiguityCheckPrompt(question string, ranked []NodeScore) string {
	candidates := buildAmbiguityCandidatesText(ranked)
	if prompt, ok := renderEmbeddedPrompt(guidanceAmbiguityCheckTemplateFile, map[string]string{
		"Question":   strings.TrimSpace(question),
		"Candidates": candidates,
	}); ok {
		return prompt
	}
	var b strings.Builder
	b.WriteString("用户问题：")
	b.WriteString(strings.TrimSpace(question))
	b.WriteString("\n\n以下是意图分类命中的候选意图及其完整路径：\n")
	b.WriteString(candidates)
	b.WriteString("\n\n请判断：用户是否必须先在这些候选路径中选定一个，我们才能正确检索？拿不准时返回 false，让检索照常进行。\n")
	b.WriteString("以 JSON 格式输出：{\"ambiguous\": true/false, \"category_ids\": [\"最匹配或需要选择的候选意图ID\"], \"reason\": \"判断理由\"}")
	return b.String()
}

func buildAmbiguityCandidatesText(ranked []NodeScore) string {
	lines := make([]string, 0, len(ranked))
	for _, candidate := range ranked {
		node := candidate.Node
		fullPath := strings.TrimSpace(node.FullPath)
		if fullPath == "" {
			fullPath = strings.TrimSpace(node.Name)
		}
		line := fmt.Sprintf("- 意图ID: %s, 名称: %s, 完整路径: %s", node.ID, node.Name, fullPath)
		if description := strings.TrimSpace(node.Description); description != "" {
			line += ", 说明: " + description
		}
		line += fmt.Sprintf(", 匹配分数: %.2f", candidate.Score)
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func boolPtr(v bool) *bool {
	return &v
}
