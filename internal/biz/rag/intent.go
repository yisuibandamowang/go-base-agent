package rag

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	intentModel "go-base-agent/internal/biz/intent_tree/model"
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

// IntentResolver resolves sub-questions into scored leaf intents.
type IntentResolver struct {
	lister IntentNodeLister
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
		scores := scoreIntentNodes(question, leafNodes, r.opts.MinScore, r.opts.MaxIntents)
		result = append(result, SubQuestionIntent{SubQuestion: strings.TrimSpace(question), NodeScores: scores})
	}
	return result, nil
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
	opts GuidanceOptions
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
		opts.MaxOptions = 3
	}
	return &IntentGuidanceService{opts: opts}
}

// DetectAmbiguity checks whether the top intents are ambiguous enough to prompt the user.
func (s *IntentGuidanceService) DetectAmbiguity(question string, subIntents []SubQuestionIntent) GuidanceDecision {
	if s == nil || !s.opts.Enabled || len(subIntents) != 1 {
		return NewGuidanceDecisionNone()
	}
	candidates := filterCandidates(subIntents[0].NodeScores)
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
	if ratio < s.opts.AmbiguityScoreRatio {
		return NewGuidanceDecisionNone()
	}

	trimmed := candidates
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

func filterCandidates(scores []NodeScore) []NodeScore {
	if len(scores) == 0 {
		return nil
	}
	out := make([]NodeScore, 0, len(scores))
	for _, score := range scores {
		if score.Score <= 0 {
			continue
		}
		out = append(out, score)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Node.ID < out[j].Node.ID
		}
		return out[i].Score > out[j].Score
	})
	return out
}
