package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type stubLogReader struct{}

func (stubLogReader) Search(ctx context.Context, req LogSearchRequest) (*LogSearchResponse, error) {
	return &LogSearchResponse{
		TraceID: req.TraceID,
		Command: []string{"node", "read_pod_logs.mjs"},
		Summary: LogSearchSummary{
			Target:       "pay / test2",
			FileLogLines: 1,
		},
		Raw: map[string]interface{}{
			"fileLogs": []interface{}{
				map[string]interface{}{
					"file":  "/home/log/pay/pay.log",
					"lines": []interface{}{"ERROR request host access denied!"},
				},
			},
		},
	}, nil
}

func TestLogSearchStreamEmitsProgressAndLogResult(t *testing.T) {
	router := newRouter(AppConfig{}, stubLogReader{})
	server := httptest.NewServer(router)
	defer server.Close()

	body := strings.NewReader(`{"service":"pay","env":"test2","keywords":["request host access denied!"]}`)
	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/log-agent/logs/search/stream", body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read stream response: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, string(respBody))
	}
	if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	response := string(respBody)
	for _, want := range []string{"event:progress", "event:log_result", "request host access denied!"} {
		if !strings.Contains(response, want) {
			t.Fatalf("stream response does not contain %q\n%s", want, response)
		}
	}
}
