package main

import (
	"reflect"
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

	assertContainsSequence(t, args, "--keyword", "qihoo_id=3523031789")
	assertContainsSequence(t, args, "--regex", `qihoo_id["']?\s*[:=]\s*["']?3523031789`)
	assertNotContainsSequence(t, args, "--keyword", "3523031789")
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

func assertContainsSequence(t *testing.T, args []string, flag string, value string) {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return
		}
	}
	t.Fatalf("args %#v does not contain %s %q", args, flag, value)
}

func assertNotContainsSequence(t *testing.T, args []string, flag string, value string) {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			t.Fatalf("args %#v contains unexpected %s %q", args, flag, value)
		}
	}
}
