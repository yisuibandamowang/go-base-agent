package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/mattn/go-sqlite3"
)

// SQLQueryResponse is the JSON-safe result of a read-only SQL query.
type SQLQueryResponse struct {
	SQL      string                   `json:"sql"`
	Columns  []string                 `json:"columns,omitempty"`
	Rows     []map[string]interface{} `json:"rows,omitempty"`
	RowCount int                      `json:"row_count"`
}

// SQLExecutor runs guarded read-only SQL queries.
type SQLExecutor struct {
	conf SQLConfig
	db   *sql.DB
}

// NewSQLExecutor creates a SQL executor from an existing database handle.
func NewSQLExecutor(conf SQLConfig, db *sql.DB) *SQLExecutor {
	return &SQLExecutor{conf: normalizeSQLConfig(conf), db: db}
}

// NewSQLExecutorFromConfig creates a SQL executor when the assistant is enabled.
func NewSQLExecutorFromConfig(conf SQLConfig) (*SQLExecutor, error) {
	conf = normalizeSQLConfig(conf)
	if !conf.Enable {
		return nil, nil
	}
	if strings.TrimSpace(conf.DSN) == "" {
		return nil, nil
	}
	driver, err := sqlDriverName(conf.Dialect)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(driver, conf.DSN)
	if err != nil {
		return nil, fmt.Errorf("failed to open sql database: %w", err)
	}
	db.SetMaxOpenConns(3)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(5 * time.Minute)
	return NewSQLExecutor(conf, db), nil
}

func normalizeSQLConfig(conf SQLConfig) SQLConfig {
	conf.Dialect = strings.TrimSpace(strings.ToLower(conf.Dialect))
	if conf.Dialect == "" {
		conf.Dialect = "postgres"
	}
	if conf.Timeout <= 0 {
		conf.Timeout = 3 * time.Second
	}
	if conf.MaxRows <= 0 {
		conf.MaxRows = 50
	}
	return conf
}

func sqlDriverName(dialect string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(dialect)) {
	case "postgres", "postgresql", "pg":
		return "pgx", nil
	case "sqlite", "sqlite3":
		return "sqlite3", nil
	default:
		return "", fmt.Errorf("不支持的 SQL 方言: %s", dialect)
	}
}

// Enabled reports whether the SQL executor can run queries.
func (e *SQLExecutor) Enabled() bool {
	return e != nil && e.conf.Enable && e.db != nil
}

// Query executes a guarded read-only SQL query with the configured timeout.
func (e *SQLExecutor) Query(ctx context.Context, req SQLQueryRequest) (*SQLQueryResponse, error) {
	if !e.Enabled() {
		return nil, fmt.Errorf("SQL 助手未启用")
	}
	plan, err := planSQL(req, e.conf)
	if err != nil {
		return nil, err
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, e.conf.Timeout)
	defer cancel()
	slog.Info("sql query started", "trace_id", req.TraceID, "sql", plan.SQL)
	rows, err := e.db.QueryContext(timeoutCtx, plan.SQL, plan.Args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("failed to read sql columns: %w", err)
	}
	outRows := make([]map[string]interface{}, 0)
	for rows.Next() {
		values := make([]interface{}, len(columns))
		pointers := make([]interface{}, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, fmt.Errorf("failed to scan sql row: %w", err)
		}
		row := make(map[string]interface{}, len(columns))
		for i, column := range columns {
			row[column] = sqlValueForJSON(values[i])
		}
		outRows = append(outRows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate sql rows: %w", err)
	}
	slog.Info("sql query completed", "trace_id", req.TraceID, "row_count", len(outRows))
	return &SQLQueryResponse{
		SQL:      plan.SQL,
		Columns:  columns,
		Rows:     outRows,
		RowCount: len(outRows),
	}, nil
}

func queryDiagnosisSQL(ctx context.Context, project string, service string, conf SQLConfig, staticExecutor *SQLExecutor, req SQLQueryRequest) (*SQLQueryResponse, error) {
	if staticExecutor != nil && staticExecutor.Enabled() {
		return staticExecutor.Query(ctx, req)
	}
	targets := scanDatasourceTargetsFromCode(req.CodeRepoPath, service)
	if len(targets) == 0 {
		return nil, fmt.Errorf("未找到代码中的数据库连接配置")
	}
	target := targets[0]
	profile := conf.SSHProfiles[project]
	if profile.Enable {
		return queryDatasourceTargetViaSSH(ctx, conf, profile, target, req)
	}
	return queryDatasourceTarget(ctx, conf, target, req)
}

func queryDatasourceTarget(ctx context.Context, conf SQLConfig, target DatasourceTarget, req SQLQueryRequest) (*SQLQueryResponse, error) {
	driver, err := sqlDriverName(firstNonEmpty(target.Dialect, conf.Dialect))
	if err != nil {
		return nil, err
	}
	dsn := postgresDSNFromDatasourceTarget(target)
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sql database: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return NewSQLExecutor(conf, db).Query(ctx, req)
}

func queryDatasourceTargetViaSSH(ctx context.Context, conf SQLConfig, profile SSHProfileConfig, target DatasourceTarget, req SQLQueryRequest) (*SQLQueryResponse, error) {
	var result *SQLQueryResponse
	err := withSSHTunnel(ctx, profile, target.Host, target.Port, func(localHost string, localPort int) error {
		localTarget := target
		localTarget.Host = localHost
		localTarget.Port = localPort
		queryResult, err := queryDatasourceTarget(ctx, conf, localTarget, req)
		if err != nil {
			return err
		}
		result = queryResult
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func postgresDSNFromDatasourceTarget(target DatasourceTarget) string {
	u := url.URL{
		Scheme: "postgres",
		Host:   net.JoinHostPort(target.Host, strconv.Itoa(target.Port)),
		Path:   "/" + strings.TrimPrefix(target.Database, "/"),
	}
	if target.Username != "" || target.Password != "" {
		u.User = url.UserPassword(target.Username, target.Password)
	}
	values := url.Values{}
	values.Set("sslmode", "disable")
	u.RawQuery = values.Encode()
	return u.String()
}

func sqlValueForJSON(value interface{}) interface{} {
	switch typed := value.(type) {
	case []byte:
		return string(typed)
	default:
		return typed
	}
}
