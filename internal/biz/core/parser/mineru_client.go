package parser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// MinerUClient 调用 MinerU SaaS API。
type MinerUClient struct {
	apiURL string
	apiKey string
	client *http.Client
}

// NewMinerUClient 创建 MinerUClient。
func NewMinerUClient(apiURL, apiKey string, httpClient *http.Client) *MinerUClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &MinerUClient{
		apiURL: strings.TrimRight(apiURL, "/"),
		apiKey: apiKey,
		client: httpClient,
	}
}

func (c *MinerUClient) requestUpload(ctx context.Context, req minerUSubmitRequest) (minerUUploadTicket, error) {
	if strings.TrimSpace(c.apiURL) == "" {
		return minerUUploadTicket{}, fmt.Errorf("mineru api url is empty")
	}
	if strings.TrimSpace(c.apiKey) == "" {
		return minerUUploadTicket{}, fmt.Errorf("mineru api key is empty")
	}
	body := map[string]any{
		"enable_formula": req.EnableFormula,
		"enable_table":   req.EnableTable,
		"language":       req.Language,
		"files": []map[string]any{
			{
				"name":    req.FileName,
				"is_ocr":  req.OCR,
				"data_id": req.DataID,
			},
		},
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return minerUUploadTicket{}, fmt.Errorf("marshal mineru request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL+"/file-urls/batch", bytes.NewReader(jsonBody))
	if err != nil {
		return minerUUploadTicket{}, fmt.Errorf("create mineru request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return minerUUploadTicket{}, fmt.Errorf("mineru requestUpload: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return minerUUploadTicket{}, fmt.Errorf("read mineru requestUpload response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return minerUUploadTicket{}, fmt.Errorf("mineru requestUpload HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	var decoded struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			BatchID  string   `json:"batch_id"`
			FileURLs []string `json:"file_urls"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return minerUUploadTicket{}, fmt.Errorf("decode mineru requestUpload response: %w", err)
	}
	if decoded.Code != 0 {
		return minerUUploadTicket{}, fmt.Errorf("mineru requestUpload code=%d msg=%s", decoded.Code, decoded.Msg)
	}
	if strings.TrimSpace(decoded.Data.BatchID) == "" {
		return minerUUploadTicket{}, fmt.Errorf("mineru requestUpload missing batch_id")
	}
	if len(decoded.Data.FileURLs) == 0 || strings.TrimSpace(decoded.Data.FileURLs[0]) == "" {
		return minerUUploadTicket{}, fmt.Errorf("mineru requestUpload missing upload url")
	}
	return minerUUploadTicket{BatchID: decoded.Data.BatchID, UploadURL: decoded.Data.FileURLs[0]}, nil
}

func (c *MinerUClient) uploadFile(ctx context.Context, uploadURL string, data []byte) error {
	if strings.TrimSpace(uploadURL) == "" {
		return fmt.Errorf("mineru upload url is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create mineru upload request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("mineru uploadFile: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("mineru uploadFile HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (c *MinerUClient) queryResult(ctx context.Context, batchID string) (minerUStatus, error) {
	if strings.TrimSpace(c.apiURL) == "" {
		return minerUStatus{}, fmt.Errorf("mineru api url is empty")
	}
	if strings.TrimSpace(c.apiKey) == "" {
		return minerUStatus{}, fmt.Errorf("mineru api key is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL+"/extract-results/batch/"+batchID, nil)
	if err != nil {
		return minerUStatus{}, fmt.Errorf("create mineru query request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.client.Do(req)
	if err != nil {
		return minerUStatus{}, fmt.Errorf("mineru queryResult: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return minerUStatus{}, fmt.Errorf("read mineru queryResult response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return minerUStatus{}, fmt.Errorf("mineru queryResult HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	var decoded struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			ExtractResult []struct {
				State      string `json:"state"`
				FullZipURL string `json:"full_zip_url"`
				ErrMsg     string `json:"err_msg"`
			} `json:"extract_result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return minerUStatus{}, fmt.Errorf("decode mineru queryResult response: %w", err)
	}
	if decoded.Code != 0 {
		return minerUStatus{}, fmt.Errorf("mineru queryResult code=%d msg=%s", decoded.Code, decoded.Msg)
	}
	if len(decoded.Data.ExtractResult) == 0 {
		return minerUStatus{State: minerUStateRunning}, nil
	}
	item := decoded.Data.ExtractResult[0]
	return minerUStatus{
		State:  parseMinerUState(item.State),
		ZipURL: item.FullZipURL,
		ErrMsg: item.ErrMsg,
	}, nil
}

func (c *MinerUClient) downloadZip(ctx context.Context, zipURL string) ([]byte, error) {
	if strings.TrimSpace(zipURL) == "" {
		return nil, fmt.Errorf("mineru zip url is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, zipURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create mineru download request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mineru downloadZip: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read mineru download response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mineru downloadZip HTTP %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

type minerUSubmitRequest struct {
	FileName      string
	DataID        string
	OCR           bool
	EnableTable   bool
	EnableFormula bool
	Language      string
}

type minerUUploadTicket struct {
	BatchID   string
	UploadURL string
}

type minerUState string

const (
	minerUStateRunning   minerUState = "RUNNING"
	minerUStateSucceeded minerUState = "SUCCEEDED"
	minerUStateCompleted minerUState = "COMPLETED"
	minerUStateFinished  minerUState = "FINISHED"
	minerUStateFailed    minerUState = "FAILED"
	minerUStateCancelled minerUState = "CANCELLED"
)

type minerUStatus struct {
	State  minerUState
	ZipURL string
	ErrMsg string
}

func parseMinerUState(raw string) minerUState {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case string(minerUStateSucceeded):
		return minerUStateSucceeded
	case string(minerUStateCompleted):
		return minerUStateCompleted
	case string(minerUStateFinished):
		return minerUStateFinished
	case string(minerUStateFailed):
		return minerUStateFailed
	case string(minerUStateCancelled):
		return minerUStateCancelled
	default:
		return minerUStateRunning
	}
}
