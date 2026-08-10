package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

type AnalyzingLogReader struct {
	base     LogReader
	analyzer Analyzer
	conf     AnalyzerConfig
}

func NewAnalyzingLogReader(base LogReader, analyzer Analyzer, conf AnalyzerConfig) *AnalyzingLogReader {
	return &AnalyzingLogReader{base: base, analyzer: analyzer, conf: conf}
}

func (r *AnalyzingLogReader) Search(ctx context.Context, req LogSearchRequest) (*LogSearchResponse, error) {
	resp, err := r.base.Search(ctx, req)
	if err != nil {
		return nil, err
	}
	if r == nil || r.analyzer == nil || req.ResolveOnly {
		return resp, nil
	}
	slog.Info("code evidence search started", "trace_id", req.TraceID, "repo", r.conf.CodeRepoPath)
	codeEvidence := searchCodeEvidence(ctx, r.conf.CodeRepoPath, req.Service, req, resp.Raw, r.conf.CodeMaxLines)
	slog.Info("code evidence search completed", "trace_id", req.TraceID, "count", len(codeEvidence))
	slog.Info("analyzer started", "trace_id", req.TraceID, "model_route", strings.Join(qihoo360ModelFallbacks, ","), "bailian_model", r.conf.BailianModel)
	analysis, err := r.analyzer.Analyze(ctx, AnalysisInput{
		Question:     req.Question,
		LogText:      logTextForAnalysis(resp),
		CodeEvidence: codeEvidence,
	})
	if err != nil {
		slog.Error("analyzer failed", "trace_id", req.TraceID, "err", err)
		resp.Analysis = &AnalysisResult{Error: err.Error(), CodeEvidence: codeEvidence}
		return resp, nil
	}
	if analysis != nil {
		analysis.CodeEvidence = codeEvidence
	}
	resp.Analysis = analysis
	slog.Info("analyzer completed", "trace_id", req.TraceID)
	return resp, nil
}

func logTextForAnalysis(resp *LogSearchResponse) string {
	if resp == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("summary: target=%s stdout_lines=%d file_log_lines=%d jobs=%d\n",
		resp.Summary.Target, resp.Summary.StdoutLines, resp.Summary.FileLogLines, resp.Summary.JobCount))
	for _, errText := range resp.Summary.Errors {
		b.WriteString("error: ")
		b.WriteString(errText)
		b.WriteByte('\n')
	}
	for _, line := range extractLogLines(resp.Raw) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
		if b.Len() > 12000 {
			break
		}
	}
	return compactText(b.String(), 12000)
}
