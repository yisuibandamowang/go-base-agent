package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestZhinuoAnalyzerUsesFirstFallbackModel(t *testing.T) {
	var gotModels []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotModel, _ := body["model"].(string)
		gotModels = append(gotModels, gotModel)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"分析结果"}}]}`))
	}))
	defer server.Close()

	analyzer := NewZhinuoAnalyzer(AnalyzerConfig{
		Enable:  true,
		APIKey:  "test-key",
		BaseURL: server.URL + "/v1",
		Model:   "other-model",
	}, nil)

	result, err := analyzer.Analyze(context.Background(), AnalysisInput{
		Question: "为什么失败",
		LogText:  "ERROR PayCenterFailed",
	})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if result.Content != "分析结果" {
		t.Fatalf("Content = %q", result.Content)
	}
	if strings.Join(gotModels, ",") != "codex-ccmax/gpt-5.5" {
		t.Fatalf("models = %#v", gotModels)
	}
}

func TestZhinuoAnalyzerFallsBackToNext360Model(t *testing.T) {
	var gotModels []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotModel, _ := body["model"].(string)
		gotModels = append(gotModels, gotModel)
		if gotModel == "codex-ccmax/gpt-5.5" {
			http.Error(w, `{"error":{"message":"no available channel"}}`, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"降级分析结果"}}]}`))
	}))
	defer server.Close()

	analyzer := NewZhinuoAnalyzer(AnalyzerConfig{
		Enable:  true,
		APIKey:  "test-key",
		BaseURL: server.URL + "/v1",
	}, nil)

	result, err := analyzer.Analyze(context.Background(), AnalysisInput{Question: "为什么失败", LogText: "ERROR"})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if result.Content != "降级分析结果" {
		t.Fatalf("Content = %q", result.Content)
	}
	want := "codex-ccmax/gpt-5.5,deepseek/deepseek-v4-flash-internal"
	if strings.Join(gotModels, ",") != want {
		t.Fatalf("models = %#v, want %s", gotModels, want)
	}
}

func TestZhinuoAnalyzerFallsBackToBailianAfter360ModelsFail(t *testing.T) {
	var gotPaths []string
	var gotModels []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotModel, _ := body["model"].(string)
		gotModels = append(gotModels, gotModel)
		if strings.Contains(r.URL.Path, "/compatible-mode/") {
			if r.Header.Get("Authorization") != "Bearer bailian-key" {
				t.Fatalf("bailian Authorization = %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"百炼兜底结果"}}]}`))
			return
		}
		http.Error(w, `{"error":{"message":"no available channel"}}`, http.StatusBadRequest)
	}))
	defer server.Close()

	analyzer := NewZhinuoAnalyzer(AnalyzerConfig{
		Enable:         true,
		APIKey:         "test-key",
		BaseURL:        server.URL + "/v1",
		BailianAPIKey:  "bailian-key",
		BailianBaseURL: server.URL,
	}, nil)

	result, err := analyzer.Analyze(context.Background(), AnalysisInput{Question: "为什么失败", LogText: "ERROR"})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if result.Content != "百炼兜底结果" {
		t.Fatalf("Content = %q", result.Content)
	}
	if gotModels[len(gotModels)-1] != "qwen3-max" {
		t.Fatalf("last model = %q", gotModels[len(gotModels)-1])
	}
	if !strings.Contains(gotPaths[len(gotPaths)-1], "/compatible-mode/v1/chat/completions") {
		t.Fatalf("last path = %q", gotPaths[len(gotPaths)-1])
	}
}

func TestZhinuoAnalyzerStreamsContentChunks(t *testing.T) {
	var gotModel string
	var gotStream bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotModel, _ = body["model"].(string)
		gotStream, _ = body["stream"].(bool)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"第一段\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"第二段\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	analyzer := NewZhinuoAnalyzer(AnalyzerConfig{
		Enable:  true,
		APIKey:  "test-key",
		BaseURL: server.URL + "/v1",
	}, nil)

	var chunks []string
	result, err := analyzer.AnalyzeStream(context.Background(), AnalysisInput{
		Question: "为什么失败",
		LogText:  "ERROR request host access denied!",
	}, func(chunk string) {
		chunks = append(chunks, chunk)
	})
	if err != nil {
		t.Fatalf("AnalyzeStream() error = %v", err)
	}
	if gotModel != "codex-ccmax/gpt-5.5" {
		t.Fatalf("model = %q", gotModel)
	}
	if !gotStream {
		t.Fatal("stream = false, want true")
	}
	if result.Content != "第一段第二段" {
		t.Fatalf("Content = %q", result.Content)
	}
	if strings.Join(chunks, "") != "第一段第二段" {
		t.Fatalf("chunks = %#v", chunks)
	}
}

func TestBuildAnalysisPromptIncludesQuestionLogsAndCode(t *testing.T) {
	prompt := buildAnalysisPrompt(AnalysisInput{
		Question: "订单为什么失败",
		LogText:  "PayCenterFailed code=9253310473",
		CodeEvidence: []CodeEvidence{{
			File:    "/Users/work_project/360/member/pay/service/pay.go",
			Line:    "88",
			Content: "return PayCenterFailed",
		}},
	})

	for _, want := range []string{"订单为什么失败", "PayCenterFailed", "pay.go:88", "代码链路"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt does not contain %q\n%s", want, prompt)
		}
	}
}

func TestBuildAnalysisPromptIdentifiesMissingBaiduBdVid(t *testing.T) {
	logLine := `{"level":"error","ts":"2026-08-11T11:05:32.921+0800","caller":"service/conversion_event.go:138","msg":"[HandleConversionEventQbusMessage] handle failed","topic":"mkt_conversion_event","msg_len":486,"status":"skipped","event_id":"d9cf1dd4-c7c9-430d-a847-795b98b68da3","event_name":"login","medium":"baidu","error":"conversion event baidu bd_vid or logidurl is empty","msg":"{\"event_id\":\"d9cf1dd4-c7c9-430d-a847-795b98b68da3\",\"busi_type\":\"namiwork\",\"product\":\"纳米Work\",\"event_name\":\"login\",\"event_time\":\"2026-08-11 11:05:32\",\"qid\":\"3649704594\",\"mid\":\"21279144214783757452967613001786\",\"aivip_extjson\":\"{\\\"360ocpc\\\":{\\\"accountid\\\":\\\"86479891\\\"},\\\"pcsem\\\":{\\\"medium\\\":\\\"baidu\\\",\\\"logidurl\\\":\\\"https://work.n.cn/launcher\\\"}}\"}"}`

	prompt := buildAnalysisPrompt(AnalysisInput{
		Question: "查看是否有这个qid的kafka消费进入和消费成功的消息",
		LogText:  logLine,
	})

	for _, want := range []string{
		"确定性日志解析",
		"直接结论：百度渠道字段校验失败，缺少 bd_vid；logidurl 已存在",
		"bd_vid_present=false",
		"logidurl_present=true",
		"https://work.n.cn/launcher",
		"消费进入已确认",
		"handle failed 日志只能在消费进入日志之后出现",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt does not contain %q\n%s", want, prompt)
		}
	}
}

func TestBuildAnalysisPromptIncludesGenericStructuredLogFacts(t *testing.T) {
	logLine := `{"level":"error","ts":"2026-08-11T12:00:01.123+0800","caller":"service/order_pay.go:88","msg":"PayCenterFailed","status":"failed","order_id":"order_123","qihoo_id":"3523031789","error":"balance is not enough","msg":"{\"qid\":\"3523031789\",\"product\":\"超级会员\",\"pay_channel\":\"alipay\",\"detail\":{\"trade_no\":\"trade_456\"}}"}`

	prompt := buildAnalysisPrompt(AnalysisInput{
		Question: "订单为什么支付失败",
		LogText:  logLine,
	})

	for _, want := range []string{
		"确定性日志解析",
		"结构化日志事实",
		"level=error",
		"caller=service/order_pay.go:88",
		"msg=PayCenterFailed",
		"error=balance is not enough",
		"status=failed",
		"order_id=order_123",
		"qihoo_id=3523031789",
		"qid=3523031789",
		"product=超级会员",
		"pay_channel=alipay",
		"trade_no=trade_456",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt does not contain %q\n%s", want, prompt)
		}
	}
}

func TestCodeSearchTermsPreferActionableLogFields(t *testing.T) {
	raw := map[string]interface{}{
		"fileLogs": []interface{}{
			map[string]interface{}{
				"lines": []interface{}{
					`{"level":"error","caller":"service/conversion_event.go:138","msg":"[HandleConversionEventQbusMessage] handle failed","error":"conversion event baidu bd_vid or logidurl is empty"}`,
				},
			},
		},
	}

	terms := codeSearchTerms(LogSearchRequest{}, raw)
	joined := strings.Join(terms, "\n")

	for _, want := range []string{"conversion event baidu bd_vid or logidurl is empty", "HandleConversionEventQbusMessage"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("terms do not contain %q: %#v", want, terms)
		}
	}
	for _, unwanted := range []string{"caller", "level", "msg"} {
		if containsString(terms, unwanted) {
			t.Fatalf("terms contain generic token %q: %#v", unwanted, terms)
		}
	}
}

func TestCodeSearchTermsPrioritizeStructuredErrorOverRequestFields(t *testing.T) {
	raw := map[string]interface{}{
		"fileLogs": []interface{}{
			map[string]interface{}{
				"lines": []interface{}{
					`{"level":"error","caller":"service/conversion_event.go:138","msg":"[HandleConversionEventQbusMessage] handle failed","error":"conversion event baidu bd_vid or logidurl is empty"}`,
				},
			},
		},
	}

	terms := codeSearchTerms(LogSearchRequest{Keywords: []string{"qid=3649704594", "product=纳米Work"}}, raw)

	if len(terms) < 2 {
		t.Fatalf("terms too short: %#v", terms)
	}
	if terms[0] != "conversion event baidu bd_vid or logidurl is empty" {
		t.Fatalf("first term = %q, want structured error first; all terms: %#v", terms[0], terms)
	}
	if terms[1] != "HandleConversionEventQbusMessage" {
		t.Fatalf("second term = %q, want handler second; all terms: %#v", terms[1], terms)
	}
}

func TestCodeSearchTermsUseGenericStructuredLogFacts(t *testing.T) {
	raw := map[string]interface{}{
		"fileLogs": []interface{}{
			map[string]interface{}{
				"lines": []interface{}{
					`{"level":"error","caller":"service/order_pay.go:88","msg":"PayCenterFailed","status":"failed","order_id":"order_123","qid":"3523031789","error":"balance is not enough"}`,
				},
			},
		},
	}

	terms := codeSearchTerms(LogSearchRequest{Keywords: []string{"order_id=order_123"}}, raw)

	if len(terms) < 2 {
		t.Fatalf("terms too short: %#v", terms)
	}
	if terms[0] != "balance is not enough" {
		t.Fatalf("first term = %q, want generic error first; all terms: %#v", terms[0], terms)
	}
	if terms[1] != "PayCenterFailed" {
		t.Fatalf("second term = %q, want log message second; all terms: %#v", terms[1], terms)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
