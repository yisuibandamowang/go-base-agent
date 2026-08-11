package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSQLQueryEndpointDisabledByDefault(t *testing.T) {
	router := newRouter(AppConfig{}, stubLogReader{})
	server := httptest.NewServer(router)
	defer server.Close()

	resp, err := http.Post(server.URL+"/api/log-agent/sql/query", "application/json", strings.NewReader(`{"sql":"select 1"}`))
	if err != nil {
		t.Fatalf("post sql query: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "SQL 助手未启用") {
		t.Fatalf("body does not mention disabled sql assistant: %s", string(body))
	}
}

func TestSQLQueryEndpointExecutesReadonlySQLiteQuery(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`create table orders(order_id text, status text); insert into orders(order_id, status) values('order_123', 'failed')`); err != nil {
		t.Fatalf("seed sqlite: %v", err)
	}
	executor := NewSQLExecutor(SQLConfig{Enable: true, Dialect: "sqlite", MaxRows: 50}, db)
	router := newRouterWithSQL(AppConfig{SQL: SQLConfig{Enable: true, Dialect: "sqlite", MaxRows: 50}}, stubLogReader{}, executor)
	server := httptest.NewServer(router)
	defer server.Close()

	resp, err := http.Post(server.URL+"/api/log-agent/sql/query", "application/json", strings.NewReader(`{"table":"orders","filters":{"order_id":"order_123"}}`))
	if err != nil {
		t.Fatalf("post sql query: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, string(body))
	}
	for _, want := range []string{`"sql":"select * from orders where order_id = ? limit 50"`, `"order_id":"order_123"`, `"status":"failed"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body does not contain %q\n%s", want, string(body))
		}
	}
}

func TestDiagnosisStreamIsSeparateEndpoint(t *testing.T) {
	router := newRouter(AppConfig{}, stubLogReader{})
	server := httptest.NewServer(router)
	defer server.Close()

	resp, err := http.Post(server.URL+"/api/log-agent/diagnosis/search/stream", "application/json", strings.NewReader(`{"service":"pay","env":"test2","keywords":["order_123"]}`))
	if err != nil {
		t.Fatalf("post diagnosis stream: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, string(body))
	}
	response := string(body)
	for _, want := range []string{"event:progress", "event:log_result", "event:db_query_result", "SQL 助手未启用"} {
		if !strings.Contains(response, want) {
			t.Fatalf("diagnosis stream does not contain %q\n%s", want, response)
		}
	}
}

func TestLogSearchStreamDoesNotRunSQLWhenExecutorConfigured(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	executor := NewSQLExecutor(SQLConfig{Enable: true, Dialect: "sqlite", MaxRows: 50}, db)
	router := newRouterWithSQL(AppConfig{SQL: SQLConfig{Enable: true, Dialect: "sqlite", MaxRows: 50}}, stubLogReader{}, executor)
	server := httptest.NewServer(router)
	defer server.Close()

	resp, err := http.Post(server.URL+"/api/log-agent/logs/search/stream", "application/json", strings.NewReader(`{"service":"pay","env":"test2","keywords":["order_id=order_123"],"sql_table":"orders"}`))
	if err != nil {
		t.Fatalf("post log stream: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	response := string(body)
	if strings.Contains(response, "db_query_result") || strings.Contains(response, "db_schema_progress") {
		t.Fatalf("log stream should not emit sql events\n%s", response)
	}
	if !strings.Contains(response, "event:log_result") {
		t.Fatalf("log stream does not contain log_result\n%s", response)
	}
}

func TestDiagnosisStreamUsesCodeDatasourceWhenNoFixedDSN(t *testing.T) {
	codeRepoPath := writeSQLModelFixture(t)
	router := newRouterWithSQL(AppConfig{
		SQL: SQLConfig{Enable: true, Dialect: "postgres", MaxRows: 50},
	}, stubLogReader{}, nil)
	server := httptest.NewServer(router)
	defer server.Close()

	body := `{"service":"pay","env":"test2","question":"排查订单下单失败原因","keywords":["order_123"],"code_repo_path":"` + codeRepoPath + `"}`
	resp, err := http.Post(server.URL+"/api/log-agent/diagnosis/search/stream", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post diagnosis stream: %v", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	response := string(respBody)
	if strings.Contains(response, "SQL 助手未启用") {
		t.Fatalf("diagnosis stream should use code datasource path, got disabled\n%s", response)
	}
	if !strings.Contains(response, "未找到代码中的数据库连接配置") {
		t.Fatalf("diagnosis stream does not contain datasource scan error\n%s", response)
	}
}

func TestDiagnosisStreamExecutesSQLWhenEnabled(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`create table orders(order_id text, status text); insert into orders(order_id, status) values('order_123', 'failed')`); err != nil {
		t.Fatalf("seed sqlite: %v", err)
	}
	executor := NewSQLExecutor(SQLConfig{Enable: true, Dialect: "sqlite", MaxRows: 50}, db)
	router := newRouterWithSQL(AppConfig{SQL: SQLConfig{Enable: true, Dialect: "sqlite", MaxRows: 50}}, stubLogReader{}, executor)
	server := httptest.NewServer(router)
	defer server.Close()

	resp, err := http.Post(server.URL+"/api/log-agent/diagnosis/search/stream", "application/json", strings.NewReader(`{"service":"pay","env":"test2","keywords":["order_id=order_123"],"sql_table":"orders"}`))
	if err != nil {
		t.Fatalf("post diagnosis stream: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	response := string(body)
	for _, want := range []string{`event:db_schema_progress`, `"sql":"select * from orders where order_id = ? limit 50"`, `"order_id":"order_123"`, `"status":"failed"`} {
		if !strings.Contains(response, want) {
			t.Fatalf("diagnosis stream does not contain %q\n%s", want, response)
		}
	}
}

func TestDiagnosisStreamInfersSQLTableWhenEnabled(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`create table orders(order_id text, status text); insert into orders(order_id, status) values('order_123', 'failed')`); err != nil {
		t.Fatalf("seed sqlite: %v", err)
	}
	codeRepoPath := writeSQLModelFixture(t)
	executor := NewSQLExecutor(SQLConfig{Enable: true, Dialect: "sqlite", MaxRows: 50}, db)
	router := newRouterWithSQL(AppConfig{SQL: SQLConfig{Enable: true, Dialect: "sqlite", MaxRows: 50}}, stubLogReader{}, executor)
	server := httptest.NewServer(router)
	defer server.Close()

	body := `{"service":"pay","env":"test2","question":"排查订单下单失败原因","keywords":["order_123"],"code_repo_path":"` + codeRepoPath + `"}`
	resp, err := http.Post(server.URL+"/api/log-agent/diagnosis/search/stream", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post diagnosis stream: %v", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	response := string(respBody)
	for _, want := range []string{`event:db_schema_progress`, `"sql":"select * from orders where order_id = ? limit 50"`, `"order_id":"order_123"`, `"status":"failed"`} {
		if !strings.Contains(response, want) {
			t.Fatalf("diagnosis stream does not contain %q\n%s", want, response)
		}
	}
}
