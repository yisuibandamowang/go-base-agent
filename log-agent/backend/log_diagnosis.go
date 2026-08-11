package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

func deterministicLogFindings(logText string) []string {
	lines := strings.Split(logText, "\n")
	out := make([]string, 0, 4)
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
	for _, line := range lines {
		if finding := diagnoseConversionConsumerLine(line); finding != "" {
			add(finding)
		}
		finding := diagnoseConversionEventLine(line)
		if finding != "" {
			add(finding)
		}
	}
	return out
}

func diagnoseConversionConsumerLine(line string) string {
	if strings.Contains(line, "[HandleConversionEventQbusMessage] 消费进入") {
		return "消费进入已确认：日志中已出现 [HandleConversionEventQbusMessage] 消费进入。"
	}
	if !strings.Contains(line, "[HandleConversionEventQbusMessage] handle failed") {
		return ""
	}
	return "消费进入已确认：当前返回的是 [HandleConversionEventQbusMessage] handle failed 日志；结合代码顺序，handle failed 日志只能在消费进入日志之后出现，因此不能判断为“消费进入未检索到”。"
}

func diagnoseConversionEventLine(line string) string {
	if !strings.Contains(line, "conversion event baidu bd_vid or logidurl is empty") {
		return ""
	}
	outer := map[string]interface{}{}
	if err := json.Unmarshal([]byte(line), &outer); err != nil {
		return ""
	}
	event := map[string]interface{}{}
	if msg, ok := outer["msg"].(string); ok {
		_ = json.Unmarshal([]byte(msg), &event)
	}
	aivip := map[string]interface{}{}
	if raw, ok := event["aivip_extjson"].(string); ok {
		_ = json.Unmarshal([]byte(raw), &aivip)
	}
	pcsem, _ := aivip["pcsem"].(map[string]interface{})
	logidurl := stringFromAny(pcsem["logidurl"])
	bdVid := stringFromAny(aivip["bd_vid"])
	if bdVid == "" && logidurl != "" {
		return fmt.Sprintf("直接结论：百度渠道字段校验失败，缺少 bd_vid；logidurl 已存在。qid=%s event_id=%s product=%s medium=%s logidurl=%s bd_vid_present=false logidurl_present=true",
			stringFromAny(event["qid"]),
			firstNonEmpty(stringFromAny(event["event_id"]), stringFromAny(outer["event_id"])),
			stringFromAny(event["product"]),
			firstNonEmpty(stringFromAny(pcsem["medium"]), stringFromAny(outer["medium"])),
			logidurl)
	}
	if bdVid != "" && logidurl == "" {
		return fmt.Sprintf("直接结论：百度渠道字段校验失败，缺少 logidurl；bd_vid 已存在。qid=%s event_id=%s product=%s medium=%s bd_vid_present=true logidurl_present=false",
			stringFromAny(event["qid"]),
			firstNonEmpty(stringFromAny(event["event_id"]), stringFromAny(outer["event_id"])),
			stringFromAny(event["product"]),
			firstNonEmpty(stringFromAny(pcsem["medium"]), stringFromAny(outer["medium"])))
	}
	if bdVid == "" && logidurl == "" {
		return fmt.Sprintf("直接结论：百度渠道字段校验失败，bd_vid 和 logidurl 都缺失。qid=%s event_id=%s product=%s medium=%s bd_vid_present=false logidurl_present=false",
			stringFromAny(event["qid"]),
			firstNonEmpty(stringFromAny(event["event_id"]), stringFromAny(outer["event_id"])),
			stringFromAny(event["product"]),
			firstNonEmpty(stringFromAny(pcsem["medium"]), stringFromAny(outer["medium"])))
	}
	return ""
}

func stringFromAny(value interface{}) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}
