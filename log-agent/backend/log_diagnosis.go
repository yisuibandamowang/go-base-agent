package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type logFact struct {
	Fields map[string]string
	Phases []string
}

var importantLogFields = map[string]struct{}{
	"accountid":   {},
	"bd_vid":      {},
	"busi_type":   {},
	"caller":      {},
	"code":        {},
	"creativeid":  {},
	"err":         {},
	"err_code":    {},
	"errno":       {},
	"error":       {},
	"event_id":    {},
	"event_name":  {},
	"event_time":  {},
	"groupid":     {},
	"level":       {},
	"logidurl":    {},
	"medium":      {},
	"mid":         {},
	"msg":         {},
	"order_id":    {},
	"order_no":    {},
	"pay_channel": {},
	"planid":      {},
	"product":     {},
	"qhclickid":   {},
	"qihoo_id":    {},
	"qid":         {},
	"status":      {},
	"topic":       {},
	"trade_no":    {},
	"trace_id":    {},
	"ts":          {},
	"user_id":     {},
}

func deterministicLogFindings(logText string) []string {
	facts := extractLogFacts(logText)
	out := make([]string, 0, len(facts)+4)
	seen := map[string]struct{}{}
	add := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		if _, ok := seen[text]; ok {
			return
		}
		seen[text] = struct{}{}
		out = append(out, text)
	}
	for _, fact := range facts {
		if text := formatStructuredLogFact(fact); text != "" {
			add(text)
		}
		if finding := diagnoseConversionConsumerFact(fact); finding != "" {
			add(finding)
		}
		if finding := diagnoseConversionEventFact(fact); finding != "" {
			add(finding)
		}
	}
	return out
}

func extractLogFacts(logText string) []logFact {
	lines := strings.Split(logText, "\n")
	out := make([]logFact, 0, len(lines))
	for _, line := range lines {
		fact := parseLogFactLine(line)
		if len(fact.Fields) == 0 && len(fact.Phases) == 0 {
			continue
		}
		out = append(out, fact)
	}
	return out
}

func parseLogFactLine(line string) logFact {
	fact := logFact{Fields: map[string]string{}}
	line = strings.TrimSpace(line)
	if line == "" {
		return fact
	}
	fact.Phases = conversionPhasesFromLine(line)
	if jsonText := jsonObjectText(line); jsonText != "" {
		collectTopLevelStringFields(jsonText, fact.Fields)
		fields := map[string]interface{}{}
		if err := json.Unmarshal([]byte(jsonText), &fields); err == nil {
			collectLogFields(fields, fact.Fields, 0)
		}
	}
	for _, match := range regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_.-]{1,40})=([^\s,，;；]+)`).FindAllStringSubmatch(line, -1) {
		key := normalizeLogFieldKey(match[1])
		if _, ok := importantLogFields[key]; ok {
			addLogField(fact.Fields, key, strings.Trim(match[2], `"'`))
		}
	}
	return fact
}

func collectTopLevelStringFields(jsonText string, out map[string]string) {
	matches := regexp.MustCompile(`"([A-Za-z_][A-Za-z0-9_.-]{1,40})"\s*:\s*"((?:\\.|[^"\\])*)"`).FindAllStringSubmatch(jsonText, -1)
	for _, match := range matches {
		key := normalizeLogFieldKey(match[1])
		if _, ok := importantLogFields[key]; !ok {
			continue
		}
		value, err := strconv.Unquote(`"` + match[2] + `"`)
		if err != nil {
			value = match[2]
		}
		addLogField(out, key, value)
	}
}

func collectLogFields(value interface{}, out map[string]string, depth int) {
	if depth > 5 || value == nil {
		return
	}
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, item := range typed {
			normalized := normalizeLogFieldKey(key)
			if _, ok := importantLogFields[normalized]; ok {
				addLogField(out, normalized, stringFromAny(item))
			}
			collectLogFields(item, out, depth+1)
		}
	case []interface{}:
		for _, item := range typed {
			collectLogFields(item, out, depth+1)
		}
	case string:
		text := strings.TrimSpace(typed)
		if nested := jsonObjectText(text); nested != "" {
			fields := map[string]interface{}{}
			if err := json.Unmarshal([]byte(nested), &fields); err == nil {
				collectLogFields(fields, out, depth+1)
			}
		}
	}
}

func addLogField(fields map[string]string, key string, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	if _, exists := fields[key]; exists {
		return
	}
	fields[key] = value
}

func normalizeLogFieldKey(key string) string {
	key = strings.TrimSpace(key)
	key = strings.ReplaceAll(key, "-", "_")
	key = strings.ToLower(key)
	switch key {
	case "logidurl", "logid_url":
		return "logidurl"
	case "bdvid", "bd_vid":
		return "bd_vid"
	case "errcode":
		return "err_code"
	default:
		return key
	}
}

func jsonObjectText(text string) string {
	text = strings.TrimSpace(text)
	start := strings.IndexByte(text, '{')
	end := strings.LastIndexByte(text, '}')
	if start < 0 || end <= start {
		return ""
	}
	return text[start : end+1]
}

func conversionPhasesFromLine(line string) []string {
	phases := make([]string, 0, 3)
	if strings.Contains(line, "[HandleConversionEventQbusMessage] 消费进入") {
		phases = append(phases, "消费进入")
	}
	if strings.Contains(line, "[HandleConversionEventQbusMessage] handle failed") {
		phases = append(phases, "处理失败")
	}
	if strings.Contains(line, "[HandleConversionEventQbusMessage] 消费成功") || strings.Contains(line, "[HandleConversionEventQbusMessage] handled") {
		phases = append(phases, "消费成功")
	}
	return phases
}

func formatStructuredLogFact(fact logFact) string {
	if len(fact.Fields) == 0 && len(fact.Phases) == 0 {
		return ""
	}
	keys := orderedLogFactKeys(fact.Fields)
	parts := make([]string, 0, len(keys)+1)
	if len(fact.Phases) > 0 {
		parts = append(parts, "phase="+strings.Join(uniqueNonEmptyStrings(fact.Phases), "->"))
	}
	for _, key := range keys {
		parts = append(parts, key+"="+fact.Fields[key])
	}
	return "结构化日志事实：" + strings.Join(parts, " ")
}

func orderedLogFactKeys(fields map[string]string) []string {
	priority := []string{
		"level", "ts", "caller", "msg", "error", "err", "status", "code", "err_code", "errno",
		"topic", "event_id", "event_name", "event_time", "order_id", "order_no", "trade_no",
		"qid", "qihoo_id", "user_id", "mid", "product", "busi_type", "pay_channel", "medium",
		"logidurl", "bd_vid", "trace_id", "accountid", "qhclickid", "planid", "groupid", "creativeid",
	}
	keys := make([]string, 0, len(fields))
	seen := map[string]struct{}{}
	for _, key := range priority {
		if _, ok := fields[key]; ok {
			keys = append(keys, key)
			seen[key] = struct{}{}
		}
	}
	extra := make([]string, 0)
	for key := range fields {
		if _, ok := seen[key]; !ok {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	return append(keys, extra...)
}

func diagnoseConversionConsumerFact(fact logFact) string {
	if containsLogString(fact.Phases, "消费进入") {
		return "消费进入已确认：日志中已出现 [HandleConversionEventQbusMessage] 消费进入。"
	}
	if !containsLogString(fact.Phases, "处理失败") {
		return ""
	}
	return "消费进入已确认：当前返回的是 [HandleConversionEventQbusMessage] handle failed 日志；结合代码顺序，handle failed 日志只能在消费进入日志之后出现，因此不能判断为“消费进入未检索到”。"
}

func diagnoseConversionEventFact(fact logFact) string {
	if !strings.Contains(fact.Fields["error"], "conversion event baidu bd_vid or logidurl is empty") {
		return ""
	}
	logidurl := fact.Fields["logidurl"]
	bdVid := fact.Fields["bd_vid"]
	if bdVid == "" && logidurl != "" {
		return fmt.Sprintf("直接结论：百度渠道字段校验失败，缺少 bd_vid；logidurl 已存在。qid=%s event_id=%s product=%s medium=%s logidurl=%s bd_vid_present=false logidurl_present=true",
			fact.Fields["qid"], fact.Fields["event_id"], fact.Fields["product"], fact.Fields["medium"], logidurl)
	}
	if bdVid != "" && logidurl == "" {
		return fmt.Sprintf("直接结论：百度渠道字段校验失败，缺少 logidurl；bd_vid 已存在。qid=%s event_id=%s product=%s medium=%s bd_vid_present=true logidurl_present=false",
			fact.Fields["qid"], fact.Fields["event_id"], fact.Fields["product"], fact.Fields["medium"])
	}
	if bdVid == "" && logidurl == "" {
		return fmt.Sprintf("直接结论：百度渠道字段校验失败，bd_vid 和 logidurl 都缺失。qid=%s event_id=%s product=%s medium=%s bd_vid_present=false logidurl_present=false",
			fact.Fields["qid"], fact.Fields["event_id"], fact.Fields["product"], fact.Fields["medium"])
	}
	return ""
}

func containsLogString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func stringFromAny(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return ""
	}
}
