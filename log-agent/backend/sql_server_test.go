package main

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestSQLDriverNameSupportsMySQL(t *testing.T) {
	driver, err := sqlDriverName("mysql")
	if err != nil {
		t.Fatalf("sqlDriverName(mysql): %v", err)
	}
	if driver != "mysql" {
		t.Fatalf("driver = %q, want mysql", driver)
	}
}

func TestDatasourceTargetDSNFromMySQLConfig(t *testing.T) {
	target := DatasourceTarget{
		Dialect:  "mysql",
		Host:     "10.228.132.196",
		Port:     20810,
		Database: "ad_platform",
		Username: "ad_platform_wr",
		Password: "secret",
	}
	got, err := dsnFromDatasourceTarget(target)
	if err != nil {
		t.Fatalf("dsnFromDatasourceTarget: %v", err)
	}
	want := "ad_platform_wr:secret@tcp(10.228.132.196:20810)/ad_platform?charset=utf8mb4&parseTime=true&readTimeout=3s&timeout=3s&writeTimeout=3s"
	if got != want {
		t.Fatalf("dsn = %q, want %q", got, want)
	}
}

func TestScanDatasourceTargetsFromFuyaoMySQLConfig(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "backend", "ad_platform_go", "webmember", "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	config := `
mySQL_ad_platform:
  host_name: "10.228.132.196"
  db_name: "ad_platform"
  user_name: "ad_platform_wr"
  password: "secret"
  port: 20810
`
	if err := os.WriteFile(filepath.Join(configDir, "config.yml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	targets := scanDatasourceTargetsFromCode(dir, "webmember")
	if len(targets) == 0 {
		t.Fatal("targets empty")
	}
	target := targets[0]
	if target.Dialect != "mysql" || target.Host != "10.228.132.196" || target.Port != 20810 || target.Database != "ad_platform" {
		t.Fatalf("target = %#v", target)
	}
	if target.Username != "ad_platform_wr" || target.Password != "secret" {
		t.Fatalf("target credentials = %#v", target)
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

func TestDiagnosisStreamInfersFuyaoMonitorTableFromLogsAndCode(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`create table ad_media_report_monitor_log(kafka_event_id text, report_result text, response text);
insert into ad_media_report_monitor_log(kafka_event_id, report_result, response) values('9017880d-031c-46a0-8fde-e4dace82c38d', 'success', 'reported');`); err != nil {
		t.Fatalf("seed sqlite: %v", err)
	}
	codeRepoPath := writeFuyaoMonitorFixture(t)
	executor := NewSQLExecutor(SQLConfig{Enable: true, Dialect: "sqlite", MaxRows: 50}, db)
	router := newRouterWithSQL(AppConfig{SQL: SQLConfig{Enable: true, Dialect: "sqlite", MaxRows: 50}}, fuyaoConversionLogReader{}, executor)
	server := httptest.NewServer(router)
	defer server.Close()

	body := `{"project":"fuyao","service":"webmember","env":"online","question":"查询这个kafka event_id的事件是否出现在日志中并消费成功，在表中的记录是什么","keywords":["9017880d-031c-46a0-8fde-e4dace82c38d"],"code_repo_path":"` + codeRepoPath + `"}`
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
	for _, want := range []string{
		`event:db_schema_progress`,
		`"sql":"select * from ad_media_report_monitor_log where kafka_event_id = ? limit 50"`,
		`"kafka_event_id":"9017880d-031c-46a0-8fde-e4dace82c38d"`,
		`"report_result":"success"`,
	} {
		if !strings.Contains(response, want) {
			t.Fatalf("diagnosis stream does not contain %q\n%s", want, response)
		}
	}
}

func TestDiagnosisStreamEmitsCodeEvidenceBeforeSQLQuery(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`create table ad_media_report_monitor_log(kafka_event_id text, report_result text);
insert into ad_media_report_monitor_log(kafka_event_id, report_result) values('9017880d-031c-46a0-8fde-e4dace82c38d', 'success');`); err != nil {
		t.Fatalf("seed sqlite: %v", err)
	}
	codeRepoPath := writeFuyaoMonitorFixture(t)
	executor := NewSQLExecutor(SQLConfig{Enable: true, Dialect: "sqlite", MaxRows: 50}, db)
	router := newRouterWithSQL(AppConfig{SQL: SQLConfig{Enable: true, Dialect: "sqlite", MaxRows: 50}}, fuyaoConversionLogReader{}, executor)
	server := httptest.NewServer(router)
	defer server.Close()

	body := `{"project":"fuyao","service":"webmember","env":"online","question":"查询这个kafka event_id的事件是否出现在日志中并消费成功，在表中的记录是什么","keywords":["9017880d-031c-46a0-8fde-e4dace82c38d"],"code_repo_path":"` + codeRepoPath + `"}`
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
	codeEvidenceAt := strings.Index(response, "event:code_evidence")
	dbQueryAt := strings.Index(response, "event:db_query_result")
	dbSchemaAt := strings.Index(response, "event:db_schema_progress")
	if codeEvidenceAt < 0 {
		t.Fatalf("diagnosis stream does not contain code_evidence\n%s", response)
	}
	if dbQueryAt < 0 || dbSchemaAt < 0 {
		t.Fatalf("diagnosis stream does not contain db query events\n%s", response)
	}
	if !(codeEvidenceAt < dbSchemaAt && dbSchemaAt < dbQueryAt) {
		t.Fatalf("event order is wrong: code_evidence=%d db_schema_progress=%d db_query_result=%d\n%s", codeEvidenceAt, dbSchemaAt, dbQueryAt, response)
	}
}

func TestSearchCodeEvidenceFollowsCallerToWritePoint(t *testing.T) {
	dir := writeFuyaoMonitorFixture(t)
	growthModelDir := filepath.Join(dir, "backend", "ad_platform_go", "webmember", "model", "mysql")
	if err := os.MkdirAll(growthModelDir, 0o755); err != nil {
		t.Fatalf("mkdir growth model dir: %v", err)
	}
	growthModel := `package mysql
const GrowthAgentTaskTable = "growth_agent_task"
type GrowthAgentTask struct {
	TaskID string ` + "`gorm:\"column:task_id\"`" + `
	TaskName string ` + "`gorm:\"column:task_name\"`" + `
	Status string ` + "`gorm:\"column:status\"`" + `
}
func (GrowthAgentTask) TableName() string { return GrowthAgentTaskTable }
`
	if err := os.WriteFile(filepath.Join(growthModelDir, "growth_agent.go"), []byte(growthModel), 0o644); err != nil {
		t.Fatalf("write growth model: %v", err)
	}

	raw := map[string]interface{}{
		"fileLogs": []interface{}{
			map[string]interface{}{
				"lines": []interface{}{
					`{"level":"info","ts":"2026-08-12T10:56:47.711+0800","caller":"service/conversion_event.go:122","msg":"[HandleConversionEventQbusMessage] 消费进入","topic":"mkt_conversion_event","event_id":"9017880d-031c-46a0-8fde-e4dace82c38d","event_name":"download","medium":"baidu"}`,
					`{"level":"error","ts":"2026-08-12T10:56:47.885+0800","caller":"service/conversion_event.go:138","msg":"[HandleConversionEventQbusMessage] handle failed","status":"skipped","event_id":"9017880d-031c-46a0-8fde-e4dace82c38d","event_name":"download","medium":"baidu"}`,
				},
			},
		},
	}

	evidence := searchCodeEvidence(context.Background(), dir, "webmember", LogSearchRequest{
		Project:  "fuyao",
		Service:  "webmember",
		Env:      "online",
		Keywords: []string{"9017880d-031c-46a0-8fde-e4dace82c38d"},
	}, raw, 50)
	joined := codeEvidenceText(evidence)
	for _, want := range []string{
		"HandleConversionEventQbusMessage",
		"ReportWithMonitorRetry",
		"InsertToAdMediaReportMonitorLog",
		"AdMediaReportMonitorLogTable",
		"kafka_event_id",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("code evidence does not contain %q\n%s", want, joined)
		}
	}
}

type fuyaoConversionLogReader struct{}

func (fuyaoConversionLogReader) Search(ctx context.Context, req LogSearchRequest) (*LogSearchResponse, error) {
	return &LogSearchResponse{
		TraceID: req.TraceID,
		Command: []string{"node", "k8s_pod_logs.mjs"},
		Summary: LogSearchSummary{
			Target:       "webmember / online",
			FileLogLines: 2,
		},
		Raw: map[string]interface{}{
			"fileLogs": []interface{}{
				map[string]interface{}{
					"file": "/home/log/webmember/webmember.log",
					"lines": []interface{}{
						`{"level":"info","ts":"2026-08-12T10:56:47.711+0800","caller":"service/conversion_event.go:122","msg":"[HandleConversionEventQbusMessage] 消费进入","topic":"mkt_conversion_event","event_id":"9017880d-031c-46a0-8fde-e4dace82c38d","event_name":"download","medium":"baidu"}`,
						`{"level":"info","ts":"2026-08-12T10:56:47.885+0800","caller":"service/conversion_event.go:141","msg":"[HandleConversionEventQbusMessage] handled","status":"reported","event_id":"9017880d-031c-46a0-8fde-e4dace82c38d","event_name":"download","medium":"baidu"}`,
					},
				},
			},
		},
	}, nil
}

func writeFuyaoMonitorFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	serviceDir := filepath.Join(dir, "backend", "ad_platform_go", "webmember", "service")
	modelDir := filepath.Join(dir, "backend", "ad_platform_go", "webmember", "model", "mysql", "medium_mysql")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		t.Fatalf("mkdir service dir: %v", err)
	}
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatalf("mkdir model dir: %v", err)
	}
	service := `package service
func HandleConversionEventQbusMessage(topic, msg string, msgLen int64) {
	result := HandleConversionEventMessage(msg)
	_ = result
}
func reportBaiduConversionEvent() {
	monitorCtx := BuildConversionEventMonitorContext()
	ReportWithMonitorRetry(monitorCtx)
}
`
	if err := os.WriteFile(filepath.Join(serviceDir, "conversion_event.go"), []byte(service), 0o644); err != nil {
		t.Fatalf("write service: %v", err)
	}
	monitorService := `package service
func ReportWithMonitorRetry() {
	runReportWithMonitorRetry(InsertToAdMediaReportMonitorLog)
}
func runReportWithMonitorRetry(insert func(AdMediaReportMonitorLog) error) {}
`
	if err := os.WriteFile(filepath.Join(serviceDir, "media_report_monitor_log.go"), []byte(monitorService), 0o644); err != nil {
		t.Fatalf("write monitor service: %v", err)
	}
	model := `package medium_mysql
const AdMediaReportMonitorLogTable = "ad_media_report_monitor_log"
type AdMediaReportMonitorLog struct {
	KafkaEventID string ` + "`gorm:\"column:kafka_event_id\"`" + `
	ReportResult string ` + "`gorm:\"column:report_result\"`" + `
	Response string ` + "`gorm:\"column:response\"`" + `
}
func InsertToAdMediaReportMonitorLog(data AdMediaReportMonitorLog) error {
	return db.Table(AdMediaReportMonitorLogTable).Create(&data).Error
}
`
	if err := os.WriteFile(filepath.Join(modelDir, "ad_media_report_monitor_log.go"), []byte(model), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}
	return dir
}
