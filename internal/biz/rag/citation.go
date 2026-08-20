package rag

import (
	"regexp"
	"strconv"
	"strings"
)

var inlineCitationPattern = regexp.MustCompile(`\[[1-9]\d*\]\(#cite-[1-9]\d*\)`)
var citationContentTagPattern = regexp.MustCompile(`<content([^>]*) data-ragent-doc-id="([^"]*)"([^>]*)>`)

// EnrichCitationContext 将知识库资料块的内部文档标记替换为本次请求的来源编号。
// 未匹配到来源的文档只移除内部标记，避免 docID 泄露给模型。
func EnrichCitationContext(kbContext string, sources []SourceRef, enabled bool) string {
	if strings.TrimSpace(kbContext) == "" {
		return kbContext
	}

	indexByDocID := make(map[string]int, len(sources))
	if enabled {
		for _, source := range sources {
			docID := strings.TrimSpace(source.DocID)
			if docID == "" || source.Index <= 0 {
				continue
			}
			if _, exists := indexByDocID[docID]; !exists {
				indexByDocID[docID] = source.Index
			}
		}
	}

	return citationContentTagPattern.ReplaceAllStringFunc(kbContext, func(tag string) string {
		matches := citationContentTagPattern.FindStringSubmatch(tag)
		if len(matches) != 4 {
			return tag
		}
		attrs := strings.TrimSpace(strings.TrimSpace(matches[1]) + " " + strings.TrimSpace(matches[3]))
		if index, ok := indexByDocID[matches[2]]; ok {
			attrs = strings.TrimSpace(attrs + ` ref="` + strconv.Itoa(index) + `"`)
		}
		if attrs == "" {
			return "<content>"
		}
		return "<content " + attrs + ">"
	})
}

// StripInlineCitations 移除回答中的请求级行内引用标记，避免旧编号污染下一轮模型上下文。
func StripInlineCitations(content string) string {
	if strings.TrimSpace(content) == "" {
		return ""
	}
	return inlineCitationPattern.ReplaceAllString(content, "")
}
