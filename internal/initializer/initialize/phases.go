package initialize

import (
	"context"
	"fmt"
	"strings"
)

// intentTreeResponse 意图树接口的扁平节点。
type intentTreeResponse struct {
	Total int              `json:"total"`
	Data  []map[string]any `json:"data"`
}

// listKnowledgeBases 拉取服务端全部知识库。
func (c *client) listKnowledgeBases(ctx context.Context) ([]map[string]any, error) {
	var resp struct {
		Code    string           `json:"code"`
		Message string           `json:"message"`
		Data    []map[string]any `json:"data"`
	}
	if err := c.getJSON("/api/ragent/knowledge-base", &resp); err != nil {
		return nil, err
	}
	if len(resp.Data) == 0 {
		return nil, nil
	}
	return resp.Data, nil
}

// listDocuments 拉取指定知识库下的全部文档。
func (c *client) listDocuments(ctx context.Context, kbID string) ([]map[string]any, error) {
	var resp struct {
		Code    string           `json:"code"`
		Message string           `json:"message"`
		Data    struct {
			Records []map[string]any `json:"records"`
			Total   int              `json:"total"`
		} `json:"data"`
	}
	if err := c.getJSON("/api/ragent/knowledge-base/"+encodePathValue(kbID)+"/docs", &resp); err != nil {
		return nil, err
	}
	if len(resp.Data.Records) == 0 {
		return nil, nil
	}
	return resp.Data.Records, nil
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
	var resp struct {
		Code    string           `json:"code"`
		Message string           `json:"message"`
		Data    struct {
			Records []map[string]any `json:"records"`
			Total   int              `json:"total"`
		} `json:"data"`
	}
	if err := c.getJSON("/api/ragent/sample-questions", &resp); err != nil {
		return nil, err
	}
	if len(resp.Data.Records) == 0 {
		return nil, nil
	}
	return resp.Data.Records, nil
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
		putIfNotBlank(payload, "promptSnippet", intent.PromptSnippet)
		putIfNotBlank(payload, "promptTemplate", intent.PromptTemplate)
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
		for _, document := range documents {
			status := strValue(document["status"])
			if !strings.EqualFold(status, "success") {
				return fmt.Errorf("文档未成功: %s, status=%s", strValue(document["docName"]), status)
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
		if intentByCode[definition.Code] == nil {
			return fmt.Errorf("缺少意图节点: %s", definition.Code)
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
