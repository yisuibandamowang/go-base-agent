package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultMemberK8SAppID = "1586"
const defaultFuyaoK8SAppID = "5658"

const (
	logProjectMember = "member"
	logProjectFuyao  = "fuyao"
)

type LogSearchRequest struct {
	TraceID         string                 `json:"trace_id"`
	Question        string                 `json:"question"`
	Project         string                 `json:"project"`
	AppID           string                 `json:"app_id"`
	Service         string                 `json:"service"`
	Env             string                 `json:"env"`
	Pod             string                 `json:"pod"`
	Cluster         string                 `json:"cluster"`
	Container       string                 `json:"container"`
	Deployment      string                 `json:"deployment"`
	ImageTag        string                 `json:"image_tag"`
	ImageRegex      string                 `json:"image_regex"`
	At              string                 `json:"at"`
	BeforeMinutes   int                    `json:"before_minutes"`
	AfterMinutes    int                    `json:"after_minutes"`
	Keyword         string                 `json:"keyword"`
	Keywords        []string               `json:"keywords"`
	Regexes         []string               `json:"regexes"`
	LogFiles        []string               `json:"log_files"`
	IncludeCritical bool                   `json:"include_critical"`
	IncludeGz       bool                   `json:"include_gz"`
	GzLimit         int                    `json:"gz_limit"`
	AllPods         bool                   `json:"all_pods"`
	PodLimit        int                    `json:"pod_limit"`
	ResolveOnly     bool                   `json:"resolve_only"`
	StdoutOnly      bool                   `json:"stdout_only"`
	FileOnly        bool                   `json:"file_only"`
	MaxLines        int                    `json:"max_lines"`
	MaxStdoutLines  int                    `json:"max_stdout_lines"`
	MaxLineChars    int                    `json:"max_line_chars"`
	CodeRepoPath    string                 `json:"code_repo_path"`
	SQL             string                 `json:"sql"`
	SQLTable        string                 `json:"sql_table"`
	SQLColumns      []string               `json:"sql_columns"`
	SQLLimit        int                    `json:"sql_limit"`
	SQLFilters      map[string]interface{} `json:"sql_filters"`
}

type LogSearchResponse struct {
	TraceID  string                 `json:"trace_id"`
	Command  []string               `json:"command"`
	Summary  LogSearchSummary       `json:"summary"`
	Analysis *AnalysisResult        `json:"analysis,omitempty"`
	Raw      map[string]interface{} `json:"raw"`
}

type LogSearchSummary struct {
	Target       string   `json:"target"`
	LogFiles     []string `json:"log_files"`
	StdoutLines  int      `json:"stdout_lines"`
	FileLogLines int      `json:"file_log_lines"`
	JobCount     int      `json:"job_count"`
	Errors       []string `json:"errors,omitempty"`
}

type LogReader interface {
	Search(ctx context.Context, req LogSearchRequest) (*LogSearchResponse, error)
}

type ScriptLogReader struct {
	conf LogReaderConfig
}

func NewScriptLogReader(conf LogReaderConfig) *ScriptLogReader {
	return &ScriptLogReader{conf: conf}
}

func (r *ScriptLogReader) Search(ctx context.Context, req LogSearchRequest) (*LogSearchResponse, error) {
	slog.Info("log search request received", "trace_id", req.TraceID, "service", req.Service, "env", req.Env, "deployment", req.Deployment, "pod", req.Pod, "resolve_only", req.ResolveOnly)
	jobs, err := buildSearchJobs(req, r.conf)
	if err != nil {
		slog.Error("log search build jobs failed", "trace_id", req.TraceID, "err", err)
		return nil, err
	}
	slog.Info("log search jobs built", "trace_id", req.TraceID, "job_count", len(jobs))
	if len(jobs) == 1 {
		return r.searchOne(ctx, jobs[0])
	}
	return r.searchBatch(ctx, req.TraceID, jobs)
}

func (r *ScriptLogReader) searchBatch(ctx context.Context, traceID string, jobs []LogSearchRequest) (*LogSearchResponse, error) {
	return runSearchBatch(ctx, traceID, jobs, r.conf.MaxConcurrency, r.searchOne)
}

func runSearchBatch(ctx context.Context, traceID string, jobs []LogSearchRequest, maxConcurrency int, search func(context.Context, LogSearchRequest) (*LogSearchResponse, error)) (*LogSearchResponse, error) {
	if maxConcurrency <= 0 {
		maxConcurrency = 4
	}
	if maxConcurrency > len(jobs) && len(jobs) > 0 {
		maxConcurrency = len(jobs)
	}
	results := make([]interface{}, len(jobs))
	responses := make([]*LogSearchResponse, len(jobs))
	errors := make([]error, len(jobs))
	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup
	for i, job := range jobs {
		i, job := i, job
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				errors[i] = ctx.Err()
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()
			slog.Info("log search batch job started", "trace_id", traceID, "service", job.Service, "env", job.Env)
			resp, err := search(ctx, job)
			if err != nil {
				slog.Error("log search batch job failed", "trace_id", traceID, "service", job.Service, "env", job.Env, "err", err)
				errors[i] = err
				return
			}
			slog.Info("log search batch job completed", "trace_id", traceID, "service", job.Service, "env", job.Env, "stdout_lines", resp.Summary.StdoutLines, "file_log_lines", resp.Summary.FileLogLines)
			responses[i] = resp
		}()
	}
	wg.Wait()
	summary := LogSearchSummary{Target: "batch", JobCount: len(jobs)}
	commands := make([]string, 0, len(jobs))
	for i, job := range jobs {
		if errors[i] != nil {
			errText := errors[i].Error()
			summary.Errors = append(summary.Errors, job.Env+"/"+job.Service+": "+errText)
			results[i] = map[string]interface{}{
				"env":     job.Env,
				"service": job.Service,
				"error":   errText,
			}
			continue
		}
		resp := responses[i]
		if resp == nil {
			continue
		}
		summary.StdoutLines += resp.Summary.StdoutLines
		summary.FileLogLines += resp.Summary.FileLogLines
		summary.LogFiles = appendUniqueStrings(summary.LogFiles, resp.Summary.LogFiles...)
		summary.Errors = append(summary.Errors, resp.Summary.Errors...)
		commands = append(commands, strings.Join(resp.Command, " "))
		results[i] = map[string]interface{}{
			"env":     job.Env,
			"service": job.Service,
			"summary": resp.Summary,
			"raw":     resp.Raw,
		}
	}
	slog.Info("log search batch completed", "trace_id", traceID, "job_count", len(jobs), "stdout_lines", summary.StdoutLines, "file_log_lines", summary.FileLogLines, "error_count", len(summary.Errors))
	return &LogSearchResponse{
		TraceID: traceID,
		Command: commands,
		Summary: summary,
		Raw: map[string]interface{}{
			"batch":   true,
			"results": results,
		},
	}, nil
}

func (r *ScriptLogReader) searchOne(ctx context.Context, req LogSearchRequest) (*LogSearchResponse, error) {
	args, err := r.buildCommandArgs(req)
	if err != nil {
		return nil, err
	}
	timeout := r.conf.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	nodePath := strings.TrimSpace(r.conf.NodePath)
	if nodePath == "" {
		nodePath = "node"
	}
	cmd := exec.CommandContext(runCtx, nodePath, args...)
	cmd.Env = commandEnv(os.Environ(), req)
	cmd.Dir = r.commandDir(req)
	slog.Info("log helper started", "trace_id", req.TraceID, "project", projectForRequest(req), "service", req.Service, "env", req.Env, "app_id", commandAppID(req), "work_dir", cmd.Dir, "max_duration", timeout.String())
	output, err := cmd.CombinedOutput()
	if runCtx.Err() != nil {
		if errors.Is(runCtx.Err(), context.Canceled) {
			slog.Info("log helper canceled", "trace_id", req.TraceID, "service", req.Service, "env", req.Env)
			return nil, fmt.Errorf("failed to read k8s logs: %w", runCtx.Err())
		}
		slog.Error("log helper timeout", "trace_id", req.TraceID, "service", req.Service, "env", req.Env, "err", runCtx.Err())
		return nil, fmt.Errorf("failed to read k8s logs: %w", runCtx.Err())
	}
	if err != nil {
		slog.Error("log helper failed", "trace_id", req.TraceID, "service", req.Service, "env", req.Env, "err", err)
		return nil, fmt.Errorf("failed to run log helper: %w: %s", err, compactText(string(output), 2000))
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(output, &raw); err != nil {
		slog.Error("log helper output decode failed", "trace_id", req.TraceID, "service", req.Service, "env", req.Env, "err", err)
		return nil, fmt.Errorf("failed to decode log helper output: %w", err)
	}
	filterLogOutputByRequest(raw, req)
	summary := summarizeLogOutput(raw)
	slog.Info("log helper completed", "trace_id", req.TraceID, "service", req.Service, "env", req.Env, "stdout_lines", summary.StdoutLines, "file_log_lines", summary.FileLogLines, "error_count", len(summary.Errors))
	return &LogSearchResponse{
		TraceID: req.TraceID,
		Command: append([]string{nodePath}, args...),
		Summary: summary,
		Raw:     raw,
	}, nil
}

func buildSearchJobs(req LogSearchRequest, conf LogReaderConfig) ([]LogSearchRequest, error) {
	req.normalize()
	if isAll(req.Pod) {
		req.Pod = ""
		req.AllPods = true
	}
	if isAll(req.Deployment) {
		req.Deployment = ""
		req.AllPods = true
	}
	envs := []string{req.Env}
	if isAll(req.Env) {
		req.Env = ""
		req.AllPods = true
		if req.Deployment != "" || req.Pod != "" {
			envs = []string{""}
		} else if projectForRequest(req) == logProjectFuyao {
			envs = []string{"test", "regress", "online"}
		} else {
			envs = append([]string{}, conf.AllowedEnvs...)
		}
	}
	services := []string{req.Service}
	if isAll(req.Service) {
		req.Service = ""
		req.AllPods = true
		if req.Deployment != "" || req.Pod != "" {
			services = []string{""}
		} else if projectForRequest(req) == logProjectFuyao {
			services = []string{"webmember"}
		} else {
			services = servicesFromConfig(conf)
		}
	}
	if len(envs) == 0 || envs[0] == "" {
		envs = []string{""}
	}
	if len(services) == 0 || services[0] == "" {
		services = []string{""}
	}
	broad := len(envs)*len(services) > 1 || req.AllPods
	if broad && !req.ResolveOnly && len(req.remoteKeywords()) == 0 && len(req.Regexes) == 0 {
		return nil, fmt.Errorf("failed to build log helper args: broad search requires keyword or regex")
	}
	jobs := make([]LogSearchRequest, 0, len(envs)*len(services))
	for _, env := range envs {
		for _, service := range services {
			job := req
			job.Env = env
			job.Service = service
			fillDefaultFuyaoDeployment(&job)
			if err := NewScriptLogReader(conf).validate(job); err != nil {
				return nil, err
			}
			jobs = append(jobs, job)
		}
	}
	return jobs, nil
}

func (r *ScriptLogReader) buildCommandArgs(req LogSearchRequest) ([]string, error) {
	scriptPath := r.scriptPath(req)
	if scriptPath == "" {
		return nil, fmt.Errorf("failed to build log helper args: script path is empty")
	}
	req.normalize()
	fillDefaultFuyaoDeployment(&req)
	if err := r.validate(req); err != nil {
		return nil, err
	}

	args := []string{scriptPath}
	appendPair := func(flag, value string) {
		if strings.TrimSpace(value) != "" {
			args = append(args, flag, strings.TrimSpace(value))
		}
	}
	appendInt := func(flag string, value int) {
		if value > 0 {
			args = append(args, flag, strconv.Itoa(value))
		}
	}
	appendPair("--cluster", req.Cluster)
	appendPair("--pod", req.Pod)
	appendPair("--container", req.Container)
	appendPair("--deployment", req.Deployment)
	appendPair("--service", req.Service)
	appendPair("--env", req.Env)
	if projectForRequest(req) == logProjectFuyao {
		appendPair("--app-id", commandAppID(req))
	}
	appendPair("--image-tag", req.ImageTag)
	appendPair("--image-regex", req.ImageRegex)
	appendPair("--at", req.At)
	appendInt("--before-minutes", req.BeforeMinutes)
	appendInt("--after-minutes", req.AfterMinutes)
	for _, keyword := range req.remoteKeywords() {
		appendPair("--keyword", keyword)
	}
	for _, regex := range req.allRegexes() {
		appendPair("--regex", regex)
	}
	for _, logFile := range logFilesForRequest(req) {
		appendPair("--log-file", logFile)
	}
	if req.IncludeCritical {
		args = append(args, "--include-critical")
	}
	if req.IncludeGz {
		args = append(args, "--include-gz")
		appendInt("--gz-limit", req.GzLimit)
	}
	if req.AllPods {
		args = append(args, "--all-pods")
		appendInt("--pod-limit", req.PodLimit)
	}
	if req.ResolveOnly {
		args = append(args, "--resolve-only")
	}
	if req.StdoutOnly {
		args = append(args, "--stdout-only")
	}
	if req.FileOnly {
		args = append(args, "--file-only")
	}
	appendInt("--max-lines", firstPositive(req.MaxLines, r.conf.MaxLines))
	appendInt("--max-stdout-lines", firstPositive(req.MaxStdoutLines, r.conf.MaxStdoutLines))
	appendInt("--max-line-chars", firstPositive(req.MaxLineChars, r.conf.MaxLineChars))
	return args, nil
}

func (r *ScriptLogReader) scriptPath(req LogSearchRequest) string {
	if projectForRequest(req) == logProjectFuyao {
		return strings.TrimSpace(r.conf.FuyaoScriptPath)
	}
	return strings.TrimSpace(r.conf.ScriptPath)
}

func (r *ScriptLogReader) commandDir(req LogSearchRequest) string {
	if projectForRequest(req) == logProjectFuyao {
		return strings.TrimSpace(r.conf.FuyaoWorkDir)
	}
	return ""
}

func fillDefaultFuyaoDeployment(req *LogSearchRequest) {
	if req == nil || projectForRequest(*req) != logProjectFuyao {
		return
	}
	if strings.TrimSpace(req.Service) == "" {
		req.Service = fuyaoServiceForDeployment(req.Deployment)
	}
	if strings.TrimSpace(req.Deployment) == "" {
		req.Deployment = fuyaoDeploymentForServiceEnv(req.Service, req.Env)
	}
	if fuyaoServiceForRequest(*req) == "webmember" && strings.TrimSpace(req.Pod) == "" {
		req.AllPods = true
	}
}

func fuyaoServiceForDeployment(deployment string) string {
	deployment = strings.TrimSpace(deployment)
	if deployment == "ad-platform-test" || deployment == "ad-platform-regress" || deployment == "ad-platform-online" {
		return "webmember"
	}
	if strings.HasPrefix(deployment, "ad-platform-fuyao-agent-backend-") {
		return "fuyao-agent-backend"
	}
	return ""
}

func fuyaoDeploymentForServiceEnv(service string, env string) string {
	service = strings.TrimSpace(service)
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "test":
		if service == "webmember" {
			return "ad-platform-test"
		}
		if service == "fuyao-agent-backend" {
			return "ad-platform-fuyao-agent-backend-test"
		}
	case "regress", "regression":
		if service == "webmember" {
			return "ad-platform-regress"
		}
		if service == "fuyao-agent-backend" {
			return "ad-platform-fuyao-agent-backend-regress"
		}
	case "online":
		if service == "webmember" {
			return "ad-platform-online"
		}
		if service == "fuyao-agent-backend" {
			return "ad-platform-fuyao-agent-backend-online"
		}
	}
	return ""
}

func logFilesForRequest(req LogSearchRequest) []string {
	if len(req.LogFiles) > 0 {
		return req.LogFiles
	}
	if projectForRequest(req) == logProjectFuyao && fuyaoServiceForRequest(req) == "webmember" {
		return []string{"/home/log/webmember/webmember.log"}
	}
	return nil
}

func fuyaoServiceForRequest(req LogSearchRequest) string {
	if strings.TrimSpace(req.Service) != "" {
		return strings.TrimSpace(req.Service)
	}
	return fuyaoServiceForDeployment(req.Deployment)
}

func (r *ScriptLogReader) validate(req LogSearchRequest) error {
	if projectForRequest(req) == "" {
		return fmt.Errorf("failed to build log helper args: unsupported project %q", req.Project)
	}
	if req.AppID != "" && !regexp.MustCompile(`^[0-9]+$`).MatchString(req.AppID) {
		return fmt.Errorf("failed to build log helper args: app_id must be numeric")
	}
	if req.Pod == "" && req.Deployment == "" && (req.Service == "" || req.Env == "") {
		return fmt.Errorf("failed to build log helper args: provide pod, deployment, or service+env")
	}
	if req.Service != "" && req.Env == "" && req.Pod == "" && req.Deployment == "" {
		return fmt.Errorf("failed to build log helper args: env is required when service is provided")
	}
	if req.Env != "" && projectForRequest(req) == logProjectFuyao && !containsFold([]string{"test", "regress", "regression", "online"}, req.Env) {
		return fmt.Errorf("failed to build log helper args: unsupported fuyao env %q", req.Env)
	}
	if req.Env != "" && projectForRequest(req) != logProjectFuyao && !containsFold(r.conf.AllowedEnvs, req.Env) {
		return fmt.Errorf("failed to build log helper args: unsupported env %q", req.Env)
	}
	if req.StdoutOnly && req.FileOnly {
		return fmt.Errorf("failed to build log helper args: stdout_only and file_only cannot both be true")
	}
	return nil
}

func (r *LogSearchRequest) normalize() {
	r.Question = strings.TrimSpace(r.Question)
	r.CodeRepoPath = strings.TrimSpace(r.CodeRepoPath)
	r.Project = strings.TrimSpace(strings.ToLower(r.Project))
	r.AppID = strings.TrimSpace(r.AppID)
	r.Service = strings.TrimSpace(r.Service)
	r.Env = strings.ToLower(strings.TrimSpace(r.Env))
	r.Pod = strings.TrimSpace(r.Pod)
	r.Cluster = strings.TrimSpace(r.Cluster)
	r.Container = strings.TrimSpace(r.Container)
	r.Deployment = strings.TrimSpace(r.Deployment)
	r.ImageTag = strings.TrimSpace(r.ImageTag)
	r.ImageRegex = strings.TrimSpace(r.ImageRegex)
	r.At = strings.TrimSpace(r.At)
	if r.PodLimit <= 0 {
		r.PodLimit = 20
	}
	if r.BeforeMinutes <= 0 {
		r.BeforeMinutes = 2
	}
	if r.AfterMinutes <= 0 {
		r.AfterMinutes = 1
	}
}

func projectForRequest(req LogSearchRequest) string {
	project := strings.TrimSpace(strings.ToLower(req.Project))
	if project == "" {
		return logProjectMember
	}
	if project == logProjectMember || project == logProjectFuyao {
		return project
	}
	return ""
}

func commandEnv(base []string, req LogSearchRequest) []string {
	if projectForRequest(req) == logProjectFuyao {
		return upsertEnv(base, "AD_PLATFORM_K8S_APP_ID", commandAppID(req))
	}
	return upsertEnv(base, "MEMBER_K8S_TEST_APP_ID", commandAppID(req))
}

func commandAppID(req LogSearchRequest) string {
	req.normalize()
	if req.AppID == "" {
		if projectForRequest(req) == logProjectFuyao {
			return defaultFuyaoK8SAppID
		}
		return defaultMemberK8SAppID
	}
	return req.AppID
}

func upsertEnv(base []string, key string, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(base)+1)
	replaced := false
	for _, item := range base {
		if strings.HasPrefix(item, prefix) {
			if replaced {
				continue
			}
			out = append(out, prefix+value)
			replaced = true
			continue
		}
		out = append(out, item)
	}
	if !replaced {
		out = append(out, prefix+value)
	}
	return out
}

func isAll(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	return value == "all" || value == "*" || value == "__all__"
}

func (r LogSearchRequest) allKeywords() []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(r.Keywords)+1)
	for _, keyword := range append([]string{r.Keyword}, r.Keywords...) {
		keyword = strings.TrimSpace(keyword)
		if keyword == "" {
			continue
		}
		if _, ok := seen[keyword]; ok {
			continue
		}
		seen[keyword] = struct{}{}
		out = append(out, keyword)
	}
	return out
}

func (r LogSearchRequest) remoteKeywords() []string {
	keywords := r.allKeywords()
	fieldValues := make([]string, 0, len(keywords))
	plainKeywords := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		_, value, ok := parseFieldValueKeyword(keyword)
		if ok {
			fieldValues = append(fieldValues, value)
			continue
		}
		plainKeywords = append(plainKeywords, keyword)
	}
	out := make([]string, 0, len(plainKeywords)+1)
	out = append(out, fieldValues...)
	out = append(out, plainKeywords...)
	return uniqueNonEmptyStrings(out)
}

func (r LogSearchRequest) allRegexes() []string {
	out := make([]string, 0, len(r.Regexes))
	seen := map[string]struct{}{}
	for _, regexText := range r.Regexes {
		regexText = strings.TrimSpace(regexText)
		if regexText == "" {
			continue
		}
		seen[regexText] = struct{}{}
		out = append(out, regexText)
	}
	return out
}

func (r LogSearchRequest) requiredFieldRegexes() []string {
	out := make([]string, 0, len(r.Keywords)+1)
	seen := map[string]struct{}{}
	for _, keyword := range append([]string{r.Keyword}, r.Keywords...) {
		field, value, ok := parseFieldValueKeyword(keyword)
		if !ok {
			continue
		}
		regexText := regexp.QuoteMeta(field) + `\\?["']?\s*[:=]\s*\\?["']?` + regexp.QuoteMeta(value)
		if _, exists := seen[regexText]; exists {
			continue
		}
		seen[regexText] = struct{}{}
		out = append(out, regexText)
	}
	return out
}

func filterLogOutputByRequest(raw map[string]interface{}, req LogSearchRequest) {
	regexes := req.requiredFieldRegexes()
	if len(regexes) == 0 {
		return
	}
	compiled := make([]*regexp.Regexp, 0, len(regexes))
	for _, regexText := range regexes {
		regex, err := regexp.Compile(regexText)
		if err != nil {
			continue
		}
		compiled = append(compiled, regex)
	}
	if len(compiled) == 0 {
		return
	}
	if stdout, ok := raw["stdout"].(map[string]interface{}); ok {
		lines := filterLinesByRegexes(interfaceSlice(stdout["lines"]), compiled)
		stdout["lines"] = lines
		stdout["totalMatches"] = len(lines)
		stdout["truncated"] = false
	}
	for _, item := range interfaceSlice(raw["fileLogs"]) {
		fileLog, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		lines := filterLinesByRegexes(interfaceSlice(fileLog["lines"]), compiled)
		fileLog["lines"] = lines
		fileLog["lineCount"] = len(lines)
		fileLog["truncated"] = false
	}
	for _, item := range interfaceSlice(raw["results"]) {
		result, ok := item.(map[string]interface{})
		if ok {
			filterLogOutputByRequest(result, req)
		}
	}
	if nestedRaw, ok := raw["raw"].(map[string]interface{}); ok {
		filterLogOutputByRequest(nestedRaw, req)
	}
}

func filterLinesByRegexes(lines []interface{}, regexes []*regexp.Regexp) []interface{} {
	out := make([]interface{}, 0, len(lines))
	for _, item := range lines {
		line, ok := item.(string)
		if !ok {
			continue
		}
		matchedAll := true
		for _, regex := range regexes {
			if !regex.MatchString(line) {
				matchedAll = false
				break
			}
		}
		if matchedAll {
			out = append(out, line)
		}
	}
	return out
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func parseFieldValueKeyword(keyword string) (string, string, bool) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" || !strings.Contains(keyword, "=") {
		return "", "", false
	}
	parts := strings.SplitN(keyword, "=", 2)
	field := strings.TrimSpace(parts[0])
	value := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
	if field == "" || value == "" {
		return "", "", false
	}
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`).MatchString(field) {
		return "", "", false
	}
	return field, value, true
}

func summarizeLogOutput(raw map[string]interface{}) LogSearchSummary {
	summary := LogSearchSummary{}
	if target, ok := raw["target"].(map[string]interface{}); ok {
		summary.Target = joinNonEmpty([]string{
			stringFromMap(target, "service"),
			stringFromMap(target, "env"),
			stringFromMap(target, "pod"),
			stringFromMap(target, "container"),
		}, " / ")
	}
	summary.LogFiles = stringSlice(raw["logFiles"])
	if stdout, ok := raw["stdout"].(map[string]interface{}); ok {
		if errText := stringFromMap(stdout, "error"); errText != "" {
			summary.Errors = append(summary.Errors, errText)
		}
		summary.StdoutLines = len(interfaceSlice(stdout["lines"]))
	}
	for _, item := range interfaceSlice(raw["fileLogs"]) {
		fileLog, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if errText := stringFromMap(fileLog, "error"); errText != "" {
			summary.Errors = append(summary.Errors, errText)
		}
		summary.FileLogLines += len(interfaceSlice(fileLog["lines"]))
	}
	for _, item := range interfaceSlice(raw["results"]) {
		result, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		nested := summarizeLogOutput(result)
		summary.StdoutLines += nested.StdoutLines
		summary.FileLogLines += nested.FileLogLines
		summary.LogFiles = appendUniqueStrings(summary.LogFiles, nested.LogFiles...)
		summary.Errors = append(summary.Errors, nested.Errors...)
		if summary.Target == "" || ((nested.StdoutLines > 0 || nested.FileLogLines > 0) && summary.StdoutLines+summary.FileLogLines == nested.StdoutLines+nested.FileLogLines) {
			summary.Target = nested.Target
		}
	}
	if nestedRaw, ok := raw["raw"].(map[string]interface{}); ok {
		nested := summarizeLogOutput(nestedRaw)
		summary.StdoutLines += nested.StdoutLines
		summary.FileLogLines += nested.FileLogLines
		summary.LogFiles = appendUniqueStrings(summary.LogFiles, nested.LogFiles...)
		summary.Errors = append(summary.Errors, nested.Errors...)
		if summary.Target == "" {
			summary.Target = nested.Target
		}
	}
	return summary
}

func containsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(wanted)) {
			return true
		}
	}
	return false
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func stringFromMap(values map[string]interface{}, key string) string {
	if value, ok := values[key].(string); ok {
		return value
	}
	return ""
}

func stringSlice(value interface{}) []string {
	items := interfaceSlice(value)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok && text != "" {
			out = append(out, text)
		}
	}
	return out
}

func interfaceSlice(value interface{}) []interface{} {
	if items, ok := value.([]interface{}); ok {
		return items
	}
	return nil
}

func joinNonEmpty(values []string, sep string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			parts = append(parts, strings.TrimSpace(value))
		}
	}
	return strings.Join(parts, sep)
}

func compactText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len([]rune(value)) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit]) + "..."
}

func appendUniqueStrings(dst []string, values ...string) []string {
	seen := make(map[string]struct{}, len(dst)+len(values))
	for _, item := range dst {
		seen[item] = struct{}{}
	}
	for _, item := range values {
		if strings.TrimSpace(item) == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		dst = append(dst, item)
	}
	return dst
}
