package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

type LogStreamEvent struct {
	Type         string             `json:"type"`
	TraceID      string             `json:"trace_id,omitempty"`
	Message      string             `json:"message,omitempty"`
	Delta        string             `json:"delta,omitempty"`
	Result       *LogSearchResponse `json:"result,omitempty"`
	Analysis     *AnalysisResult    `json:"analysis,omitempty"`
	CodeEvidence []CodeEvidence     `json:"code_evidence,omitempty"`
	Error        string             `json:"error,omitempty"`
}

type LogStreamEmitter func(LogStreamEvent) bool

type LogStreamReader interface {
	SearchStream(ctx context.Context, req LogSearchRequest, emit LogStreamEmitter) (*LogSearchResponse, error)
}

func streamSearchWithReader(ctx context.Context, reader LogReader, req LogSearchRequest, emit LogStreamEmitter) (*LogSearchResponse, error) {
	if streamer, ok := reader.(LogStreamReader); ok {
		return streamer.SearchStream(ctx, req, emit)
	}
	emit(LogStreamEvent{Type: "progress", TraceID: req.TraceID, Message: "开始抓取 K8S 日志"})
	slog.Info("log stream fallback search started", "trace_id", req.TraceID, "service", req.Service, "env", req.Env)
	resp, err := reader.Search(ctx, req)
	if err != nil {
		slog.Error("log stream fallback search failed", "trace_id", req.TraceID, "err", err)
		return nil, err
	}
	emit(LogStreamEvent{Type: "log_result", TraceID: req.TraceID, Message: "日志查询完成", Result: resp})
	slog.Info("log stream fallback search completed", "trace_id", req.TraceID, "stdout_lines", resp.Summary.StdoutLines, "file_log_lines", resp.Summary.FileLogLines)
	return resp, nil
}

func (r *AnalyzingLogReader) SearchStream(ctx context.Context, req LogSearchRequest, emit LogStreamEmitter) (*LogSearchResponse, error) {
	if r == nil || r.base == nil {
		return nil, fmt.Errorf("failed to stream log search: reader is nil")
	}
	emit(LogStreamEvent{Type: "progress", TraceID: req.TraceID, Message: "开始抓取 K8S 日志"})
	slog.Info("log stream search started", "trace_id", req.TraceID, "service", req.Service, "env", req.Env, "deployment", req.Deployment, "pod", req.Pod)

	resp, err := r.base.Search(ctx, req)
	if err != nil {
		slog.Error("log stream search failed", "trace_id", req.TraceID, "err", err)
		return nil, err
	}
	emit(LogStreamEvent{Type: "log_result", TraceID: req.TraceID, Message: "日志查询完成", Result: resp})
	slog.Info("log stream search completed", "trace_id", req.TraceID, "stdout_lines", resp.Summary.StdoutLines, "file_log_lines", resp.Summary.FileLogLines, "job_count", resp.Summary.JobCount)

	if r.analyzer == nil || req.ResolveOnly {
		return resp, nil
	}

	codeRepoPath := codeRepoPathForRequest(req, r.conf.CodeRepoPath)
	emit(LogStreamEvent{Type: "analysis_progress", TraceID: req.TraceID, Message: "开始检索会员代码链路线索"})
	slog.Info("code evidence search started", "trace_id", req.TraceID, "repo", codeRepoPath)
	codeEvidence := searchCodeEvidence(ctx, codeRepoPath, req.Service, req, resp.Raw, r.conf.CodeMaxLines)
	emit(LogStreamEvent{Type: "code_evidence", TraceID: req.TraceID, Message: fmt.Sprintf("代码线索检索完成，共 %d 条", len(codeEvidence)), CodeEvidence: codeEvidence})
	slog.Info("code evidence search completed", "trace_id", req.TraceID, "count", len(codeEvidence))

	input := AnalysisInput{
		Question:     req.Question,
		LogText:      logTextForAnalysis(resp),
		CodeEvidence: codeEvidence,
	}
	emit(LogStreamEvent{Type: "analysis_progress", TraceID: req.TraceID, Message: "开始调用 360 智脑生成定位结果"})
	slog.Info("analyzer stream started", "trace_id", req.TraceID, "model_route", strings.Join(qihoo360ModelFallbacks, ","), "bailian_model", r.conf.BailianModel, "log_chars", len(input.LogText))

	var analysis *AnalysisResult
	if streaming, ok := r.analyzer.(StreamingAnalyzer); ok {
		analysis, err = streaming.AnalyzeStream(ctx, input, func(delta string) {
			if strings.TrimSpace(delta) == "" {
				return
			}
			emit(LogStreamEvent{Type: "analysis_delta", TraceID: req.TraceID, Delta: delta})
		})
	} else {
		analysis, err = r.analyzer.Analyze(ctx, input)
	}
	if err != nil {
		slog.Error("analyzer stream failed", "trace_id", req.TraceID, "err", err)
		resp.Analysis = &AnalysisResult{Error: err.Error(), CodeEvidence: codeEvidence}
		emit(LogStreamEvent{Type: "analysis_result", TraceID: req.TraceID, Analysis: resp.Analysis})
		return resp, nil
	}
	if analysis != nil {
		analysis.CodeEvidence = codeEvidence
	}
	resp.Analysis = analysis
	emit(LogStreamEvent{Type: "analysis_result", TraceID: req.TraceID, Message: "智能分析完成", Analysis: analysis})
	slog.Info("analyzer stream completed", "trace_id", req.TraceID)
	return resp, nil
}
