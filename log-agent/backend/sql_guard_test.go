package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateReadonlySQLRejectsMutationsAndMultiStatements(t *testing.T) {
	for _, sqlText := range []string{
		"delete from orders where id = 1",
		"update orders set status = 'ok'",
		"select * from orders; drop table orders",
		"insert into orders(id) values(1)",
		"truncate table orders",
	} {
		if err := validateReadonlySQL(sqlText); err == nil {
			t.Fatalf("validateReadonlySQL(%q) = nil, want error", sqlText)
		}
	}
}

func TestValidateReadonlySQLAllowsSelectAndAddsLimit(t *testing.T) {
	sqlText, err := normalizeReadonlySQL(" select id, status from orders where id = ? ", 50)
	if err != nil {
		t.Fatalf("normalizeReadonlySQL: %v", err)
	}
	want := "select id, status from orders where id = ? limit 50"
	if sqlText != want {
		t.Fatalf("sql = %q, want %q", sqlText, want)
	}
}

func TestPlanSQLFromTableAndFilters(t *testing.T) {
	plan, err := planSQL(SQLQueryRequest{
		Table:   "orders",
		Columns: []string{"id", "status"},
		Filters: map[string]interface{}{
			"order_id": "order_123",
			"status":   "failed",
		},
		Limit: 20,
	}, SQLConfig{MaxRows: 50})
	if err != nil {
		t.Fatalf("planSQL: %v", err)
	}
	if plan.SQL != "select id, status from orders where order_id = ? and status = ? limit 20" {
		t.Fatalf("sql = %q", plan.SQL)
	}
	if len(plan.Args) != 2 || plan.Args[0] != "order_123" || plan.Args[1] != "failed" {
		t.Fatalf("args = %#v", plan.Args)
	}
}

func TestPlanSQLInfersTableFromCodeRepo(t *testing.T) {
	dir := writeSQLModelFixture(t)

	plan, err := planSQL(SQLQueryRequest{
		Description:  "根据 order_id 查询订单状态",
		CodeRepoPath: dir,
		Columns:      []string{"order_id", "status"},
		Filters: map[string]interface{}{
			"order_id": "order_123",
		},
	}, SQLConfig{MaxRows: 50})
	if err != nil {
		t.Fatalf("planSQL: %v", err)
	}
	want := "select order_id, status from orders where order_id = ? limit 50"
	if plan.SQL != want {
		t.Fatalf("sql = %q, want %q", plan.SQL, want)
	}
	if len(plan.TableCandidates) == 0 || plan.TableCandidates[0].Table != "orders" {
		t.Fatalf("table candidates = %#v", plan.TableCandidates)
	}
}

func TestDiagnosisSQLRequestInfersTableFromCodeRepoAndKeyword(t *testing.T) {
	dir := writeSQLModelFixture(t)

	queryReq := sqlQueryRequestForDiagnosis(LogSearchRequest{
		Question:      "排查订单下单失败原因",
		CodeRepoPath:  dir,
		Keywords:      []string{"order_id=order_123"},
		SQLColumns:    []string{"order_id", "status"},
		BeforeMinutes: 1,
	}, nil, "")
	plan, err := planSQL(queryReq, SQLConfig{MaxRows: 50})
	if err != nil {
		t.Fatalf("planSQL: %v", err)
	}
	if plan.SQL != "select order_id, status from orders where order_id = ? limit 50" {
		t.Fatalf("sql = %q", plan.SQL)
	}
}

func TestDiagnosisSQLRequestUsesStructuredLogFacts(t *testing.T) {
	queryReq := sqlQueryRequestForDiagnosis(LogSearchRequest{
		Question: "排查 kafka 事件上报失败原因",
		SQLTable: "conversion_events",
	}, map[string]interface{}{
		"fileLogs": []interface{}{
			map[string]interface{}{
				"lines": []interface{}{`{"event_id":"event_123","status":"failed","error":"parse args failed"}`},
			},
		},
	}, "")
	plan, err := planSQL(queryReq, SQLConfig{MaxRows: 50})
	if err != nil {
		t.Fatalf("planSQL: %v", err)
	}
	if plan.SQL != "select * from conversion_events where event_id = ? limit 50" {
		t.Fatalf("sql = %q", plan.SQL)
	}
	if len(plan.Args) != 1 || plan.Args[0] != "event_123" {
		t.Fatalf("args = %#v", plan.Args)
	}
}

func TestPlanSQLInfersFuyaoMonitorTableAndKafkaEventIDAlias(t *testing.T) {
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
	model := `package medium_mysql
const AdMediaReportMonitorLogTable = "ad_media_report_monitor_log"
type AdMediaReportMonitorLog struct {
	KafkaEventID string ` + "`gorm:\"column:kafka_event_id\"`" + `
	ReportResult string ` + "`gorm:\"column:report_result\"`" + `
}
func InsertToAdMediaReportMonitorLog(data AdMediaReportMonitorLog) error {
	return db.Table(AdMediaReportMonitorLogTable).Create(&data).Error
}
`
	if err := os.WriteFile(filepath.Join(modelDir, "ad_media_report_monitor_log.go"), []byte(model), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}

	plan, err := planSQL(SQLQueryRequest{
		Description:  "查询这个 kafka event_id 的事件是否出现在日志中并消费成功，在表中的记录是什么 HandleConversionEventQbusMessage ReportWithMonitorRetry",
		CodeRepoPath: dir,
		Filters: map[string]interface{}{
			"event_id": "9017880d-031c-46a0-8fde-e4dace82c38d",
		},
	}, SQLConfig{MaxRows: 50})
	if err != nil {
		t.Fatalf("planSQL: %v", err)
	}
	want := "select * from ad_media_report_monitor_log where kafka_event_id = ? limit 50"
	if plan.SQL != want {
		t.Fatalf("sql = %q, want %q", plan.SQL, want)
	}
	if len(plan.Args) != 1 || plan.Args[0] != "9017880d-031c-46a0-8fde-e4dace82c38d" {
		t.Fatalf("args = %#v", plan.Args)
	}
	if len(plan.TableCandidates) == 0 || plan.TableCandidates[0].Table != "ad_media_report_monitor_log" {
		t.Fatalf("table candidates = %#v", plan.TableCandidates)
	}
}

func TestPlanSQLIgnoresGoParameterNamedFromString(t *testing.T) {
	dir := t.TempDir()
	serviceDir := filepath.Join(dir, "service")
	modelDir := filepath.Join(dir, "model")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		t.Fatalf("mkdir service dir: %v", err)
	}
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatalf("mkdir model dir: %v", err)
	}
	service := `package service
func invalidateBusinessInfoCache(from string) {}
func HandleConversionEventQbusMessage(topic, msg string, msgLen int64) {}
`
	if err := os.WriteFile(filepath.Join(serviceDir, "business_manage.go"), []byte(service), 0o644); err != nil {
		t.Fatalf("write service: %v", err)
	}
	model := `package model
const AdMediaReportMonitorLogTable = "ad_media_report_monitor_log"
type AdMediaReportMonitorLog struct {
	KafkaEventID string ` + "`gorm:\"column:kafka_event_id\"`" + `
}
func InsertToAdMediaReportMonitorLog(data AdMediaReportMonitorLog) error {
	return db.Table(AdMediaReportMonitorLogTable).Create(&data).Error
}
`
	if err := os.WriteFile(filepath.Join(modelDir, "monitor.go"), []byte(model), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}

	plan, err := planSQL(SQLQueryRequest{
		Description:  "HandleConversionEventQbusMessage kafka event_id report monitor",
		CodeRepoPath: dir,
		Filters:      map[string]interface{}{"event_id": "event-1"},
	}, SQLConfig{MaxRows: 50})
	if err != nil {
		t.Fatalf("planSQL: %v", err)
	}
	if strings.Contains(plan.SQL, " from string ") {
		t.Fatalf("sql should not use Go parameter type as table: %s", plan.SQL)
	}
	if plan.SQL != "select * from ad_media_report_monitor_log where kafka_event_id = ? limit 50" {
		t.Fatalf("sql = %q", plan.SQL)
	}
}

func TestScanDatasourceTargetsFromCode(t *testing.T) {
	dir := t.TempDir()
	serviceDir := filepath.Join(dir, "pay")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		t.Fatalf("mkdir service dir: %v", err)
	}
	config := `
spring:
  datasource:
    url: jdbc:postgresql://pg-member.internal:15432/member_pay?sslmode=disable
    username: readonly
    password: secret
`
	if err := os.WriteFile(filepath.Join(serviceDir, "application.yml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	targets := scanDatasourceTargetsFromCode(dir, "pay")
	if len(targets) != 1 {
		t.Fatalf("targets len = %d, want 1: %#v", len(targets), targets)
	}
	target := targets[0]
	if target.Host != "pg-member.internal" || target.Port != 15432 || target.Database != "member_pay" {
		t.Fatalf("target = %#v", target)
	}
	if target.Username != "readonly" || target.Password != "secret" {
		t.Fatalf("target credentials = %#v", target)
	}
}

func TestNewSQLExecutorFromConfigAllowsDiagnosisOnlyWithoutDSN(t *testing.T) {
	executor, err := NewSQLExecutorFromConfig(SQLConfig{Enable: true})
	if err != nil {
		t.Fatalf("NewSQLExecutorFromConfig() error = %v", err)
	}
	if executor != nil {
		t.Fatalf("executor = %#v, want nil without fixed DSN", executor)
	}
}

func writeSQLModelFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeSQLModelFiles(t, dir)
	return dir
}

func writeSQLModelAndDatasourceFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeSQLModelFiles(t, dir)
	serviceDir := filepath.Join(dir, "pay", "src", "main", "resources")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		t.Fatalf("mkdir service config dir: %v", err)
	}
	config := `
spring:
  datasource:
    url: jdbc:postgresql://pg-member.internal:5432/member_pay
    username: readonly
    password: secret
`
	if err := os.WriteFile(filepath.Join(serviceDir, "application.yml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write datasource config: %v", err)
	}
	return dir
}

func writeSQLModelFiles(t *testing.T, dir string) {
	t.Helper()
	modelDir := filepath.Join(dir, "model")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatalf("mkdir model dir: %v", err)
	}
	model := `package model
type Order struct {
	OrderID string ` + "`gorm:\"column:order_id\"`" + `
	Status string ` + "`gorm:\"column:status\"`" + `
}
func (Order) TableName() string { return "orders" }
`
	if err := os.WriteFile(filepath.Join(modelDir, "order.go"), []byte(model), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}
}
