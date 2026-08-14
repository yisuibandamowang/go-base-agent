package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"go-base-agent/internal/biz/codeqna"
)

type CodeEvidence struct {
	File    string `json:"file"`
	Line    string `json:"line,omitempty"`
	Content string `json:"content"`
}

func searchCodeEvidence(ctx context.Context, repoPath string, service string, req LogSearchRequest, raw map[string]interface{}, maxLines int) []CodeEvidence {
	reqQuestion := strings.TrimSpace(req.Question)
	reqTerms := make([]string, 0, len(req.allKeywords())+len(req.Regexes)+8)
	logLines := extractLogLines(raw)
	for _, line := range logLines {
		addStructuredLogTerms(line, func(term string) {
			reqTerms = append(reqTerms, term)
		})
	}
	for _, keyword := range req.allKeywords() {
		field, _, ok := parseFieldValueKeyword(keyword)
		if ok {
			reqTerms = append(reqTerms, field)
			continue
		}
		reqTerms = append(reqTerms, keyword)
	}
	reqTerms = append(reqTerms, req.Regexes...)
	searchReq := codeqna.SearchRequest{
		RepoPath: codeSearchRootForRequest(repoPath, service, req),
		Question: reqQuestion,
		Terms:    reqTerms,
		MaxLines: maxLines,
	}
	items, err := codeqna.Search(ctx, searchReq)
	if err != nil {
		return nil
	}
	out := make([]CodeEvidence, 0, len(items))
	for _, item := range items {
		out = append(out, CodeEvidence{File: item.File, Line: item.Line, Content: item.Content})
	}
	return out
}

func codeSearchTerms(req LogSearchRequest, raw map[string]interface{}) []string {
	terms := make([]string, 0, len(req.allKeywords())+len(req.Regexes)+8)
	for _, line := range extractLogLines(raw) {
		addStructuredLogTerms(line, func(term string) {
			terms = append(terms, term)
		})
	}
	for _, keyword := range req.allKeywords() {
		field, _, ok := parseFieldValueKeyword(keyword)
		if ok {
			terms = append(terms, field)
			continue
		}
		terms = append(terms, keyword)
	}
	terms = append(terms, req.Regexes...)
	return uniqueCodeSearchTerms(terms)
}

func codeSearchRootForRequest(repoPath string, service string, req LogSearchRequest) string {
	if projectForRequest(req) == logProjectFuyao && (service == "" || service == "all" || service == "webmember") {
		return strings.TrimRight(repoPath, "/") + "/backend/ad_platform_go/webmember"
	}
	if service != "" && service != "all" {
		return strings.TrimRight(repoPath, "/") + "/" + service
	}
	return repoPath
}

func addStructuredLogTerms(line string, add func(string)) {
	for _, fact := range extractLogFacts(line) {
		add(fact.Fields["error"])
		add(fact.Fields["err"])
		if msg := fact.Fields["msg"]; !strings.Contains(msg, "HandleConversionEventQbusMessage") && isActionableCodeSearchTerm(msg) {
			add(msg)
		}
		if strings.Contains(line, "HandleConversionEventQbusMessage") {
			add("HandleConversionEventQbusMessage")
		}
		if caller := fact.Fields["caller"]; strings.Contains(caller, ".go:") || strings.Contains(caller, ".java:") {
			add(strings.TrimSuffix(caller, regexp.MustCompile(`:\d+$`).FindString(caller)))
		}
	}
}

func isGenericCodeSearchToken(token string) bool {
	switch strings.ToLower(strings.TrimSpace(token)) {
	case "caller", "level", "msg", "error", "status", "topic", "event_id", "event_name", "aivip_extjson", "service/conversion_event.go:138":
		return true
	default:
		return false
	}
}

func isActionableCodeSearchTerm(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[") {
		return false
	}
	if isGenericCodeSearchToken(value) {
		return false
	}
	return true
}

func uniqueCodeSearchTerms(terms []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.Trim(strings.TrimSpace(term), `"'，,;；。.`)
		if !isActionableCodeSearchTerm(term) {
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

func extractLogLines(raw map[string]interface{}) []string {
	lines := make([]string, 0)
	var walk func(interface{})
	walk = func(value interface{}) {
		switch typed := value.(type) {
		case map[string]interface{}:
			for key, item := range typed {
				if key == "lines" {
					walk(item)
					continue
				}
				if key == "stdout" || key == "fileLogs" || key == "results" || key == "raw" {
					walk(item)
				}
			}
		case []interface{}:
			for _, item := range typed {
				walk(item)
			}
		case string:
			lines = append(lines, typed)
		}
	}
	walk(raw)
	return lines
}

func parseRgLine(line string) CodeEvidence {
	parts := strings.SplitN(line, ":", 3)
	if len(parts) < 3 {
		return CodeEvidence{Content: line}
	}
	return CodeEvidence{File: parts[0], Line: parts[1], Content: parts[2]}
}

func codeEvidenceText(items []CodeEvidence) string {
	var b strings.Builder
	for _, item := range items {
		b.WriteString(fmt.Sprintf("%s:%s %s\n", item.File, item.Line, item.Content))
	}
	return b.String()
}
