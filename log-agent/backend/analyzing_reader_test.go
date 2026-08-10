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
