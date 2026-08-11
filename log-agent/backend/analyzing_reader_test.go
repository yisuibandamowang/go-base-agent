package main

import "testing"

func TestCodeRepoPathForRequestUsesRequestValue(t *testing.T) {
	got := codeRepoPathForRequest(LogSearchRequest{CodeRepoPath: " /tmp/member-code "}, "/Users/work_project/360/member")
	if got != "/tmp/member-code" {
		t.Fatalf("codeRepoPathForRequest() = %q", got)
	}
}

func TestCodeRepoPathForRequestFallsBackToConfig(t *testing.T) {
	got := codeRepoPathForRequest(LogSearchRequest{}, "/Users/work_project/360/member")
	if got != "/Users/work_project/360/member" {
		t.Fatalf("codeRepoPathForRequest() = %q", got)
	}
}

func TestCodeRepoPathForRequestDefaultsFuyaoProject(t *testing.T) {
	got := codeRepoPathForRequest(LogSearchRequest{Project: "fuyao"}, "/Users/work_project/360/member")
	if got != "/Users/work_project/360/ad-platform-bot" {
		t.Fatalf("codeRepoPathForRequest() = %q", got)
	}
}

func TestCodeSearchRootNarrowsFuyaoAllServiceToWebmember(t *testing.T) {
	got := codeSearchRootForRequest("/Users/work_project/360/ad-platform-bot", "all", LogSearchRequest{Project: "fuyao"})
	want := "/Users/work_project/360/ad-platform-bot/backend/ad_platform_go/webmember"
	if got != want {
		t.Fatalf("codeSearchRootForRequest() = %q, want %q", got, want)
	}
}
