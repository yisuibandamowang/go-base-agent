package codeqna

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Evidence represents a code search hit.
type Evidence struct {
	File    string
	Line    string
	Content string
}

// SearchRequest controls a code repository search.
type SearchRequest struct {
	RepoPath string
	Question string
	Terms    []string
	MaxLines int
}

// Search finds code evidence for the request by recursively expanding rg hits.
func Search(ctx context.Context, req SearchRequest) ([]Evidence, error) {
	repoPath := strings.TrimSpace(req.RepoPath)
	if repoPath == "" {
		return nil, nil
	}
	info, err := os.Stat(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat code repo: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("failed to inspect code repo: path is not a directory")
	}

	terms := normalizeTerms(req.Terms)
	if len(terms) == 0 {
		terms = termsFromQuestion(req.Question)
	}
	if len(terms) == 0 {
		return nil, nil
	}

	maxLines := req.MaxLines
	if maxLines <= 0 {
		maxLines = 80
	}
	searchCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	out := make([]Evidence, 0, maxLines)
	seenEvidence := map[string]struct{}{}
	seenTerms := map[string]struct{}{}
	for depth := 0; depth < 4 && len(terms) > 0 && len(out) < maxLines; depth++ {
		nextTerms := make([]string, 0, len(terms)*2)
		for _, term := range terms {
			term = strings.TrimSpace(term)
			if term == "" {
				continue
			}
			if _, ok := seenTerms[term]; ok {
				continue
			}
			seenTerms[term] = struct{}{}
			if len(out) >= maxLines {
				break
			}
			args := []string{"-n", "--fixed-strings", "--max-count", "20", "-g", "*.go", "-g", "*.java", "-g", "*.yaml", "-g", "*.yml", "-g", "*.xml", "-g", "*.sql", term, repoPath}
			result, err := exec.CommandContext(searchCtx, "rg", args...).CombinedOutput()
			if err != nil && len(result) == 0 {
				continue
			}
			for _, line := range strings.Split(string(result), "\n") {
				if strings.TrimSpace(line) == "" || len(out) >= maxLines {
					continue
				}
				evidence := parseRgLine(line)
				key := evidence.File + ":" + evidence.Line + ":" + evidence.Content
				if _, ok := seenEvidence[key]; ok {
					continue
				}
				seenEvidence[key] = struct{}{}
				out = append(out, evidence)
				nextTerms = append(nextTerms, termsFromEvidence(evidence)...)
				if evidence.File != "" {
					nextTerms = append(nextTerms, termsFromFile(evidence.File)...)
				}
			}
		}
		terms = uniqueTerms(nextTerms)
	}
	return out, nil
}

// FormatEvidence renders evidence in a prompt-friendly format.
func FormatEvidence(items []Evidence) string {
	var b strings.Builder
	for _, item := range items {
		b.WriteString(item.File)
		if item.Line != "" {
			b.WriteString(":")
			b.WriteString(item.Line)
		}
		b.WriteString(" ")
		b.WriteString(strings.TrimSpace(item.Content))
		b.WriteByte('\n')
	}
	return b.String()
}

func termsFromQuestion(question string) []string {
	question = strings.TrimSpace(question)
	if question == "" {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0)
	add := func(term string) {
		term = strings.TrimSpace(term)
		if term == "" {
			return
		}
		if _, ok := seen[term]; ok {
			return
		}
		seen[term] = struct{}{}
		out = append(out, term)
	}

	var latin strings.Builder
	var cjk []rune
	flushLatin := func() {
		if latin.Len() > 0 {
			add(latin.String())
			latin.Reset()
		}
	}
	flushCJK := func() {
		if len(cjk) >= 2 {
			for i := 0; i+1 < len(cjk); i++ {
				add(string(cjk[i : i+2]))
			}
		}
		cjk = cjk[:0]
	}

	for _, r := range question {
		switch {
		case isCJKRune(r):
			flushLatin()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushCJK()
			latin.WriteRune(r)
		default:
			flushLatin()
			flushCJK()
		}
	}
	flushLatin()
	flushCJK()
	if len(out) > 16 {
		return out[:16]
	}
	return out
}

func termsFromEvidence(evidence Evidence) []string {
	terms := make([]string, 0, 12)
	add := func(term string) {
		term = strings.Trim(strings.TrimSpace(term), `"'，,;；。.`)
		if !isActionableCodeSearchTerm(term) {
			return
		}
		terms = append(terms, term)
	}
	for _, match := range regexp.MustCompile(`\b(ReportWithMonitorRetry|InsertTo[A-Z][A-Za-z0-9_]+|Build[A-Z][A-Za-z0-9_]+|Handle[A-Z][A-Za-z0-9_]+|[A-Za-z_][A-Za-z0-9_]*Table)\b`).FindAllString(evidence.Content, -1) {
		add(match)
	}
	for _, match := range regexp.MustCompile(`gorm:"column:([A-Za-z_][A-Za-z0-9_]*)"`).FindAllStringSubmatch(evidence.Content, -1) {
		add(match[1])
	}
	return uniqueTerms(terms)
}

func termsFromFile(filePath string) []string {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	text := string(content)
	terms := make([]string, 0, 16)
	add := func(term string) {
		term = strings.Trim(strings.TrimSpace(term), `"'，,;；。.`)
		if !isActionableCodeSearchTerm(term) {
			return
		}
		terms = append(terms, term)
	}
	for _, match := range regexp.MustCompile(`\b(ReportWithMonitorRetry|InsertTo[A-Z][A-Za-z0-9_]+|Build[A-Z][A-Za-z0-9_]+|Handle[A-Z][A-Za-z0-9_]+|[A-Za-z_][A-Za-z0-9_]*Table)\b`).FindAllString(text, -1) {
		add(match)
	}
	for _, match := range regexp.MustCompile(`gorm:"column:([A-Za-z_][A-Za-z0-9_]*)"`).FindAllStringSubmatch(text, -1) {
		add(match[1])
	}
	for _, match := range regexp.MustCompile(`(?s)TableName\(\)\s+string\s*\{[^}]*return\s+"([A-Za-z_][A-Za-z0-9_]*)"`).FindAllStringSubmatch(text, -1) {
		add(match[1])
	}
	for _, match := range regexp.MustCompile(`(?m)\b([A-Za-z_][A-Za-z0-9_]*Table)\s*=\s*"([A-Za-z_][A-Za-z0-9_]*)"`).FindAllStringSubmatch(text, -1) {
		add(match[1])
		add(match[2])
	}
	for _, match := range regexp.MustCompile(`(?i)\.Table\(\s*([A-Za-z_][A-Za-z0-9_]*Table)\s*\)`).FindAllStringSubmatch(text, -1) {
		add(match[1])
	}
	return uniqueTerms(terms)
}

func parseRgLine(line string) Evidence {
	parts := strings.SplitN(line, ":", 3)
	if len(parts) < 3 {
		return Evidence{Content: line}
	}
	return Evidence{File: parts[0], Line: parts[1], Content: parts[2]}
}

func normalizeTerms(terms []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		out = append(out, term)
	}
	return out
}

func uniqueTerms(terms []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		out = append(out, term)
	}
	if len(out) > 16 {
		return out[:16]
	}
	return out
}

func isActionableCodeSearchTerm(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[") {
		return false
	}
	switch strings.ToLower(value) {
	case "caller", "level", "msg", "error", "status", "topic", "event_id", "event_name", "aivip_extjson", "service/conversion_event.go:138":
		return false
	}
	return true
}

func isCJKRune(r rune) bool {
	return r >= 0x4e00 && r <= 0x9fff
}
