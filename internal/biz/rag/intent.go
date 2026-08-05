package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"unicode"

	intentModel "go-base-agent/internal/biz/intent_tree/model"
	"go-base-agent/internal/infra/chat"
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
	TopK                int
	McpToolID           string
	Kind                IntentKind
	PromptSnippet       string
	PromptTemplate      string
	ParamPromptTemplate string
	SortOrder           int
	Enabled             int16
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

// MergeIntentGroup 将多个子问题的意图候选合并分组。
func MergeIntentGroup(subIntents []SubQuestionIntent) IntentGroup {
	mcpIntents := make([]NodeScore, 0)
	kbIntents := make([]NodeScore, 0)
	for _, si := range subIntents {
		for _, ns := range si.NodeScores {
			switch ns.Node.Kind {
			case IntentKindMCP:
				mcpIntents = append(mcpIntents, ns)
			case IntentKindKB:
				kbIntents = append(kbIntents, ns)
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

	result := make([]SubQuestionIntent, 0, len(questions))
	for _, question := range questions {
		scores, ok := r.classifyWithLLM(ctx, question, leafNodes, nodes)
		if !ok {
			scores = scoreIntentNodes(question, leafNodes, r.opts.MinScore, r.opts.MaxIntents)
		}
		result = append(result, SubQuestionIntent{SubQuestion: strings.TrimSpace(question), NodeScores: scores})
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

func buildIntentClassifierPrompt(leafNodes []IntentNode, rawNodes []intentModel.IntentNode) string {
	var b strings.Builder
	b.WriteString("你是企业内部知识库意图分类助手，负责将用户问题路由到正确的知识分类叶子节点。\n")
	b.WriteString("只输出 JSON 数组，例如 [{\"id\":\"node-id\",\"score\":0.9,\"reason\":\"...\"}]；没有匹配时输出 []。\n")
	b.WriteString("实体导向问题必须命中关键实体名称；问题明确提到某系统时，只在该系统分类下选择；不要为了有结果强行选择弱相关分类。\n\n")
	b.WriteString("分类列表：\n")
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
		if strings.TrimSpace(node.Examples) != "" {
			b.WriteString("  examples=")
			b.WriteString(strings.TrimSpace(node.Examples))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return b.String()
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
		{normalizeText(node.CollectionName), 0.18},
		{normalizeText(node.PromptSnippet), 0.12},
		{normalizeText(node.McpToolID), 0.10},
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
	Enabled             bool
	AmbiguityScoreRatio float64
	AmbiguityMargin     float64
	MaxOptions          int
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
type IntentGuidanceService struct {
	opts    GuidanceOptions
	lister  IntentNodeLister
	checker AmbiguityChecker
}

// NewIntentGuidanceService creates a guidance service.
func NewIntentGuidanceService(opts GuidanceOptions) *IntentGuidanceService {
	if opts.AmbiguityScoreRatio <= 0 {
		opts.AmbiguityScoreRatio = 0.8
	}
	if opts.AmbiguityMargin <= 0 {
		opts.AmbiguityMargin = 0.15
	}
	if opts.MaxOptions <= 0 {
		opts.MaxOptions = 6
	}
	return &IntentGuidanceService{opts: opts}
}

// SetIntentNodeLister injects an intent node lister for domain name resolution.
func (s *IntentGuidanceService) SetIntentNodeLister(lister IntentNodeLister) {
	s.lister = lister
}

// SetAmbiguityChecker injects the optional LLM-based ambiguity checker.
func (s *IntentGuidanceService) SetAmbiguityChecker(checker AmbiguityChecker) {
	s.checker = checker
}

// DetectAmbiguity checks whether the top intents are ambiguous enough to prompt the user.
func (s *IntentGuidanceService) DetectAmbiguity(ctx context.Context, question string, subIntents []SubQuestionIntent) GuidanceDecision {
	if s == nil || !s.opts.Enabled || len(subIntents) != 1 {
		return NewGuidanceDecisionNone()
	}
	nodeIndex := s.loadNodeIndex(ctx)
	candidates := filterCandidates(subIntents[0].NodeScores, nodeIndex)
	if len(candidates) < 2 {
		return NewGuidanceDecisionNone()
	}
	top := candidates[0].Score
	second := candidates[1].Score
	if top <= 0 {
		return NewGuidanceDecisionNone()
	}

	ratio := second / top
	if ratio < s.opts.AmbiguityScoreRatio-s.opts.AmbiguityMargin {
		return NewGuidanceDecisionNone()
	}
	if s.shouldSkipGuidance(question, candidates, nodeIndex) {
		return NewGuidanceDecisionNone()
	}
	if ratio < s.opts.AmbiguityScoreRatio {
		if s.checker != nil && s.checker.CheckAmbiguity(ctx, question, candidates) {
			return s.promptDecision(question, candidates)
		}
		return NewGuidanceDecisionNone()
	}

	return s.promptDecision(question, candidates)
}

func (s *IntentGuidanceService) shouldSkipGuidance(question string, ranked []NodeScore, nodeIndex map[string]IntentNode) bool {
	domainNames := make([]string, 0)
	seenDomain := make(map[string]bool)
	for _, candidate := range ranked {
		if domain := resolveDomainName(candidate.Node, nodeIndex); domain != "" && !seenDomain[domain] {
			seenDomain[domain] = true
			domainNames = append(domainNames, domain)
		}
	}
	if len(domainNames) == 0 {
		return false
	}

	normalizedQuestion := normalizeText(question)
	for _, name := range domainNames {
		for _, alias := range buildSystemAliases(name) {
			if len(alias) >= 2 && strings.Contains(normalizedQuestion, alias) {
				return true
			}
		}
	}
	return false
}

func (s *IntentGuidanceService) promptDecision(question string, ranked []NodeScore) GuidanceDecision {
	trimmed := ranked
	if len(trimmed) > s.opts.MaxOptions {
		trimmed = trimmed[:s.opts.MaxOptions]
	}
	var b strings.Builder
	b.WriteString("问题“")
	b.WriteString(strings.TrimSpace(question))
	b.WriteString("”有歧义，请选择更接近的方向：")
	for i, candidate := range trimmed {
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("%d. %s", i+1, candidate.Node.Name))
		if candidate.Node.Description != "" {
			b.WriteString(" - ")
			b.WriteString(candidate.Node.Description)
		}
	}
	return NewGuidanceDecisionPrompt(b.String())
}

func filterCandidates(scores []NodeScore, nodeIndex map[string]IntentNode) []NodeScore {
	if len(scores) == 0 {
		return nil
	}
	out := make([]NodeScore, 0, len(scores))
	for _, score := range scores {
		if score.Node.Kind != IntentKindKB {
			continue
		}
		if score.Score <= 0 {
			continue
		}
		out = append(out, score)
	}
	if len(nodeIndex) > 0 {
		dedup := make(map[string]NodeScore, len(out))
		for _, score := range out {
			key := resolveSystemNodeID(score.Node, nodeIndex)
			if existing, ok := dedup[key]; ok {
				if existing.Score >= score.Score {
					continue
				}
			}
			dedup[key] = score
		}
		out = make([]NodeScore, 0, len(dedup))
		for _, score := range dedup {
			out = append(out, score)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Node.ID < out[j].Node.ID
		}
		return out[i].Score > out[j].Score
	})
	return out
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

func resolveSystemNodeID(node IntentNode, nodeIndex map[string]IntentNode) string {
	current := node
	parent := fetchParentNode(current, nodeIndex)
	for {
		if current.Level == 1 && (parent.ID == "" || parent.Level == 0) {
			return current.ID
		}
		if parent.ID == "" {
			return current.ID
		}
		current = parent
		parent = fetchParentNode(current, nodeIndex)
	}
}

func resolveDomainName(node IntentNode, nodeIndex map[string]IntentNode) string {
	current := node
	for {
		if current.Level == 0 {
			return strings.TrimSpace(current.Name)
		}
		parent := fetchParentNode(current, nodeIndex)
		if parent.ID == "" {
			return ""
		}
		current = parent
	}
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

func buildSystemAliases(systemName string) []string {
	systemName = strings.TrimSpace(systemName)
	if systemName == "" {
		return nil
	}
	alias := normalizeText(systemName)
	if alias == "" {
		return nil
	}
	return []string{alias}
}

// LLMAmbiguityChecker uses an LLM to confirm borderline ambiguity cases.
type LLMAmbiguityChecker struct {
	llm chat.LLMService
}

// NewLLMAmbiguityChecker creates a new LLM-based ambiguity checker.
func NewLLMAmbiguityChecker(llm chat.LLMService) *LLMAmbiguityChecker {
	return &LLMAmbiguityChecker{llm: llm}
}

// CheckAmbiguity calls the LLM and returns true when clarification is needed.
func (c *LLMAmbiguityChecker) CheckAmbiguity(ctx context.Context, question string, ranked []NodeScore) bool {
	if c == nil || c.llm == nil || len(ranked) == 0 {
		return true
	}
	req := chat.Request{
		Messages: []chat.Message{
			chat.NewSystemMessage("你是一个歧义确认器。只返回严格 JSON 对象，例如 {\"ambiguous\":true,\"reason\":\"...\"}。"),
			chat.NewUserMessage(buildAmbiguityCheckPrompt(question, ranked)),
		},
		Temperature: floatPtr(0.1),
		TopP:        floatPtr(0.3),
		Thinking:    boolPtr(false),
	}
	raw, err := c.llm.Chat(ctx, req)
	if err != nil {
		slog.Warn("ambiguity check llm failed, fallback to clarify", "err", err)
		return true
	}
	cleaned := stripCodeFence(strings.TrimSpace(raw))
	if cleaned == "" {
		return true
	}
	var result struct {
		Ambiguous bool   `json:"ambiguous"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
		slog.Warn("ambiguity check llm returned invalid json, fallback to clarify", "raw", raw, "err", err)
		return true
	}
	return result.Ambiguous
}

func buildAmbiguityCheckPrompt(question string, ranked []NodeScore) string {
	var b strings.Builder
	b.WriteString("问题：")
	b.WriteString(strings.TrimSpace(question))
	b.WriteString("\n候选：\n")
	for _, candidate := range ranked {
		b.WriteString("- 品类ID: ")
		b.WriteString(candidate.Node.ID)
		b.WriteString(", 名称: ")
		b.WriteString(candidate.Node.Name)
		if candidate.Node.Description != "" {
			b.WriteString(", 描述: ")
			b.WriteString(candidate.Node.Description)
		}
		b.WriteString(", 分数: ")
		b.WriteString(fmt.Sprintf("%.2f", candidate.Score))
		b.WriteString("\n")
	}
	b.WriteString("请判断是否存在明显歧义，只返回 JSON。")
	return b.String()
}

func boolPtr(v bool) *bool {
	return &v
}
