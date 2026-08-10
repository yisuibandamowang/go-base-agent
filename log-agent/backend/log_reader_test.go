package main

import (
	"context"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func testLogReaderConfig() LogReaderConfig {
	return LogReaderConfig{
		NodePath:       "node",
		ScriptPath:     "/Users/mima0000/.codex/skills/member-k8s-pod-log-read/scripts/read_pod_logs.mjs",
		Timeout:        10 * time.Second,
		AllowedEnvs:    []string{"test", "test2", "test3", "test4", "regress", "online"},
		MaxLines:       120,
		MaxStdoutLines: 80,
		MaxLineChars:   1200,
		MaxConcurrency: 4,
	}
}

func TestBuildCommandArgsUsesStructuredInputsAndDefaults(t *testing.T) {
	reader := NewScriptLogReader(testLogReaderConfig())

	args, err := reader.buildCommandArgs(LogSearchRequest{
		Service:         "pay",
		Env:             "test2",
		At:              "2026-08-10 10:00:00",
		BeforeMinutes:   3,
		AfterMinutes:    2,
		Keywords:        []string{"PayCenterFailed", "order_123"},
		IncludeCritical: true,
		IncludeGz:       true,
		AllPods:         true,
		PodLimit:        5,
	})
	if err != nil {
		t.Fatalf("buildCommandArgs() error = %v", err)
	}

	want := []string{
		"/Users/mima0000/.codex/skills/member-k8s-pod-log-read/scripts/read_pod_logs.mjs",
		"--service", "pay",
		"--env", "test2",
		"--at", "2026-08-10 10:00:00",
		"--before-minutes", "3",
		"--after-minutes", "2",
		"--keyword", "PayCenterFailed",
		"--keyword", "order_123",
		"--include-critical",
		"--include-gz",
		"--all-pods",
		"--pod-limit", "5",
		"--max-lines", "120",
		"--max-stdout-lines", "80",
		"--max-line-chars", "1200",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args mismatch\nwant: %#v\n got: %#v", want, args)
	}
}

func TestBuildCommandArgsRejectsUnsupportedEnv(t *testing.T) {
	reader := NewScriptLogReader(testLogReaderConfig())

	_, err := reader.buildCommandArgs(LogSearchRequest{Service: "pay", Env: "dev"})
	if err == nil {
		t.Fatal("buildCommandArgs() error = nil, want unsupported env error")
	}
}

func TestBuildCommandArgsRequiresTargetLocator(t *testing.T) {
	reader := NewScriptLogReader(testLogReaderConfig())

	_, err := reader.buildCommandArgs(LogSearchRequest{Keyword: "PayCenterFailed"})
	if err == nil {
		t.Fatal("buildCommandArgs() error = nil, want target locator error")
	}
}

func TestBuildCommandArgsExpandsFieldValueKeywordWithoutBroadValueMatch(t *testing.T) {
	reader := NewScriptLogReader(testLogReaderConfig())

	args, err := reader.buildCommandArgs(LogSearchRequest{
		Service:  "membergateway",
		Env:      "regress",
		Keywords: []string{"qihoo_id=3523031789"},
	})
	if err != nil {
		t.Fatalf("buildCommandArgs() error = %v", err)
	}

	assertContainsSequence(t, args, "--keyword", "3523031789")
	assertNotContainsSequence(t, args, "--keyword", "qihoo_id=3523031789")
	assertNotContainsSequence(t, args, "--regex", `qihoo_id\\?["']?\s*[:=]\s*\\?["']?3523031789`)
}

func TestBuildCommandArgsUsesFuyaoLogHelper(t *testing.T) {
	conf := testLogReaderConfig()
	conf.FuyaoScriptPath = "/Users/work_project/360/ad-platform-bot/.codex/skills/ad-platform-runtime-readonly/scripts/k8s_pod_logs.mjs"
	reader := NewScriptLogReader(conf)

	args, err := reader.buildCommandArgs(LogSearchRequest{
		Project:    "fuyao",
		Deployment: "ad-platform-fuyao-agent-backend-online",
		Keywords:   []string{"order_123"},
		AllPods:    true,
	})
	if err != nil {
		t.Fatalf("buildCommandArgs() error = %v", err)
	}

	if args[0] != conf.FuyaoScriptPath {
		t.Fatalf("script path = %q, want %q", args[0], conf.FuyaoScriptPath)
	}
	assertContainsSequence(t, args, "--deployment", "ad-platform-fuyao-agent-backend-online")
	assertContainsSequence(t, args, "--app-id", "5658")
	assertContainsSequence(t, args, "--keyword", "order_123")
}

func TestBuildCommandArgsUsesFuyaoWebmemberLogFile(t *testing.T) {
	conf := testLogReaderConfig()
	conf.FuyaoScriptPath = "/Users/work_project/360/ad-platform-bot/.codex/skills/ad-platform-runtime-readonly/scripts/k8s_pod_logs.mjs"
	reader := NewScriptLogReader(conf)

	args, err := reader.buildCommandArgs(LogSearchRequest{
		Project:    "fuyao",
		Deployment: "ad-platform-online",
		Service:    "webmember",
		Env:        "online",
		Keywords:   []string{"mid=739e1d0b130d0801da877d8f6958e92e"},
	})
	if err != nil {
		t.Fatalf("buildCommandArgs() error = %v", err)
	}

	assertContainsSequence(t, args, "--log-file", "/home/log/webmember/webmember.log")
}

func TestBuildCommandArgsDefaultsFuyaoWebmemberToAllPods(t *testing.T) {
	conf := testLogReaderConfig()
	conf.FuyaoScriptPath = "/Users/work_project/360/ad-platform-bot/.codex/skills/ad-platform-runtime-readonly/scripts/k8s_pod_logs.mjs"
	reader := NewScriptLogReader(conf)

	args, err := reader.buildCommandArgs(LogSearchRequest{
		Project:    "fuyao",
		Deployment: "ad-platform-online",
		Service:    "webmember",
		Env:        "online",
		Keywords:   []string{"product=苏打办公"},
	})
	if err != nil {
		t.Fatalf("buildCommandArgs() error = %v", err)
	}

	assertContains(t, args, "--all-pods")
	assertContainsSequence(t, args, "--pod-limit", "20")
}

func TestBuildSearchJobsUsesFuyaoDeploymentsForAllEnv(t *testing.T) {
	conf := testLogReaderConfig()

	jobs, err := buildSearchJobs(LogSearchRequest{
		Project:  "fuyao",
		Env:      "all",
		Service:  "all",
		Keywords: []string{"order_123"},
	}, conf)
	if err != nil {
		t.Fatalf("buildSearchJobs() error = %v", err)
	}

	got := make([]string, 0, len(jobs))
	for _, job := range jobs {
		got = append(got, job.Env+"/"+job.Deployment)
	}
	want := []string{
		"test/ad-platform-test",
		"regress/ad-platform-regress",
		"online/ad-platform-online",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("jobs mismatch\nwant: %#v\n got: %#v", want, got)
	}
}

func TestBuildSearchJobsUsesFuyaoDeploymentForSingleEnv(t *testing.T) {
	conf := testLogReaderConfig()

	jobs, err := buildSearchJobs(LogSearchRequest{
		Project:  "fuyao",
		Env:      "online",
		Service:  "fuyao-agent-backend",
		Keywords: []string{"order_123"},
	}, conf)
	if err != nil {
		t.Fatalf("buildSearchJobs() error = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs len = %d, want 1", len(jobs))
	}
	if got := jobs[0].Deployment; got != "ad-platform-fuyao-agent-backend-online" {
		t.Fatalf("deployment = %q, want ad-platform-fuyao-agent-backend-online", got)
	}
}

func TestBuildSearchJobsIncludesFuyaoWebmemberDeployment(t *testing.T) {
	conf := testLogReaderConfig()

	jobs, err := buildSearchJobs(LogSearchRequest{
		Project:  "fuyao",
		Env:      "online",
		Service:  "all",
		Keywords: []string{"mid=739e1d0b130d0801da877d8f6958e92e"},
	}, conf)
	if err != nil {
		t.Fatalf("buildSearchJobs() error = %v", err)
	}

	got := make([]string, 0, len(jobs))
	for _, job := range jobs {
		got = append(got, job.Service+"/"+job.Deployment)
	}
	want := []string{
		"webmember/ad-platform-online",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("jobs mismatch\nwant: %#v\n got: %#v", want, got)
	}
}

func TestBuildSearchJobsInfersFuyaoWebmemberServiceFromDeployment(t *testing.T) {
	conf := testLogReaderConfig()

	jobs, err := buildSearchJobs(LogSearchRequest{
		Project:    "fuyao",
		Env:        "online",
		Service:    "all",
		Deployment: "ad-platform-online",
		Keywords:   []string{"mid=739e1d0b130d0801da877d8f6958e92e"},
	}, conf)
	if err != nil {
		t.Fatalf("buildSearchJobs() error = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs len = %d, want 1", len(jobs))
	}
	if got := jobs[0].Service; got != "webmember" {
		t.Fatalf("service = %q, want webmember", got)
	}
}

func TestFieldValueRegexMatchesEscapedJSONFields(t *testing.T) {
	req := LogSearchRequest{Keywords: []string{"mid=739e1d0b130d0801da877d8f6958e92e"}}
	regexes := req.requiredFieldRegexes()
	if len(regexes) != 1 {
		t.Fatalf("regexes len = %d, want 1: %#v", len(regexes), regexes)
	}
	line := `\"mid\":\"739e1d0b130d0801da877d8f6958e92e\"`
	if matched, err := regexp.MatchString(regexes[0], line); err != nil || !matched {
		t.Fatalf("regex %q matched=%v err=%v for %q", regexes[0], matched, err, line)
	}
}

func TestFilterLogOutputRequiresAllFieldValueKeywords(t *testing.T) {
	req := LogSearchRequest{Keywords: []string{
		"mid=739e1d0b130d0801da877d8f6958e92e",
		"product=苏打办公",
	}}
	raw := map[string]interface{}{
		"stdout": map[string]interface{}{
			"lines": []interface{}{
				`{\"product\":\"苏打办公\",\"mid\":\"other\"}`,
				`{\"product\":\"苏打办公\",\"mid\":\"739e1d0b130d0801da877d8f6958e92e\"}`,
			},
		},
		"fileLogs": []interface{}{
			map[string]interface{}{
				"lines": []interface{}{
					`{\"product\":\"苏打办公\",\"mid\":\"other\"}`,
					`{\"product\":\"苏打办公\",\"mid\":\"739e1d0b130d0801da877d8f6958e92e\"}`,
				},
			},
		},
	}

	filterLogOutputByRequest(raw, req)

	stdout := raw["stdout"].(map[string]interface{})
	if got := len(interfaceSlice(stdout["lines"])); got != 1 {
		t.Fatalf("stdout lines len = %d, want 1", got)
	}
	fileLogs := interfaceSlice(raw["fileLogs"])
	fileLog := fileLogs[0].(map[string]interface{})
	if got := len(interfaceSlice(fileLog["lines"])); got != 1 {
		t.Fatalf("file log lines len = %d, want 1", got)
	}
}

func TestFilterLogOutputRequiresAllFieldValueKeywordsInsideHelperResults(t *testing.T) {
	req := LogSearchRequest{Keywords: []string{
		"event_id=baae75ca-ed3c-41c6-a9f0-496f24e7a0ce",
		"product=zero浏览器",
	}}
	raw := map[string]interface{}{
		"results": []interface{}{
			map[string]interface{}{
				"fileLogs": []interface{}{
					map[string]interface{}{
						"lines": []interface{}{
							`{\"event_id\":\"baae75ca-ed3c-41c6-a9f0-496f24e7a0ce\",\"product\":\"other\"}`,
							`{\"event_id\":\"baae75ca-ed3c-41c6-a9f0-496f24e7a0ce\",\"product\":\"zero浏览器\"}`,
						},
					},
				},
			},
		},
	}

	filterLogOutputByRequest(raw, req)

	results := interfaceSlice(raw["results"])
	result := results[0].(map[string]interface{})
	fileLogs := interfaceSlice(result["fileLogs"])
	fileLog := fileLogs[0].(map[string]interface{})
	if got := len(interfaceSlice(fileLog["lines"])); got != 1 {
		t.Fatalf("nested file log lines len = %d, want 1", got)
	}
}

func TestSummarizeLogOutputIncludesHelperResults(t *testing.T) {
	raw := map[string]interface{}{
		"logFiles": []interface{}{"/home/log/webmember/webmember.log"},
		"results": []interface{}{
			map[string]interface{}{
				"target": map[string]interface{}{
					"service": "webmember",
					"env":     "online",
					"pod":     "ad-platform-online-h-6b8db445d5-zq65c",
				},
				"stdout": map[string]interface{}{
					"lines": []interface{}{"stdout line"},
				},
				"fileLogs": []interface{}{
					map[string]interface{}{
						"file":  "/home/log/webmember/webmember.log",
						"lines": []interface{}{"matched line"},
					},
				},
			},
		},
	}

	summary := summarizeLogOutput(raw)

	if summary.Target != "webmember / online / ad-platform-online-h-6b8db445d5-zq65c" {
		t.Fatalf("Target = %q", summary.Target)
	}
	if summary.StdoutLines != 1 {
		t.Fatalf("StdoutLines = %d, want 1", summary.StdoutLines)
	}
	if summary.FileLogLines != 1 {
		t.Fatalf("FileLogLines = %d, want 1", summary.FileLogLines)
	}
	if !reflect.DeepEqual(summary.LogFiles, []string{"/home/log/webmember/webmember.log"}) {
		t.Fatalf("LogFiles = %#v", summary.LogFiles)
	}
}

func TestCommandEnvUsesRequestAppIDWhenProvided(t *testing.T) {
	env := commandEnv([]string{
		"MEMBER_K8S_TEST_APP_ID=1586",
		"OTHER=value",
	}, LogSearchRequest{AppID: "708635"})

	if got := envValue(env, "MEMBER_K8S_TEST_APP_ID"); got != "708635" {
		t.Fatalf("MEMBER_K8S_TEST_APP_ID = %q, want 708635", got)
	}
	if count := envKeyCount(env, "MEMBER_K8S_TEST_APP_ID"); count != 1 {
		t.Fatalf("MEMBER_K8S_TEST_APP_ID count = %d, want 1", count)
	}
	if got := envValue(env, "OTHER"); got != "value" {
		t.Fatalf("OTHER = %q, want value", got)
	}
}

func TestCommandEnvDefaultsToMemberAppID(t *testing.T) {
	env := commandEnv([]string{
		"MEMBER_K8S_TEST_APP_ID=708635",
	}, LogSearchRequest{})

	if got := envValue(env, "MEMBER_K8S_TEST_APP_ID"); got != "1586" {
		t.Fatalf("MEMBER_K8S_TEST_APP_ID = %q, want 1586", got)
	}
}

func TestCommandEnvUsesFuyaoAppID(t *testing.T) {
	env := commandEnv([]string{
		"AD_PLATFORM_K8S_APP_ID=1586",
	}, LogSearchRequest{Project: "fuyao"})

	if got := envValue(env, "AD_PLATFORM_K8S_APP_ID"); got != "5658" {
		t.Fatalf("AD_PLATFORM_K8S_APP_ID = %q, want 5658", got)
	}
}

func TestBuildSearchJobsExpandsAllEnvAndAllServiceIncludingOnline(t *testing.T) {
	conf := testLogReaderConfig()
	conf.Services = []string{"pay", "membergateway"}

	jobs, err := buildSearchJobs(LogSearchRequest{
		Env:      "all",
		Service:  "all",
		Keywords: []string{"order_123"},
		AllPods:  true,
	}, conf)
	if err != nil {
		t.Fatalf("buildSearchJobs() error = %v", err)
	}

	got := make([]string, 0, len(jobs))
	for _, job := range jobs {
		got = append(got, job.Env+"/"+job.Service)
		if !job.AllPods {
			t.Fatalf("job %+v AllPods = false, want true", job)
		}
	}
	want := []string{
		"test/pay", "test/membergateway",
		"test2/pay", "test2/membergateway",
		"test3/pay", "test3/membergateway",
		"test4/pay", "test4/membergateway",
		"regress/pay", "regress/membergateway",
		"online/pay", "online/membergateway",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("jobs mismatch\nwant: %#v\n got: %#v", want, got)
	}
}

func TestBuildSearchJobsRejectsBroadSearchWithoutKeyword(t *testing.T) {
	conf := testLogReaderConfig()
	conf.Services = []string{"pay", "membergateway"}

	_, err := buildSearchJobs(LogSearchRequest{Env: "all", Service: "all"}, conf)
	if err == nil {
		t.Fatal("buildSearchJobs() error = nil, want broad search guard error")
	}
}

func TestRunSearchBatchPreservesOrderAndRunsConcurrently(t *testing.T) {
	jobs := []LogSearchRequest{
		{TraceID: "trace-1", Env: "test2", Service: "pay"},
		{TraceID: "trace-1", Env: "test2", Service: "membergateway"},
	}
	start := time.Now()
	resp, err := runSearchBatch(context.Background(), "trace-1", jobs, 2, func(ctx context.Context, job LogSearchRequest) (*LogSearchResponse, error) {
		time.Sleep(80 * time.Millisecond)
		return &LogSearchResponse{
			TraceID: job.TraceID,
			Command: []string{"node", job.Service},
			Summary: LogSearchSummary{
				Target:       job.Service + " / " + job.Env,
				FileLogLines: 1,
			},
			Raw: map[string]interface{}{
				"service": job.Service,
			},
		}, nil
	})
	if err != nil {
		t.Fatalf("runSearchBatch() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 140*time.Millisecond {
		t.Fatalf("runSearchBatch() elapsed = %s, want concurrent execution", elapsed)
	}
	if resp.Summary.FileLogLines != 2 {
		t.Fatalf("FileLogLines = %d, want 2", resp.Summary.FileLogLines)
	}
	results := interfaceSlice(resp.Raw["results"])
	if len(results) != 2 {
		t.Fatalf("results len = %d, want 2", len(results))
	}
	first, _ := results[0].(map[string]interface{})
	second, _ := results[1].(map[string]interface{})
	if first["service"] != "pay" || second["service"] != "membergateway" {
		t.Fatalf("results order = %#v", results)
	}
}

func assertContainsSequence(t *testing.T, args []string, flag string, value string) {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return
		}
	}
	t.Fatalf("args %#v does not contain %s %q", args, flag, value)
}

func assertContains(t *testing.T, args []string, value string) {
	t.Helper()
	for _, arg := range args {
		if arg == value {
			return
		}
	}
	t.Fatalf("args %#v does not contain %q", args, value)
}

func assertNotContainsSequence(t *testing.T, args []string, flag string, value string) {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			t.Fatalf("args %#v contains unexpected %s %q", args, flag, value)
		}
	}
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}

func envKeyCount(env []string, key string) int {
	prefix := key + "="
	count := 0
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			count++
		}
	}
	return count
}
