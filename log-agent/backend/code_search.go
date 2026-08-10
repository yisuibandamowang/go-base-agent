package main

import (
	"context"
	"fmt"
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
	for _, term := range terms {
		if len(out) >= maxLines {
			break
		}
		args := []string{"-n", "--fixed-strings", "--max-count", "20", "-g", "*.go", "-g", "*.java", "-g", "*.yaml", "-g", "*.yml", term}
		if service != "" && service != "all" {
			args = append(args, repoPath+"/"+service)
		} else {
			args = append(args, repoPath)
		}
		result, err := exec.CommandContext(searchCtx, "rg", args...).CombinedOutput()
		if err != nil && len(result) == 0 {
			continue
		}
		for _, line := range strings.Split(string(result), "\n") {
			if strings.TrimSpace(line) == "" || len(out) >= maxLines {
				continue
			}
			out = append(out, parseRgLine(line))
		}
	}
	return out
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
	for _, line := range extractLogLines(raw) {
		for _, token := range regexp.MustCompile(`[A-Za-z][A-Za-z0-9_./:-]{5,}`).FindAllString(line, 8) {
			if strings.Contains(token, "/home/log") || strings.Contains(token, "http") {
				continue
			}
			add(strings.Trim(token, `",`))
			if len(out) >= 8 {
				return out
			}
		}
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
