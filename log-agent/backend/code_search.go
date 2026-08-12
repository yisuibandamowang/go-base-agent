package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type CodeEvidence struct {
	File    string `json:"file"`
	Line    string `json:"line,omitempty"`
	Content string `json:"content"`
}

func searchCodeEvidence(ctx context.Context, repoPath string, service string, req LogSearchRequest, raw map[string]interface{}, maxLines int) []CodeEvidence {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return nil
	}
	terms := codeSearchTerms(req, raw)
	if len(terms) == 0 {
		return nil
	}
	if maxLines <= 0 {
		maxLines = 80
	}
	searchCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out := make([]CodeEvidence, 0, maxLines)
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
			args := []string{"-n", "--fixed-strings", "--max-count", "20", "-g", "*.go", "-g", "*.java", "-g", "*.yaml", "-g", "*.yml", term}
			args = append(args, codeSearchRootForRequest(repoPath, service, req))
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
				nextTerms = append(nextTerms, codeSearchTermsFromEvidence(evidence)...)
				if evidence.File != "" {
					nextTerms = append(nextTerms, codeSearchTermsFromFile(evidence.File)...)
				}
			}
		}
		terms = appendUniqueStrings(nil, nextTerms...)
	}
	return out
}

func codeSearchTermsFromEvidence(evidence CodeEvidence) []string {
	terms := make([]string, 0, 8)
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
	return appendUniqueStrings(nil, terms...)
}

func codeSearchTermsFromFile(filePath string) []string {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	text := string(content)
	terms := make([]string, 0, 12)
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
	return appendUniqueStrings(nil, terms...)
}

func codeSearchTerms(req LogSearchRequest, raw map[string]interface{}) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 8)
	add := func(term string) {
		term = strings.Trim(strings.TrimSpace(term), `"'`)
		if len([]rune(term)) < 4 || regexp.MustCompile(`^\d+$`).MatchString(term) {
			return
		}
		if _, ok := seen[term]; ok {
			return
		}
		seen[term] = struct{}{}
		out = append(out, term)
	}
	logLines := extractLogLines(raw)
	for _, line := range logLines {
		addStructuredLogTerms(line, add)
	}
	for _, keyword := range req.allKeywords() {
		field, _, ok := parseFieldValueKeyword(keyword)
		if ok {
			add(field)
			continue
		}
		add(keyword)
	}
	for _, regexText := range req.Regexes {
		add(regexText)
	}
	for _, line := range logLines {
		for _, token := range regexp.MustCompile(`[A-Za-z][A-Za-z0-9_./:-]{5,}`).FindAllString(line, 8) {
			token = strings.Trim(token, `",`)
			if strings.Contains(token, "/home/log") || strings.Contains(token, "http") {
				continue
			}
			if isGenericCodeSearchToken(token) {
				continue
			}
			add(token)
			if len(out) >= 8 {
				return out
			}
		}
	}
	return out
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
