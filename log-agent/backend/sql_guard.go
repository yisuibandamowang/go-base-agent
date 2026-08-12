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
	filters := req.Filters
	if len(candidates) > 0 && table == candidates[0].Table {
		filters = alignFiltersWithTableFields(filters, candidates[0].Fields)
	}
	args := make([]interface{}, 0, len(filters))
	where := make([]string, 0, len(filters))
	keys := make([]string, 0, len(filters))
	for key := range filters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !isSafeSQLIdentifier(key) {
			return nil, fmt.Errorf("过滤字段不合法: %s", key)
		}
		where = append(where, key+" = ?")
		args = append(args, filters[key])
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
	contextText := strings.ToLower(description)
	codeContext := collectCodeContext(repoPath, contextText)
	candidatesByTable := map[string]TableCandidate{}
	_ = filepath.WalkDir(repoPath, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			if entry != nil && entry.IsDir() && strings.HasPrefix(entry.Name(), ".") && path != repoPath {
				return filepath.SkipDir
			}
			return nil
		}
		if !isSQLInferenceFile(entry.Name()) {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		text := string(content)
		for _, table := range codeTableNames(path, text) {
			fields := codeColumnNames(text)
			score := tableCandidateScore(table, fields, contextText, codeContext, strings.ToLower(path), strings.ToLower(text), filterKeys)
			if score > 0 {
				mergeTableCandidate(candidatesByTable, TableCandidate{Table: table, File: path, Fields: fields, Score: score})
			}
		}
		return nil
	})
	candidates := make([]TableCandidate, 0, len(candidatesByTable))
	for _, candidate := range candidatesByTable {
		candidates = append(candidates, candidate)
	}
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

func isSQLInferenceFile(name string) bool {
	return strings.HasSuffix(name, ".go") ||
		strings.HasSuffix(name, ".java") ||
		strings.HasSuffix(name, ".xml") ||
		strings.HasSuffix(name, ".sql")
}

func codeTableNames(path string, text string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0)
	add := func(table string) {
		table = strings.TrimSpace(table)
		if table == "" {
			return
		}
		if isIgnoredTableCandidate(table) {
			return
		}
		if _, ok := seen[table]; ok {
			return
		}
		seen[table] = struct{}{}
		out = append(out, table)
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?s)TableName\(\)\s+string\s*\{[^}]*return\s+"([A-Za-z_][A-Za-z0-9_]*)"`),
		regexp.MustCompile(`(?i)\.Table\(\s*["']([A-Za-z_][A-Za-z0-9_]*)["']\s*\)`),
		regexp.MustCompile(`(?m)\b[A-Za-z_][A-Za-z0-9_]*Table\s*=\s*"([A-Za-z_][A-Za-z0-9_]*)"`),
	}
	if shouldInferSQLKeywordTables(path) {
		patterns = append(patterns, regexp.MustCompile(`(?i)\b(?:CREATE\s+TABLE|ALTER\s+TABLE|FROM|JOIN|INTO|UPDATE)\s+`+"`?"+`([A-Za-z_][A-Za-z0-9_]*)`+"`?"))
	}
	for _, pattern := range patterns {
		for _, match := range pattern.FindAllStringSubmatch(text, -1) {
			add(match[1])
		}
	}
	constTables := map[string]string{}
	for _, match := range regexp.MustCompile(`(?m)\b([A-Za-z_][A-Za-z0-9_]*Table)\s*=\s*"([A-Za-z_][A-Za-z0-9_]*)"`).FindAllStringSubmatch(text, -1) {
		constTables[match[1]] = match[2]
	}
	for _, match := range regexp.MustCompile(`(?m)\.Table\(\s*([A-Za-z_][A-Za-z0-9_]*Table)\s*\)`).FindAllStringSubmatch(text, -1) {
		if table := constTables[match[1]]; table != "" {
			add(table)
		}
	}
	return out
}

func isIgnoredTableCandidate(table string) bool {
	switch strings.ToLower(strings.TrimSpace(table)) {
	case "current_timestamp", "null", "default", "primary", "key", "unique", "constraint", "index", "values", "set", "where", "select", "string", "int", "int64", "time":
		return true
	default:
		return false
	}
}

func shouldInferSQLKeywordTables(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".sql") ||
		strings.HasSuffix(lower, ".xml") ||
		strings.HasSuffix(lower, ".java") ||
		strings.HasSuffix(lower, ".properties")
}

func codeColumnNames(text string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0)
	add := func(field string) {
		field = strings.TrimSpace(field)
		if field == "" {
			return
		}
		if _, ok := seen[field]; ok {
			return
		}
		seen[field] = struct{}{}
		out = append(out, field)
	}
	for _, match := range regexp.MustCompile(`column:([A-Za-z_][A-Za-z0-9_]*)`).FindAllStringSubmatch(text, -1) {
		add(match[1])
	}
	for _, match := range regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_]*)`").FindAllStringSubmatch(text, -1) {
		add(match[1])
	}
	sort.Strings(out)
	return out
}

func tableCandidateScore(table string, fields []string, desc string, codeContext string, path string, text string, filterKeys []string) int {
	score := 0
	fieldSet := map[string]struct{}{}
	for _, field := range fields {
		fieldSet[strings.ToLower(strings.TrimSpace(field))] = struct{}{}
	}
	for _, key := range filterKeys {
		normalizedKey := strings.ToLower(strings.TrimSpace(key))
		if normalizedKey == "" {
			continue
		}
		if _, ok := fieldSet[normalizedKey]; ok {
			score += 20
		}
		if normalizedKey == "event_id" {
			if _, ok := fieldSet["kafka_event_id"]; ok {
				score += 60
			}
		}
	}
	tableLower := strings.ToLower(table)
	if strings.Contains(desc, tableLower) {
		score += 3
	}
	if strings.Contains(codeContext, tableLower) {
		score += 6
	}
	score += tokenOverlapScore(tableLower, desc, 2)
	score += tokenOverlapScore(tableLower, codeContext, 4)
	score += tokenOverlapScore(path, desc, 2)
	score += tokenOverlapScore(path, codeContext, 2)
	for _, field := range fields {
		lowerField := strings.ToLower(field)
		if strings.Contains(desc, lowerField) {
			score += 2
		}
		if strings.Contains(codeContext, lowerField) {
			score += 3
		}
		for _, key := range filterKeys {
			if key == lowerField {
				score += 4
			}
			if key == "event_id" && lowerField == "kafka_event_id" {
				score += 5
			}
		}
	}
	for _, token := range []string{"conversion", "report", "monitor", "media", "kafka"} {
		if strings.Contains(desc, token) && strings.Contains(tableLower+" "+path+" "+text, token) {
			score += 2
		}
		if strings.Contains(codeContext, token) && strings.Contains(tableLower+" "+path+" "+text, token) {
			score += 3
		}
	}
	return score
}

func collectCodeContext(repoPath string, desc string) string {
	terms := collectCodeSearchTermsFromDescription(desc)
	seen := map[string]struct{}{}
	var b strings.Builder
	for _, term := range terms {
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		_ = filepath.WalkDir(repoPath, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				if entry != nil && entry.IsDir() && strings.HasPrefix(entry.Name(), ".") && path != repoPath {
					return filepath.SkipDir
				}
				return nil
			}
			if !isSQLInferenceFile(entry.Name()) {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			text := string(content)
			if strings.Contains(strings.ToLower(path), strings.ToLower(term)) || strings.Contains(strings.ToLower(text), strings.ToLower(term)) {
				b.WriteString(path)
				b.WriteByte('\n')
				b.WriteString(text)
				b.WriteByte('\n')
			}
			return nil
		})
		if b.Len() > 200000 {
			break
		}
	}
	return strings.ToLower(b.String())
}

func collectCodeSearchTermsFromDescription(desc string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 12)
	add := func(term string) {
		term = strings.Trim(strings.TrimSpace(term), `"'，,;；。.`)
		if !isUsefulCodeContextTerm(term) {
			return
		}
		if _, ok := seen[term]; ok {
			return
		}
		seen[term] = struct{}{}
		out = append(out, term)
	}
	for _, match := range regexp.MustCompile(`\b[A-Za-z0-9_./-]+\.go:\d+\b`).FindAllString(desc, -1) {
		add(match)
		if idx := strings.LastIndex(match, ":"); idx > 0 {
			add(match[:idx])
		}
	}
	for _, match := range regexp.MustCompile(`\b[A-Z][A-Za-z0-9_]{5,}\b`).FindAllString(desc, -1) {
		add(match)
	}
	for _, match := range regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]{2,40}=([A-Za-z0-9_./:-]{4,})`).FindAllStringSubmatch(desc, -1) {
		add(match[1])
	}
	for _, token := range strings.FieldsFunc(desc, func(r rune) bool {
		return !(r == '_' || r == '-' || r == '/' || r == '.' || r == ':' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	}) {
		add(token)
		if len(out) >= 16 {
			break
		}
	}
	return out
}

func isUsefulCodeContextTerm(term string) bool {
	if len([]rune(term)) < 4 {
		return false
	}
	lower := strings.ToLower(term)
	switch lower {
	case "event_id", "order_id", "status", "topic", "caller", "service", "online", "error", "level", "trace_id", "where", "from", "select", "reported", "failed", "success", "skipped", "duplicate":
		return false
	default:
		return !regexp.MustCompile(`^\d+$`).MatchString(term)
	}
}

func tokenOverlapScore(source string, context string, weight int) int {
	score := 0
	for _, token := range strings.Split(strings.ToLower(source), "_") {
		token = strings.TrimSpace(token)
		if len(token) < 4 {
			continue
		}
		if strings.Contains(context, token) {
			score += weight
		}
	}
	return score
}

func mergeTableCandidate(candidates map[string]TableCandidate, next TableCandidate) {
	current, exists := candidates[next.Table]
	if !exists {
		candidates[next.Table] = next
		return
	}
	current.Score += next.Score
	if current.File == "" {
		current.File = next.File
	}
	current.Fields = appendUniqueStrings(current.Fields, next.Fields...)
	sort.Strings(current.Fields)
	candidates[next.Table] = current
}

func alignFiltersWithTableFields(filters map[string]interface{}, fields []string) map[string]interface{} {
	if len(filters) == 0 || len(fields) == 0 {
		return filters
	}
	fieldSet := map[string]struct{}{}
	for _, field := range fields {
		fieldSet[strings.ToLower(field)] = struct{}{}
	}
	out := make(map[string]interface{}, len(filters))
	for key, value := range filters {
		normalizedKey := strings.ToLower(strings.TrimSpace(key))
		if normalizedKey == "event_id" {
			if _, hasEventID := fieldSet["event_id"]; !hasEventID {
				if _, hasKafkaEventID := fieldSet["kafka_event_id"]; hasKafkaEventID {
					out["kafka_event_id"] = value
					continue
				}
			}
		}
		out[key] = value
	}
	return out
}
