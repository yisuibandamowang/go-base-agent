package initialize

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// intentTreeResponse 意图树接口的扁平节点。
type intentTreeResponse struct {
	Total int              `json:"total"`
	Data  []map[string]any `json:"data"`
}

// listKnowledgeBases 拉取服务端全部知识库。
func (c *client) listKnowledgeBases(ctx context.Context) ([]map[string]any, error) {
	return c.listPagedRecords("/api/ragent/knowledge-base")
}

// listDocuments 拉取指定知识库下的全部文档。
func (c *client) listDocuments(ctx context.Context, kbID string) ([]map[string]any, error) {
	return c.listPagedRecords("/api/ragent/knowledge-base/" + encodePathValue(kbID) + "/docs")
}

// flattenIntentTree 拉取并摊平意图树。
func (c *client) flattenIntentTree(ctx context.Context) ([]map[string]any, error) {
	var resp struct {
		Code    string           `json:"code"`
		Message string           `json:"message"`
		Data    []map[string]any `json:"data"`
	}
	if err := c.getJSON("/api/ragent/intent-tree/trees", &resp); err != nil {
		return nil, err
	}
	return flattenIntentNodes(resp.Data), nil
}

func flattenIntentNodes(nodes []map[string]any) []map[string]any {
	var result []map[string]any
	for _, node := range nodes {
		result = append(result, node)
		children, _ := node["children"].([]any)
		for _, child := range children {
			if childMap, ok := child.(map[string]any); ok {
				result = append(result, flattenIntentNodes([]map[string]any{childMap})...)
			}
		}
	}
	return result
}

// listSampleQuestions 拉取全部示例问题。
func (c *client) listSampleQuestions(ctx context.Context) ([]map[string]any, error) {
	return c.listPagedRecords("/api/ragent/sample-questions")
}

func (c *client) listPagedRecords(path string) ([]map[string]any, error) {
	all := make([]map[string]any, 0)
	for current := 1; ; current++ {
		var resp struct {
			Data json.RawMessage `json:"data"`
		}
		pagePath := fmt.Sprintf("%s?current=%d&size=500", path, current)
		if err := c.getJSON(pagePath, &resp); err != nil {
			return nil, err
		}
		raw := bytes.TrimSpace(resp.Data)
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			return all, nil
		}
		if raw[0] == '[' {
			var records []map[string]any
			if err := json.Unmarshal(raw, &records); err != nil {
				return nil, fmt.Errorf("解析列表响应失败: %w", err)
			}
			return append(all, records...), nil
		}
		var page struct {
			Records []map[string]any `json:"records"`
			Pages   int              `json:"pages"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("解析分页响应失败: %w", err)
		}
		all = append(all, page.Records...)
		if page.Pages <= current || page.Pages <= 0 {
			return all, nil
		}
	}
}

// initKnowledgeBases 创建数据集定义的知识库，已存在且配置一致时复用。
func initKnowledgeBases(ctx context.Context, c *client, dataset *Dataset, dryRun bool) error {
	existingByCollection := make(map[string]map[string]any)
	if !dryRun {
		bases, err := c.listKnowledgeBases(ctx)
		if err != nil {
			return fmt.Errorf("拉取知识库列表失败: %w", err)
		}
		for _, base := range bases {
			existingByCollection[strValue(base["collectionName"])] = base
		}
	}
	for _, definition := range dataset.KnowledgeBases {
		if dryRun {
			fmt.Printf("[knowledge-base][dry-run] create ref=%s name=%s collection=%s model=%s\n",
				definition.Ref, definition.Name, definition.CollectionName, definition.EmbeddingModel)
			continue
		}
		existing := existingByCollection[definition.CollectionName]
		if existing != nil {
			if definition.Name != strValue(existing["name"]) {
				return fmt.Errorf("Collection 已存在但名称不同: %s", definition.CollectionName)
			}
			if definition.EmbeddingModel != strValue(existing["embeddingModel"]) {
				return fmt.Errorf("Collection 已存在但嵌入模型不同: %s", definition.CollectionName)
			}
			fmt.Printf("[knowledge-base] 复用已存在知识库: %s (%s)\n", definition.Name, strValue(existing["id"]))
			continue
		}
		payload := map[string]any{
			"name":           definition.Name,
			"embeddingModel": definition.EmbeddingModel,
			"collectionName": definition.CollectionName,
		}
		var resp struct {
			Code string `json:"code"`
			Data string `json:"data"`
		}
		if err := c.postJSON("/api/ragent/knowledge-base", payload, &resp); err != nil {
			return fmt.Errorf("创建知识库失败 %s: %w", definition.Name, err)
		}
		fmt.Printf("[knowledge-base] 已创建: %s (%s)\n", definition.Name, resp.Data)
	}
	return nil
}

func initDocuments(ctx context.Context, c *client, dataset *Dataset, dryRun bool, timeout, pollInterval time.Duration) error {
	return initDocumentsWithReplace(ctx, c, dataset, dryRun, timeout, pollInterval, true)
}

func initDocumentsWithReplace(ctx context.Context, c *client, dataset *Dataset, dryRun bool, timeout, pollInterval time.Duration, replaceExisting bool) error {
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	if pollInterval <= 0 {
		pollInterval = 3 * time.Second
	}
	bases, err := c.listKnowledgeBases(ctx)
	if err != nil {
		return fmt.Errorf("拉取知识库列表失败: %w", err)
	}
	idByCollection := make(map[string]string, len(bases))
	for _, base := range bases {
		idByCollection[strValue(base["collectionName"])] = strValue(base["id"])
	}
	for _, definition := range dataset.KnowledgeBases {
		dir := strings.TrimSpace(definition.DocumentsDir)
		if dir == "" {
			continue
		}
		kbID := idByCollection[definition.CollectionName]
		if kbID == "" {
			return fmt.Errorf("文档所属知识库不存在: %s", definition.CollectionName)
		}
		files, err := collectDocumentFiles(dir)
		if err != nil {
			return fmt.Errorf("读取文档目录失败 %s: %w", dir, err)
		}
		existing, err := c.listDocuments(ctx, kbID)
		if err != nil {
			return fmt.Errorf("拉取文档列表失败 %s: %w", definition.Name, err)
		}
		for _, filePath := range files {
			fileName := filepath.Base(filePath)
			if dryRun {
				fmt.Printf("[document][dry-run] upload kb=%s file=%s\n", definition.Ref, filePath)
				continue
			}
			existingSameName := documentsNamed(existing, fileName)
			if len(existingSameName) > 0 && !replaceExisting {
				if shouldSkipExistingDocument(existingSameName, fileName) {
					fmt.Printf("[document] 跳过已成功文档: %s\n", fileName)
					continue
				}
				return fmt.Errorf("文档已存在且不可替换: %s", fileName)
			}
			for _, old := range existingSameName {
				if strValue(old["id"]) != "" {
					if strings.EqualFold(strValue(old["status"]), "running") {
						return fmt.Errorf("已有文档正在分块，无法替换: %s", fileName)
					}
					if err := c.delete("/api/ragent/knowledge-base/docs/" + encodePathValue(strValue(old["id"]))); err != nil {
						return fmt.Errorf("删除旧文档失败 %s: %w", fileName, err)
					}
				}
			}
			docID, err := c.uploadDocument(ctx, kbID, filePath, definition.IngestionSpec)
			if err != nil {
				return err
			}
			if err := c.postEmpty(ctx, "/api/ragent/knowledge-base/docs/"+encodePathValue(docID)+"/chunk"); err != nil {
				return fmt.Errorf("触发文档分块失败 %s: %w", fileName, err)
			}
			if err := waitForDocument(ctx, c, docID, fileName, timeout, pollInterval); err != nil {
				return err
			}
		}
	}
	return nil
}

func documentsNamed(documents []map[string]any, name string) []map[string]any {
	result := make([]map[string]any, 0)
	for _, document := range documents {
		if strValue(document["docName"]) == name {
			result = append(result, document)
		}
	}
	return result
}

func collectDocumentFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			nested, err := collectDocumentFiles(path)
			if err != nil {
				return nil, err
			}
			files = append(files, nested...)
			continue
		}
		files = append(files, path)
	}
	sort.Strings(files)
	return files, nil
}

func shouldSkipExistingDocument(existing []map[string]any, name string) bool {
	for _, doc := range existing {
		if strValue(doc["docName"]) == name && strings.EqualFold(strValue(doc["status"]), "success") && numericValue(doc["chunkCount"]) > 0 {
			return true
		}
	}
	return false
}

func waitForDocument(ctx context.Context, c *client, docID, name string, timeout, pollInterval time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		var resp struct {
			Data map[string]any `json:"data"`
		}
		if err := c.getJSON("/api/ragent/knowledge-base/docs/"+encodePathValue(docID), &resp); err != nil {
			return fmt.Errorf("查询文档状态失败 %s: %w", name, err)
		}
		status := strings.ToLower(strValue(resp.Data["status"]))
		switch status {
		case "success":
			if numericValue(resp.Data["chunkCount"]) <= 0 {
				return fmt.Errorf("文档分块成功但没有有效分块: %s", name)
			}
			return nil
		case "failed", "error":
			return fmt.Errorf("文档分块失败 %s: %s", name, strValue(resp.Data["errorMessage"]))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("等待文档分块超时 %s", name)
		case <-ticker.C:
		}
	}
}

func numericValue(value any) int {
	switch n := value.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		var parsed int
		_, _ = fmt.Sscanf(n, "%d", &parsed)
		return parsed
	default:
		return 0
	}
}

// initIntentTree 先清理旧节点再按数据集重建。
func initIntentTree(ctx context.Context, c *client, dataset *Dataset, dryRun bool) error {
	if dryRun {
		for _, intent := range dataset.Intents {
			fmt.Printf("[intent][dry-run] create code=%s parent=%s kbRef=%s\n",
				intent.Code, intent.ParentCode, intent.KnowledgeBaseRef)
		}
		return nil
	}

	existing, err := c.flattenIntentTree(ctx)
	if err != nil {
		return fmt.Errorf("拉取意图树失败: %w", err)
	}
	if len(existing) > 0 {
		ids := make([]string, 0, len(existing))
		for _, node := range existing {
			ids = append(ids, strValue(node["id"]))
		}
		var resp struct {
			Code string `json:"code"`
		}
		if err := c.postJSON("/api/ragent/intent-tree/batch/delete", map[string]any{"ids": ids}, &resp); err != nil {
			return fmt.Errorf("清理旧意图节点失败: %w", err)
		}
		fmt.Printf("[intent] 已清理旧意图节点: %d\n", len(ids))
	}

	kbIDByRef, err := resolveKBIDs(ctx, c, dataset)
	if err != nil {
		return err
	}
	for _, intent := range dataset.Intents {
		payload := map[string]any{
			"intentCode": intent.Code,
			"name":       intent.Name,
			"level":      intent.Level,
			"kind":       intent.Kind,
			"sortOrder":  intent.SortOrder,
			"enabled":    boolToInt(intent.Enabled),
		}
		putIfNotBlank(payload, "parentCode", intent.ParentCode)
		putIfNotBlank(payload, "description", intent.Description)
		putIfNotBlank(payload, "examples", intent.Examples)
		putIfNotBlank(payload, "mcpToolId", intent.McpToolID)
		putIfNotBlank(payload, "promptSnippet", intent.PromptSnippet)
		putIfNotBlank(payload, "promptTemplate", intent.PromptTemplate)
		putIfNotBlank(payload, "paramPromptTemplate", intent.ParamPromptTemplate)
		if intent.TopK > 0 {
			payload["topK"] = intent.TopK
		}
		if intent.KnowledgeBaseRef != "" {
			kbID, ok := kbIDByRef[intent.KnowledgeBaseRef]
			if !ok {
				return fmt.Errorf("意图 %s 引用的知识库尚未创建: %s", intent.Code, intent.KnowledgeBaseRef)
			}
			payload["kbId"] = kbID
		}
		var resp struct {
			Code string `json:"code"`
			Data string `json:"data"`
		}
		if err := c.postJSON("/api/ragent/intent-tree", payload, &resp); err != nil {
			return fmt.Errorf("创建意图失败 %s: %w", intent.Code, err)
		}
		fmt.Printf("[intent] 已创建: %s (%s)\n", intent.Code, resp.Data)
	}
	return nil
}

// initSampleQuestions 先清理旧问题再按数据集重建。
func initSampleQuestions(ctx context.Context, c *client, dataset *Dataset, dryRun bool) error {
	if dryRun {
		for _, question := range dataset.Questions {
			fmt.Printf("[sample-question][dry-run] create ref=%s title=%s\n", question.Ref, question.Title)
		}
		return nil
	}

	existing, err := c.listSampleQuestions(ctx)
	if err != nil {
		return fmt.Errorf("拉取示例问题失败: %w", err)
	}
	for _, item := range existing {
		if err := c.delete("/api/ragent/sample-questions/" + encodePathValue(strValue(item["id"]))); err != nil {
			return fmt.Errorf("清理旧示例问题失败: %w", err)
		}
	}
	if len(existing) > 0 {
		fmt.Printf("[sample-question] 已清理旧示例问题: %d\n", len(existing))
	}
	for _, question := range dataset.Questions {
		payload := map[string]any{"question": question.Text}
		putIfNotBlank(payload, "title", question.Title)
		putIfNotBlank(payload, "description", question.Description)
		var resp struct {
			Code string `json:"code"`
			Data string `json:"data"`
		}
		if err := c.postJSON("/api/ragent/sample-questions", payload, &resp); err != nil {
			return fmt.Errorf("创建示例问题失败 %s: %w", question.Ref, err)
		}
		fmt.Printf("[sample-question] 已创建: %s (%s)\n", question.Ref, resp.Data)
	}
	return nil
}

// verify 校验远端初始化结果与数据集一致。
func verify(ctx context.Context, c *client, dataset *Dataset) error {
	bases, err := c.listKnowledgeBases(ctx)
	if err != nil {
		return fmt.Errorf("校验知识库失败: %w", err)
	}
	baseByCollection := make(map[string]map[string]any, len(bases))
	for _, base := range bases {
		baseByCollection[strValue(base["collectionName"])] = base
	}
	if len(baseByCollection) != len(dataset.KnowledgeBases) {
		return fmt.Errorf("知识库总数校验失败，actual=%d, expected=%d",
			len(baseByCollection), len(dataset.KnowledgeBases))
	}

	documentCount := 0
	for _, definition := range dataset.KnowledgeBases {
		base := baseByCollection[definition.CollectionName]
		if base == nil {
			return fmt.Errorf("缺少知识库: %s", definition.CollectionName)
		}
		documents, err := c.listDocuments(ctx, strValue(base["id"]))
		if err != nil {
			return fmt.Errorf("校验文档列表失败 %s: %w", definition.Name, err)
		}
		expectedNames := make(map[string]struct{})
		files, err := collectDocumentFiles(definition.DocumentsDir)
		if err != nil {
			return fmt.Errorf("读取文档目录失败 %s: %w", definition.Name, err)
		}
		for _, filePath := range files {
			expectedNames[filepath.Base(filePath)] = struct{}{}
		}
		if len(documents) != len(expectedNames) {
			return fmt.Errorf("知识库文档数量不一致: %s, expected=%d, actual=%d",
				definition.Name, len(expectedNames), len(documents))
		}
		for _, document := range documents {
			name := strValue(document["docName"])
			if _, ok := expectedNames[name]; !ok {
				return fmt.Errorf("出现当前智能体类型外的文档: %s", name)
			}
			status := strValue(document["status"])
			if !strings.EqualFold(status, "success") {
				return fmt.Errorf("文档未成功: %s, status=%s", name, status)
			}
			if numericValue(document["chunkCount"]) <= 0 {
				return fmt.Errorf("文档没有 Chunk: %s", name)
			}
		}
		documentCount += len(documents)
	}

	intents, err := c.flattenIntentTree(ctx)
	if err != nil {
		return fmt.Errorf("校验意图树失败: %w", err)
	}
	if len(intents) != len(dataset.Intents) {
		return fmt.Errorf("意图节点总数校验失败，actual=%d, expected=%d",
			len(intents), len(dataset.Intents))
	}
	intentByCode := make(map[string]map[string]any, len(intents))
	for _, intent := range intents {
		intentByCode[strValue(intent["intentCode"])] = intent
	}
	for _, definition := range dataset.Intents {
		actual := intentByCode[definition.Code]
		if actual == nil {
			return fmt.Errorf("缺少意图节点: %s", definition.Code)
		}
		if definition.KnowledgeBaseRef != "" {
			expectedCollection := ""
			for _, kb := range dataset.KnowledgeBases {
				if kb.Ref == definition.KnowledgeBaseRef {
					expectedCollection = kb.CollectionName
					break
				}
			}
			if !containsCollection(actual, expectedCollection) {
				return fmt.Errorf("意图没有绑定预期知识库: %s, expected=%s", definition.Code, expectedCollection)
			}
		}
	}

	questions, err := c.listSampleQuestions(ctx)
	if err != nil {
		return fmt.Errorf("校验示例问题失败: %w", err)
	}
	if len(questions) != len(dataset.Questions) {
		return fmt.Errorf("示例问题总数校验失败，actual=%d, expected=%d",
			len(questions), len(dataset.Questions))
	}
	actualTexts := make(map[string]struct{}, len(questions))
	for _, question := range questions {
		actualTexts[strValue(question["question"])] = struct{}{}
	}
	for _, definition := range dataset.Questions {
		if _, ok := actualTexts[definition.Text]; !ok {
			return fmt.Errorf("缺少示例问题: %s", definition.Ref)
		}
	}

	fmt.Printf("[verify] 通过：knowledgeBases=%d, documents=%d, intents=%d, questions=%d\n",
		len(baseByCollection), documentCount, len(intents), len(questions))
	return nil
}

func containsCollection(intent map[string]any, expected string) bool {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return true
	}
	if strings.EqualFold(strValue(intent["collectionName"]), expected) {
		return true
	}
	values, _ := intent["collectionNames"].([]any)
	for _, value := range values {
		if strings.EqualFold(strValue(value), expected) {
			return true
		}
	}
	return false
}

// resolveKBIDs 拉取服务端知识库并按 collectionName 映射到 ref。
func resolveKBIDs(ctx context.Context, c *client, dataset *Dataset) (map[string]string, error) {
	bases, err := c.listKnowledgeBases(ctx)
	if err != nil {
		return nil, fmt.Errorf("拉取知识库列表失败: %w", err)
	}
	idByCollection := make(map[string]string, len(bases))
	for _, base := range bases {
		idByCollection[strValue(base["collectionName"])] = strValue(base["id"])
	}
	result := make(map[string]string, len(dataset.KnowledgeBases))
	for _, definition := range dataset.KnowledgeBases {
		id, ok := idByCollection[definition.CollectionName]
		if !ok {
			return nil, fmt.Errorf("知识库尚未创建: %s", definition.CollectionName)
		}
		result[definition.Ref] = id
	}
	return result, nil
}

func strValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return fmt.Sprintf("%v", value)
}

func putIfNotBlank(payload map[string]any, key, value string) {
	if strings.TrimSpace(value) != "" {
		payload[key] = value
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
