package parser

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMinerUClient_RequestUploadQueryAndDownload(t *testing.T) {
	zipBytes := makeTestZip(t, map[string]string{
		"result.md": "# 标题\n\n正文",
	})
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file-urls/batch":
			if got := r.Header.Get("Authorization"); got != "Bearer token" {
				t.Fatalf("unexpected auth header: %q", got)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			files, ok := body["files"].([]any)
			if !ok || len(files) != 1 {
				t.Fatalf("unexpected files payload: %+v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"batch_id":"batch-1","file_urls":["` + server.URL + `/upload-1"]}}`))
		case "/extract-results/batch/batch-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"extract_result":[{"state":"SUCCEEDED","full_zip_url":"` + server.URL + `/zip-1","err_msg":""}]}}`))
		case "/zip-1":
			_, _ = w.Write(zipBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewMinerUClient(server.URL, "token", server.Client())
	ticket, err := client.requestUpload(context.Background(), minerUSubmitRequest{
		FileName:      "会员文档.pdf",
		DataID:        "doc-1",
		OCR:           false,
		EnableTable:   true,
		EnableFormula: true,
		Language:      "ch",
	})
	if err != nil {
		t.Fatalf("request upload: %v", err)
	}
	if ticket.BatchID != "batch-1" || ticket.UploadURL != server.URL+"/upload-1" {
		t.Fatalf("unexpected ticket: %+v", ticket)
	}

	status, err := client.queryResult(context.Background(), ticket.BatchID)
	if err != nil {
		t.Fatalf("query result: %v", err)
	}
	if status.State != minerUStateSucceeded || status.ZipURL != server.URL+"/zip-1" {
		t.Fatalf("unexpected status: %+v", status)
	}

	downloaded, err := client.downloadZip(context.Background(), status.ZipURL)
	if err != nil {
		t.Fatalf("download zip: %v", err)
	}
	if !bytes.Equal(downloaded, zipBytes) {
		t.Fatalf("downloaded zip mismatch")
	}
}

func makeTestZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}
