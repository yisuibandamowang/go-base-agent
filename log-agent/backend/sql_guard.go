package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// SQLQueryRequest describes a read-only SQL query or a table/filter query plan.
type SQLQueryRequest struct {
	TraceID      string                 `json:"trace_id"`
	SQL          string                 `json:"sql"`
	Table        string                 `json:"table"`
	Description  string                 `json:"description"`
	CodeRepoPath string                 `json:"code_repo_path"`
	Columns      []string               `json:"columns"`
	Filters      map[string]interface{} `json:"filters"`
	Limit        int                    `json:"limit"`
}

// SQLPlan is the normalized SQL statement and bind arguments ready to execute.
type SQLPlan struct {
	SQL             string           `json:"sql"`
	Args            []interface{}    `json:"args,omitempty"`
	TableCandidates []TableCandidate `json:"table_candidates,omitempty"`
}

// TableCandidate is a table inferred from code with lightweight matching metadata.
type TableCandidate struct {
	Table  string   `json:"table"`
	File   string   `json:"file,omitempty"`
	Fields []string `json:"fields,omitempty"`
	Score  int      `json:"score"`
}

func validateReadonlySQL(sqlText string) error {
	normalized := strings.TrimSpace(sqlText)
	if normalized == "" {
		return fmt.Errorf("SQL 不能为空")
	}
	trimmed := strings.TrimSuffix(normalized, ";")
	if strings.Contains(trimmed, ";") {
		return fmt.Errorf("只允许单条 SELECT 查询")
	}
	lower := strings.ToLower(strings.TrimSpace(trimmed))
	if !strings.HasPrefix(lower, "select ") {
		return fmt.Errorf("只允许 SELECT 查询")
	}
	if regexp.MustCompile(`\b(insert|update|delete|alter|drop|truncate|create|replace|merge|grant|revoke|call|execute)\b`).MatchString(lower) {
		return fmt.Errorf("SQL 包含非只读关键字")
	}
	return nil
}

func normalizeReadonlySQL(sqlText string, maxRows int) (string, error) {
	if err := validateReadonlySQL(sqlText); err != nil {
		return "", err
	}
	if maxRows <= 0 {
		maxRows = 50
	}
	normalized := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sqlText), ";"))
	if regexp.MustCompile(`(?i)\blimit\s+\d+\b`).MatchString(normalized) {
		return normalized, nil
	}
	return fmt.Sprintf("%s limit %d", normalized, maxRows), nil
}

func planSQL(req SQLQueryRequest, conf SQLConfig) (*SQLPlan, error) {
	if strings.TrimSpace(req.SQL) != "" {
		sqlText, err := normalizeReadonlySQL(req.SQL, maxSQLRows(req.Limit, conf.MaxRows))
		if err != nil {
			return nil, err
		}
		return &SQLPlan{SQL: sqlText}, nil
	}
	table := strings.TrimSpace(req.Table)
	candidates := []TableCandidate(nil)
	if table == "" && strings.TrimSpace(req.CodeRepoPath) != "" {
		candidates = inferTableCandidatesFromCode(req.CodeRepoPath, req.Description, req.Filters)
		if len(candidates) > 0 {
			table = candidates[0].Table
		}
	}
	if !isSafeSQLIdentifier(table) {
		return nil, fmt.Errorf("表名不合法")
	}
	columns := req.Columns
	if len(columns) == 0 {
		columns = []string{"*"}
	}
	columnSQL := make([]string, 0, len(columns))
	for _, column := range columns {
		column = strings.TrimSpace(column)
		if column == "*" {
			columnSQL = append(columnSQL, "*")
			continue
		}
		if !isSafeSQLIdentifier(column) {
			return nil, fmt.Errorf("字段名不合法: %s", column)
		}
		columnSQL = append(columnSQL, column)
	}
	args := make([]interface{}, 0, len(req.Filters))
	where := make([]string, 0, len(req.Filters))
	keys := make([]string, 0, len(req.Filters))
	for key := range req.Filters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !isSafeSQLIdentifier(key) {
			return nil, fmt.Errorf("过滤字段不合法: %s", key)
		}
		where = append(where, key+" = ?")
		args = append(args, req.Filters[key])
	}
	sqlText := fmt.Sprintf("select %s from %s", strings.Join(columnSQL, ", "), table)
	if len(where) > 0 {
		sqlText += " where " + strings.Join(where, " and ")
	}
	sqlText += fmt.Sprintf(" limit %d", maxSQLRows(req.Limit, conf.MaxRows))
	return &SQLPlan{SQL: sqlText, Args: args, TableCandidates: candidates}, nil
}

func isSafeSQLIdentifier(value string) bool {
	return regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`).MatchString(strings.TrimSpace(value))
}

func maxSQLRows(requested int, configured int) int {
	if configured <= 0 {
		configured = 50
	}
	if requested <= 0 || requested > configured {
		return configured
	}
	return requested
}

func inferTableCandidatesFromCode(repoPath string, description string, filters map[string]interface{}) []TableCandidate {
	repoPath = strings.TrimSpace(repoPath)
	info, err := os.Stat(repoPath)
	if err != nil || !info.IsDir() {
		return nil
	}
	filterKeys := make([]string, 0, len(filters))
	for key := range filters {
		filterKeys = append(filterKeys, strings.ToLower(key))
	}
	desc := strings.ToLower(description)
	candidates := make([]TableCandidate, 0)
	_ = filepath.WalkDir(repoPath, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			if entry != nil && entry.IsDir() && strings.HasPrefix(entry.Name(), ".") && path != repoPath {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") && !strings.HasSuffix(entry.Name(), ".java") && !strings.HasSuffix(entry.Name(), ".xml") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		text := string(content)
		for _, match := range regexp.MustCompile(`(?s)TableName\(\)\s+string\s*\{[^}]*return\s+"([A-Za-z_][A-Za-z0-9_]*)"`).FindAllStringSubmatch(text, -1) {
			table := match[1]
			fields := codeColumnNames(text)
			score := tableCandidateScore(table, fields, desc, filterKeys)
			if score > 0 {
				candidates = append(candidates, TableCandidate{Table: table, File: path, Fields: fields, Score: score})
			}
		}
		return nil
	})
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].Table < candidates[j].Table
		}
		return candidates[i].Score > candidates[j].Score
	})
	if len(candidates) > 5 {
		candidates = candidates[:5]
	}
	return candidates
}

func codeColumnNames(text string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, match := range regexp.MustCompile(`column:([A-Za-z_][A-Za-z0-9_]*)`).FindAllStringSubmatch(text, -1) {
		if _, ok := seen[match[1]]; ok {
			continue
		}
		seen[match[1]] = struct{}{}
		out = append(out, match[1])
	}
	sort.Strings(out)
	return out
}

func tableCandidateScore(table string, fields []string, desc string, filterKeys []string) int {
	score := 0
	if strings.Contains(desc, strings.ToLower(table)) {
		score += 3
	}
	for _, field := range fields {
		lowerField := strings.ToLower(field)
		if strings.Contains(desc, lowerField) {
			score += 2
		}
		for _, key := range filterKeys {
			if key == lowerField {
				score += 4
			}
		}
	}
	return score
}
