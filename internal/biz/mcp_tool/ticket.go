package mcp_tool

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"
)

const ticketToolID = "ticket_query"

var (
	ticketRegions           = []string{"华东", "华南", "华北", "西南", "西北"}
	ticketProducts          = []string{"企业版", "专业版", "基础版"}
	ticketPriorities        = []string{"紧急", "高", "中", "低"}
	ticketCategories        = []string{"功能异常", "性能问题", "安装部署", "使用咨询", "数据问题", "权限问题"}
	ticketCustomersByRegion = map[string][]string{
		"华东": []string{"腾讯科技", "阿里巴巴", "字节跳动", "网易公司"},
		"华南": []string{"美团点评", "京东集团", "小米科技", "格力电器"},
		"华北": []string{"百度在线", "华为技术", "中兴通讯", "用友网络"},
		"西南": []string{"科大讯飞", "金蝶软件", "三一重工", "中联重科"},
		"西北": []string{"浪潮集团", "东软集团", "美的集团", "海尔智家"},
	}
	ticketEngineersByRegion = map[string][]string{
		"华东": []string{"工程师A1", "工程师A2"},
		"华南": []string{"工程师B1", "工程师B2"},
		"华北": []string{"工程师C1", "工程师C2"},
		"西南": []string{"工程师D1", "工程师D2"},
		"西北": []string{"工程师E1", "工程师E2"},
	}
	ticketIssueTemplates = []string{
		"系统登录后页面白屏无法操作",
		"报表导出功能超时失败",
		"用户权限配置不生效",
		"数据同步延迟超过预期",
		"批量导入数据格式校验异常",
		"API接口调用返回500错误",
		"定时任务未按计划执行",
		"搜索功能结果不准确",
		"通知消息无法正常推送",
		"文件上传大小限制配置无效",
		"仪表盘数据展示不一致",
		"多租户数据隔离存在问题",
		"审批流程节点卡住无法流转",
		"移动端页面适配显示异常",
		"数据备份任务执行失败",
	}
)

type ticketRecord struct {
	ticketID   string
	region     string
	customer   string
	product    string
	title      string
	category   string
	priority   string
	status     string
	engineer   string
	createDate string
}

func newTicketQueryTool() *Tool {
	return &Tool{
		Name:        ticketToolID,
		Description: "查询客户技术支持工单数据，支持按地区、状态、优先级、产品、客户等维度筛选，支持汇总概览、工单列表、统计分析等多种查询",
		Domains:     []string{"ticket"},
		Properties: map[string]propDesc{
			"region":       {Type: "string", Description: "地区筛选：华东、华南、华北、西南、西北，不填则查询全国"},
			"status":       {Type: "string", Description: "工单状态筛选：待处理、处理中、已解决、已关闭，不填则查询全部状态"},
			"priority":     {Type: "string", Description: "优先级筛选：紧急、高、中、低，不填则查询全部优先级"},
			"product":      {Type: "string", Description: "产品筛选：企业版、专业版、基础版，不填则查询全部产品"},
			"customerName": {Type: "string", Description: "客户名称关键字，支持模糊匹配"},
			"queryType":    {Type: "string", Description: "查询类型：summary(汇总概览)、list(工单列表)、stats(统计分析)"},
			"limit":        {Type: "integer", Description: "返回记录数限制，默认10"},
		},
		Required: []string{},
		Execute: func(ctx context.Context, args map[string]interface{}) ([]toolContent, error) {
			_ = ctx
			region := strings.TrimSpace(stringArg(args, "region"))
			status := strings.TrimSpace(stringArg(args, "status"))
			priority := strings.TrimSpace(stringArg(args, "priority"))
			product := strings.TrimSpace(stringArg(args, "product"))
			customerName := strings.TrimSpace(stringArg(args, "customerName"))
			queryType := strings.TrimSpace(stringArg(args, "queryType"))
			limit := intArg(args, "limit")

			if queryType == "" {
				queryType = "summary"
			}
			if limit <= 0 {
				limit = 10
			}

			allData := getOrGenerateTicketData()
			filtered := filterTicketData(allData, region, status, priority, product, customerName)

			var result string
			switch queryType {
			case "list":
				result = buildTicketListResult(filtered, limit)
			case "stats":
				result = buildTicketStatsResult(filtered)
			default:
				result = buildTicketSummaryResult(filtered, region, status, priority, product)
			}
			return []toolContent{{Type: "text", Text: result}}, nil
		},
	}
}

func buildTicketSummaryResult(data []ticketRecord, region, status, priority, product string) string {
	total := len(data)
	var pending, inProgress, resolved, closed, urgent, high int
	for _, ticket := range data {
		switch ticket.status {
		case "待处理":
			pending++
		case "处理中":
			inProgress++
		case "已解决":
			resolved++
		case "已关闭":
			closed++
		}
		switch ticket.priority {
		case "紧急":
			urgent++
		case "高":
			high++
		}
	}

	var sb strings.Builder
	sb.WriteString("【客户工单汇总概览】\n\n")

	filters := make([]string, 0, 4)
	if region != "" {
		filters = append(filters, "地区: "+region)
	}
	if status != "" {
		filters = append(filters, "状态: "+status)
	}
	if priority != "" {
		filters = append(filters, "优先级: "+priority)
	}
	if product != "" {
		filters = append(filters, "产品: "+product)
	}
	if len(filters) > 0 {
		sb.WriteString("筛选条件: ")
		sb.WriteString(strings.Join(filters, "，"))
		sb.WriteString("\n\n")
	}

	sb.WriteString(fmt.Sprintf("工单总数: %d 个\n\n", total))
	sb.WriteString("【状态分布】\n")
	sb.WriteString(fmt.Sprintf("  待处理: %d 个\n", pending))
	sb.WriteString(fmt.Sprintf("  处理中: %d 个\n", inProgress))
	sb.WriteString(fmt.Sprintf("  已解决: %d 个\n", resolved))
	sb.WriteString(fmt.Sprintf("  已关闭: %d 个\n\n", closed))

	if total > 0 {
		resolveRate := float64(resolved+closed) * 100.0 / float64(total)
		sb.WriteString(fmt.Sprintf("解决率: %.1f%%\n", resolveRate))
	}

	if urgent+high > 0 {
		sb.WriteString(fmt.Sprintf("\n⚠ 紧急/高优先级工单: %d 个（紧急 %d，高 %d）\n", urgent+high, urgent, high))
	}

	if product == "" {
		byProduct := make(map[string]int)
		for _, ticket := range data {
			byProduct[ticket.product]++
		}
		if len(byProduct) > 0 {
			sb.WriteString("\n【按产品分布】\n")
			for _, entry := range sortTicketCountMap(byProduct) {
				sb.WriteString(fmt.Sprintf("  %s: %d 个\n", entry.key, entry.value))
			}
		}
	}

	if region == "" {
		byRegion := make(map[string]int)
		for _, ticket := range data {
			byRegion[ticket.region]++
		}
		if len(byRegion) > 0 {
			sb.WriteString("\n【按地区分布】\n")
			for _, entry := range sortTicketCountMap(byRegion) {
				sb.WriteString(fmt.Sprintf("  %s: %d 个\n", entry.key, entry.value))
			}
		}
	}

	return strings.TrimSpace(sb.String())
}

func buildTicketListResult(data []ticketRecord, limit int) string {
	sorted := append([]ticketRecord(nil), data...)
	sort.Slice(sorted, func(i, j int) bool {
		pi := ticketPriorityIndex(sorted[i].priority)
		pj := ticketPriorityIndex(sorted[j].priority)
		if pi != pj {
			return pi < pj
		}
		return sorted[i].createDate > sorted[j].createDate
	})
	if len(sorted) > limit {
		sorted = sorted[:limit]
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("【工单列表】共 %d 条，显示 %d 条（按优先级排序）\n\n", len(data), len(sorted)))
	for i, ticket := range sorted {
		sb.WriteString(fmt.Sprintf("%d. [%s] %s\n", i+1, ticket.ticketID, ticket.title))
		sb.WriteString(fmt.Sprintf("   客户: %s | 产品: %s | 地区: %s\n", ticket.customer, ticket.product, ticket.region))
		sb.WriteString(fmt.Sprintf("   优先级: %s | 状态: %s | 分类: %s\n", ticket.priority, ticket.status, ticket.category))
		sb.WriteString(fmt.Sprintf("   处理人: %s | 创建时间: %s\n\n", ticket.engineer, ticket.createDate))
	}
	return strings.TrimSpace(sb.String())
}

func buildTicketStatsResult(data []ticketRecord) string {
	var sb strings.Builder
	sb.WriteString("【工单统计分析】\n\n")
	if len(data) == 0 {
		sb.WriteString("暂无工单数据")
		return sb.String()
	}

	byCategory := make(map[string]int)
	byProduct := make(map[string][]ticketRecord)
	byEngineer := make(map[string]int)
	for _, ticket := range data {
		byCategory[ticket.category]++
		byProduct[ticket.product] = append(byProduct[ticket.product], ticket)
		if ticket.status == "待处理" || ticket.status == "处理中" {
			byEngineer[ticket.engineer]++
		}
	}

	sb.WriteString("【问题分类统计】\n")
	for _, entry := range sortTicketCountMap(byCategory) {
		percent := float64(entry.value) * 100.0 / float64(len(data))
		sb.WriteString(fmt.Sprintf("  %s: %d 个 (%.1f%%)\n", entry.key, entry.value, percent))
	}

	sb.WriteString("\n【各产品解决率】\n")
	for _, productName := range sortTicketProductKeys(byProduct) {
		tickets := byProduct[productName]
		var resolvedCount int
		for _, ticket := range tickets {
			if ticket.status == "已解决" || ticket.status == "已关闭" {
				resolvedCount++
			}
		}
		sb.WriteString(fmt.Sprintf("  %s: %.1f%% (%d/%d)\n",
			productName, float64(resolvedCount)*100.0/float64(len(tickets)), resolvedCount, len(tickets)))
	}

	sb.WriteString("\n【处理人工单量排名】\n")
	for _, entry := range sortTicketCountMap(byEngineer) {
		sb.WriteString(fmt.Sprintf("  %s: %d 个待处理\n", entry.key, entry.value))
	}

	return strings.TrimSpace(sb.String())
}

func filterTicketData(data []ticketRecord, region, status, priority, product, customerName string) []ticketRecord {
	filtered := make([]ticketRecord, 0, len(data))
	for _, ticket := range data {
		if region != "" && region != ticket.region {
			continue
		}
		if status != "" && status != ticket.status {
			continue
		}
		if priority != "" && priority != ticket.priority {
			continue
		}
		if product != "" && product != ticket.product {
			continue
		}
		if customerName != "" && !strings.Contains(ticket.customer, customerName) {
			continue
		}
		filtered = append(filtered, ticket)
	}
	return filtered
}

func getOrGenerateTicketData() []ticketRecord {
	today := time.Now()
	key := today.Format("2006-01-02")
	if ticketDataCache != nil && ticketDataCacheKey == key {
		return ticketDataCache
	}
	ticketDataCache = generateTicketData(today)
	ticketDataCacheKey = key
	return ticketDataCache
}

func generateTicketData(today time.Time) []ticketRecord {
	records := make([]ticketRecord, 0, 120)
	rng := rand.New(rand.NewSource(today.Unix()))
	ticketSeq := 1

	for d := 0; d < 30; d++ {
		date := today.AddDate(0, 0, -d)
		if date.Weekday() == time.Saturday || date.Weekday() == time.Sunday {
			continue
		}
		ticketsPerDay := 2 + rng.Intn(5)
		for i := 0; i < ticketsPerDay; i++ {
			region := ticketRegions[rng.Intn(len(ticketRegions))]
			customers := ticketCustomersByRegion[region]
			engineers := ticketEngineersByRegion[region]
			record := ticketRecord{
				ticketID:   fmt.Sprintf("TK-%s-%04d", today.Format("200601"), ticketSeq),
				region:     region,
				customer:   customers[rng.Intn(len(customers))],
				product:    ticketProducts[rng.Intn(len(ticketProducts))],
				title:      ticketIssueTemplates[rng.Intn(len(ticketIssueTemplates))],
				category:   ticketCategories[rng.Intn(len(ticketCategories))],
				engineer:   engineers[rng.Intn(len(engineers))],
				createDate: date.Format("2006-01-02"),
			}

			switch priorityWeight := rng.Intn(100); {
			case priorityWeight < 5:
				record.priority = "紧急"
			case priorityWeight < 20:
				record.priority = "高"
			case priorityWeight < 60:
				record.priority = "中"
			default:
				record.priority = "低"
			}

			statusWeight := rng.Intn(100)
			switch {
			case d > 7:
				if statusWeight < 80 {
					record.status = "已关闭"
				} else {
					record.status = "已解决"
				}
			case d > 3:
				switch {
				case statusWeight < 30:
					record.status = "已解决"
				case statusWeight < 60:
					record.status = "已关闭"
				case statusWeight < 85:
					record.status = "处理中"
				default:
					record.status = "待处理"
				}
			default:
				switch {
				case statusWeight < 35:
					record.status = "待处理"
				case statusWeight < 70:
					record.status = "处理中"
				case statusWeight < 90:
					record.status = "已解决"
				default:
					record.status = "已关闭"
				}
			}

			records = append(records, record)
			ticketSeq++
		}
	}

	return records
}

type ticketCountEntry struct {
	key   string
	value int
}

func sortTicketCountMap(values map[string]int) []ticketCountEntry {
	entries := make([]ticketCountEntry, 0, len(values))
	for key, value := range values {
		entries = append(entries, ticketCountEntry{key: key, value: value})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].value == entries[j].value {
			return entries[i].key < entries[j].key
		}
		return entries[i].value > entries[j].value
	})
	return entries
}

func sortTicketProductKeys(values map[string][]ticketRecord) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func ticketPriorityIndex(priority string) int {
	for i, item := range ticketPriorities {
		if item == priority {
			return i
		}
	}
	return len(ticketPriorities)
}

var (
	ticketDataCache    []ticketRecord
	ticketDataCacheKey string
)
