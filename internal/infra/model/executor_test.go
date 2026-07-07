package model

import (
	"errors"
	"fmt"
	"testing"

	"go-base-agent/internal/framework/config"
)

func testExecutor() *RoutingExecutor {
	return NewRoutingExecutor(NewHealthStore(config.AISelectionConfig{
		FailureThreshold: 2,
		OpenDurationMs:   100,
	}))
}

func TestExecuteWithFallback_FirstSucceeds(t *testing.T) {
	exec := testExecutor()
	targets := []Target{
		{ID: "m1", Candidate: config.AICandidateConfig{Provider: "p1", Model: "gpt-4"}},
		{ID: "m2", Candidate: config.AICandidateConfig{Provider: "p2", Model: "gpt-3.5"}},
	}

	resolve := func(tgt Target) (string, bool) { return tgt.Candidate.Model, true }
	call := func(client string, tgt Target) (string, error) {
		return fmt.Sprintf("result-from-%s", client), nil
	}

	result, err := ExecuteWithFallback(exec, CapabilityChat, targets, resolve, call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "result-from-gpt-4" {
		t.Fatalf("unexpected result: %s", result)
	}
}

func TestExecuteWithFallback_FallbackOnFailure(t *testing.T) {
	exec := testExecutor()
	targets := []Target{
		{ID: "m1", Candidate: config.AICandidateConfig{Provider: "p1", Model: "fail"}},
		{ID: "m2", Candidate: config.AICandidateConfig{Provider: "p2", Model: "success"}},
	}

	resolve := func(tgt Target) (string, bool) { return tgt.Candidate.Model, true }
	call := func(client string, tgt Target) (string, error) {
		if client == "fail" {
			return "", errors.New("simulated failure")
		}
		return fmt.Sprintf("result-from-%s", client), nil
	}

	result, err := ExecuteWithFallback(exec, CapabilityChat, targets, resolve, call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "result-from-success" {
		t.Fatalf("expected fallback result, got %s", result)
	}
}

func TestExecuteWithFallback_AllFail(t *testing.T) {
	exec := testExecutor()
	targets := []Target{
		{ID: "m1", Candidate: config.AICandidateConfig{Provider: "p1", Model: "f1"}},
		{ID: "m2", Candidate: config.AICandidateConfig{Provider: "p2", Model: "f2"}},
	}

	resolve := func(tgt Target) (string, bool) { return tgt.Candidate.Model, true }
	call := func(client string, tgt Target) (string, error) {
		return "", errors.New("simulated failure")
	}

	_, err := ExecuteWithFallback(exec, CapabilityChat, targets, resolve, call)
	if err == nil {
		t.Fatal("expected error when all fail")
	}
}

func TestExecuteWithFallback_EmptyTargets(t *testing.T) {
	exec := testExecutor()
	resolve := func(tgt Target) (string, bool) { return "", false }
	call := func(client string, tgt Target) (string, error) { return "", nil }

	_, err := ExecuteWithFallback(exec, CapabilityChat, nil, resolve, call)
	if err == nil {
		t.Fatal("expected error for empty targets")
	}
}

func TestExecuteWithFallback_ClientResolutionFailed(t *testing.T) {
	exec := testExecutor()
	targets := []Target{
		{ID: "m1", Candidate: config.AICandidateConfig{Provider: "p1", Model: "missing"}},
		{ID: "m2", Candidate: config.AICandidateConfig{Provider: "p2", Model: "found"}},
	}

	resolve := func(tgt Target) (string, bool) {
		if tgt.Candidate.Model == "missing" {
			return "", false
		}
		return tgt.Candidate.Model, true
	}
	call := func(client string, tgt Target) (string, error) {
		return fmt.Sprintf("result-from-%s", client), nil
	}

	result, err := ExecuteWithFallback(exec, CapabilityRerank, targets, resolve, call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "result-from-found" {
		t.Fatalf("expected fallback result, got %s", result)
	}
}

func TestExecuteWithFallback_HealthStoreDeniesCall(t *testing.T) {
	health := NewHealthStore(config.AISelectionConfig{
		FailureThreshold: 1,
		OpenDurationMs:   5000,
	})
	exec := NewRoutingExecutor(health)

	health.MarkFailure("m1")

	targets := []Target{
		{ID: "m1", Candidate: config.AICandidateConfig{Provider: "p1", Model: "bad"}},
		{ID: "m2", Candidate: config.AICandidateConfig{Provider: "p2", Model: "good"}},
	}

	resolve := func(tgt Target) (string, bool) { return tgt.Candidate.Model, true }
	call := func(client string, tgt Target) (string, error) {
		return fmt.Sprintf("result-from-%s", client), nil
	}

	result, err := ExecuteWithFallback(exec, CapabilityChat, targets, resolve, call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "result-from-good" {
		t.Fatalf("expected good result, got %s", result)
	}
}
