package main

import (
	"os"
	"path/filepath"
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
