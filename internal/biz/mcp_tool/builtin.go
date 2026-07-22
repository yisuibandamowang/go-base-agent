package mcp_tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultYouComAPIURL = "https://ydc-index.io/v1/search"
	defaultYouComCount  = 5
	maxYouComCount      = 20
)

func builtinTools() []*Tool {
	tools := []*Tool{
		newSalesQueryTool(),
		newTicketQueryTool(),
		newWeatherQueryTool(),
	}
	if apiKey := strings.TrimSpace(os.Getenv("YDC_API_KEY")); apiKey != "" {
		tools = append(tools, newYouComSearchTool(defaultYouComAPIURL, apiKey, nil))
	}
	return tools
}

func newSalesQueryTool() *Tool {
	return &Tool{
		Name:        "sales_query",
		Description: "查询软件销售数据，支持按地区、时间、产品、销售人员等维度筛选，支持汇总统计、排名、明细列表等多种查询",
		Domains:     []string{"sales"},
		Properties: map[string]propDesc{
			"region":      {Type: "string", Description: "地区筛选：华东、华南、华北、西南、西北，不填则查询全国"},
			"period":      {Type: "string", Description: "时间段：本月、上月、本季度、上季度、本年，默认本月"},
			"product":     {Type: "string", Description: "产品筛选：企业版、专业版、基础版，不填则查询全部产品"},
			"salesPerson": {Type: "string", Description: "销售人员姓名，不填则查询全部销售"},
			"queryType":   {Type: "string", Description: "查询类型：summary(汇总)、ranking(排名)、detail(明细)、trend(趋势)"},
			"limit":       {Type: "integer", Description: "返回记录数限制，默认10"},
		},
		Required: []string{},
		Execute: func(ctx context.Context, args map[string]interface{}) ([]toolContent, error) {
			_ = ctx
			region := strings.TrimSpace(stringArg(args, "region"))
			period := strings.TrimSpace(stringArg(args, "period"))
			product := strings.TrimSpace(stringArg(args, "product"))
			salesPerson := strings.TrimSpace(stringArg(args, "salesPerson"))
			queryType := strings.TrimSpace(stringArg(args, "queryType"))
			limit := intArg(args, "limit")

			if period == "" {
				period = "本月"
			}
			if queryType == "" {
				queryType = "summary"
			}
			if limit <= 0 {
				limit = 10
			}

			data := generateSalesData(period)
			filtered := filterSalesRecords(data, region, product, salesPerson)

			var result string
			switch queryType {
			case "ranking":
				result = buildSalesRankingResult(filtered, region, period, limit)
			case "detail":
				result = buildSalesDetailResult(filtered, region, period, limit)
			case "trend":
				result = buildSalesTrendResult(filtered, region, period)
			default:
				result = buildSalesSummaryResult(filtered, region, period, product, salesPerson)
			}
			return []toolContent{{Type: "text", Text: result}}, nil
		},
	}
}

func newWeatherQueryTool() *Tool {
	return &Tool{
		Name:        "weather_query",
		Description: "查询城市天气信息，支持查看当前实时天气和未来多天天气预报，包含温度、湿度、风力、天气状况等信息",
		Properties: map[string]propDesc{
			"city":      {Type: "string", Description: "城市名称，如北京、上海、广州等"},
			"queryType": {Type: "string", Description: "查询类型：current(当前天气)、forecast(未来预报)"},
			"days":      {Type: "integer", Description: "预报天数，仅forecast模式有效，默认3天，最多7天"},
		},
		Required: []string{"city"},
		Execute: func(ctx context.Context, args map[string]interface{}) ([]toolContent, error) {
			_ = ctx
			city := strings.TrimSpace(stringArg(args, "city"))
			queryType := strings.TrimSpace(stringArg(args, "queryType"))
			days := intArg(args, "days")

			if city == "" {
				return errorContent("请提供城市名称"), nil
			}
			if queryType == "" {
				queryType = "current"
			}
			if days <= 0 {
				days = 3
			}
			if days > 7 {
				days = 7
			}
			if _, ok := cityCoordinates[city]; !ok {
				keys := make([]string, 0, len(cityCoordinates))
				for key := range cityCoordinates {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				return errorContent("暂不支持查询该城市，当前支持：" + strings.Join(keys, "、")), nil
			}

			switch queryType {
			case "forecast":
				return []toolContent{{Type: "text", Text: buildWeatherForecastResult(city, days)}}, nil
			default:
				return []toolContent{{Type: "text", Text: buildWeatherCurrentResult(city)}}, nil
			}
		},
	}
}

func newYouComSearchTool(apiURL, apiKey string, client *http.Client) *Tool {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Tool{
		Name:        "youcom_search",
		Description: "基于 You.com Search API 的联网搜索，返回带来源链接和摘录片段的网页与新闻结果。需要配置 YDC_API_KEY 环境变量",
		Properties: map[string]propDesc{
			"query":     {Type: "string", Description: "检索关键词或问题"},
			"count":     {Type: "integer", Description: "最多返回的结果条数（网页+新闻合计），默认 5，最大 20"},
			"freshness": {Type: "string", Description: "结果时效过滤：day、week、month、year，不传则不限"},
		},
		Required: []string{"query"},
		Execute: func(ctx context.Context, args map[string]interface{}) ([]toolContent, error) {
			query := strings.TrimSpace(stringArg(args, "query"))
			count := intArg(args, "count")
			freshness := strings.TrimSpace(stringArg(args, "freshness"))

			if query == "" {
				return errorContent("请提供检索关键词 query"), nil
			}
			if count <= 0 {
				count = defaultYouComCount
			}
			if count > maxYouComCount {
				count = maxYouComCount
			}
			if freshness != "" && !isValidFreshness(freshness) {
				return errorContent("freshness 参数不合法，可选值：day、week、month、year"), nil
			}
			if strings.TrimSpace(apiKey) == "" {
				return errorContent("You.com 联网搜索未配置：请先设置环境变量 YDC_API_KEY"), nil
			}

			text, err := doYouComSearch(ctx, client, apiURL, apiKey, query, count, freshness)
			if err != nil {
				return errorContent("搜索失败: " + err.Error()), nil
			}
			return []toolContent{{Type: "text", Text: text}}, nil
		},
	}
}

func doYouComSearch(ctx context.Context, client *http.Client, apiURL, apiKey, query string, count int, freshness string) (string, error) {
	reqURL, err := url.Parse(apiURL)
	if err != nil {
		return "", fmt.Errorf("invalid api url: %w", err)
	}
	q := reqURL.Query()
	q.Set("query", query)
	q.Set("count", strconv.Itoa(count))
	if freshness != "" {
		q.Set("freshness", freshness)
	}
	reqURL.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("X-API-Key", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("call api: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("api returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	return formatYouComResults(body, count)
}

func formatYouComResults(body []byte, count int) (string, error) {
	var payload struct {
		Results struct {
			Web  []youComItem `json:"web"`
			News []youComItem `json:"news"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	items := append(payload.Results.Web, payload.Results.News...)
	if len(items) == 0 {
		return "未检索到相关结果，请尝试更换关键词。", nil
	}
	if count > 0 && len(items) > count {
		items = items[:count]
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("检索完成，共 %d 条结果：\n\n", len(items)))
	for i, item := range items {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, firstNonEmpty(item.Title, "(无标题)")))
		if item.URL != "" {
			sb.WriteString("   链接: ")
			sb.WriteString(item.URL)
			sb.WriteByte('\n')
		}
		excerpt := firstNonEmpty(item.Description, firstSnippet(item.Snippets))
		if excerpt != "" {
			sb.WriteString("   摘录: ")
			sb.WriteString(excerpt)
			sb.WriteByte('\n')
		}
		sb.WriteByte('\n')
	}
	return strings.TrimSpace(sb.String()), nil
}

func buildSalesSummaryResult(data []salesRecord, region, period, product, salesPerson string) string {
	totalAmount := 0.0
	byProduct := make(map[string]float64)
	byRegion := make(map[string]float64)
	for _, record := range data {
		totalAmount += record.amount
		byProduct[record.product] += record.amount
		byRegion[record.region] += record.amount
	}

	orderCount := len(data)
	avgAmount := 0.0
	if orderCount > 0 {
		avgAmount = totalAmount / float64(orderCount)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("【%s 销售数据汇总】\n\n", period))
	filters := make([]string, 0, 3)
	if region != "" {
		filters = append(filters, "地区: "+region)
	}
	if product != "" {
		filters = append(filters, "产品: "+product)
	}
	if salesPerson != "" {
		filters = append(filters, "销售: "+salesPerson)
	}
	if len(filters) > 0 {
		sb.WriteString("筛选条件: ")
		sb.WriteString(strings.Join(filters, "，"))
		sb.WriteString("\n\n")
	}
	sb.WriteString(fmt.Sprintf("总销售额: ¥%.2f 万\n", totalAmount))
	sb.WriteString(fmt.Sprintf("成交订单: %d 笔\n", orderCount))
	sb.WriteString(fmt.Sprintf("平均单价: ¥%.2f 万\n", avgAmount))

	if product == "" && len(byProduct) > 0 {
		sb.WriteString("\n【按产品分布】\n")
		for _, entry := range sortAmountMap(byProduct) {
			percent := 0.0
			if totalAmount > 0 {
				percent = entry.value / totalAmount * 100
			}
			sb.WriteString(fmt.Sprintf("  %s: ¥%.2f 万 (%.1f%%)\n", entry.key, entry.value, percent))
		}
	}
	if region == "" && len(byRegion) > 0 {
		sb.WriteString("\n【按地区分布】\n")
		for _, entry := range sortAmountMap(byRegion) {
			percent := 0.0
			if totalAmount > 0 {
				percent = entry.value / totalAmount * 100
			}
			sb.WriteString(fmt.Sprintf("  %s: ¥%.2f 万 (%.1f%%)\n", entry.key, entry.value, percent))
		}
	}
	return strings.TrimSpace(sb.String())
}

func buildSalesRankingResult(data []salesRecord, region, period string, limit int) string {
	bySales := make(map[string]float64)
	for _, record := range data {
		bySales[record.salesPerson] += record.amount
	}

	ranking := sortAmountMap(bySales)
	if len(ranking) > limit {
		ranking = ranking[:limit]
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("【%s", period))
	if region != "" {
		sb.WriteString(" ")
		sb.WriteString(region)
	}
	sb.WriteString(" 销售排名】\n\n")
	if len(ranking) == 0 {
		sb.WriteString("暂无销售数据")
		return strings.TrimSpace(sb.String())
	}
	for i, entry := range ranking {
		sb.WriteString(fmt.Sprintf("第%d名: %s - ¥%.2f 万\n", i+1, entry.key, entry.value))
	}
	return strings.TrimSpace(sb.String())
}

func buildSalesDetailResult(data []salesRecord, region, period string, limit int) string {
	records := append([]salesRecord(nil), data...)
	sort.Slice(records, func(i, j int) bool {
		return records[i].amount > records[j].amount
	})
	if len(records) > limit {
		records = records[:limit]
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("【%s", period))
	if region != "" {
		sb.WriteString(" ")
		sb.WriteString(region)
	}
	sb.WriteString(" 销售明细】\n\n")
	sb.WriteString(fmt.Sprintf("共 %d 条记录，显示金额最高的 %d 条：\n\n", len(data), len(records)))
	for i, record := range records {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, record.customer))
		sb.WriteString(fmt.Sprintf("   产品: %s | 金额: ¥%.2f 万\n", record.product, record.amount))
		sb.WriteString(fmt.Sprintf("   销售: %s | 地区: %s | 日期: %s\n\n", record.salesPerson, record.region, record.date.Format("2006-01-02")))
	}
	return strings.TrimSpace(sb.String())
}

func buildSalesTrendResult(data []salesRecord, region, period string) string {
	byWeek := make(map[int]float64)
	for _, record := range data {
		week := (record.date.Day()-1)/7 + 1
		byWeek[week] += record.amount
	}

	weeks := make([]int, 0, len(byWeek))
	for week := range byWeek {
		weeks = append(weeks, week)
	}
	sort.Ints(weeks)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("【%s", period))
	if region != "" {
		sb.WriteString(" ")
		sb.WriteString(region)
	}
	sb.WriteString(" 销售趋势】\n\n")
	if len(weeks) == 0 {
		sb.WriteString("暂无数据")
		return strings.TrimSpace(sb.String())
	}
	for _, week := range weeks {
		sb.WriteString(fmt.Sprintf("第%d周: ¥%.2f 万\n", week, byWeek[week]))
	}
	return strings.TrimSpace(sb.String())
}

func generateSalesData(period string) []salesRecord {
	start, end := salesPeriodRange(period)
	seed := start.Unix() ^ end.Unix() ^ int64(len(period))
	rng := rand.New(rand.NewSource(seed))

	records := make([]salesRecord, 0, 48)
	for i := 0; i < 48; i++ {
		region := salesRegions[rng.Intn(len(salesRegions))]
		product := salesProducts[rng.Intn(len(salesProducts))]
		salesPeople := salesByRegion[region]
		salesPerson := salesPeople[rng.Intn(len(salesPeople))]
		amount := float64(500+rng.Intn(30000)) / 100.0
		date := randomDateBetween(rng, start, end)
		customer := salesCustomers[rng.Intn(len(salesCustomers))]
		records = append(records, salesRecord{
			region:      region,
			product:     product,
			salesPerson: salesPerson,
			customer:    customer,
			amount:      amount,
			date:        date,
		})
	}
	return records
}

func filterSalesRecords(data []salesRecord, region, product, salesPerson string) []salesRecord {
	filtered := make([]salesRecord, 0, len(data))
	for _, record := range data {
		if region != "" && region != record.region {
			continue
		}
		if product != "" && product != record.product {
			continue
		}
		if salesPerson != "" && salesPerson != record.salesPerson {
			continue
		}
		filtered = append(filtered, record)
	}
	return filtered
}

func salesPeriodRange(period string) (time.Time, time.Time) {
	now := time.Now()
	loc := now.Location()
	year, month, _ := now.Date()

	monthStart := time.Date(year, month, 1, 0, 0, 0, 0, loc)
	monthNum := int(month)
	quarterStart := time.Date(year, time.Month(monthNum-((monthNum-1)%3)), 1, 0, 0, 0, 0, loc)
	yearStart := time.Date(year, 1, 1, 0, 0, 0, 0, loc)

	switch period {
	case "上月":
		start := monthStart.AddDate(0, -1, 0)
		return start, monthStart
	case "本季度":
		return quarterStart, quarterStart.AddDate(0, 3, 0)
	case "上季度":
		start := quarterStart.AddDate(0, -3, 0)
		return start, quarterStart
	case "本年":
		return yearStart, yearStart.AddDate(1, 0, 0)
	default:
		return monthStart, monthStart.AddDate(0, 1, 0)
	}
}

func randomDateBetween(rng *rand.Rand, start, end time.Time) time.Time {
	if !end.After(start) {
		return start
	}
	days := int(end.Sub(start).Hours() / 24)
	if days <= 0 {
		return start
	}
	return start.AddDate(0, 0, rng.Intn(days))
}

type amountEntry struct {
	key   string
	value float64
}

func sortAmountMap(values map[string]float64) []amountEntry {
	entries := make([]amountEntry, 0, len(values))
	for key, value := range values {
		entries = append(entries, amountEntry{key: key, value: value})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].value == entries[j].value {
			return entries[i].key < entries[j].key
		}
		return entries[i].value > entries[j].value
	})
	return entries
}

type salesRecord struct {
	region      string
	product     string
	salesPerson string
	customer    string
	amount      float64
	date        time.Time
}

var salesRegions = []string{"华东", "华南", "华北", "西南", "西北"}

var salesProducts = []string{"企业版", "专业版", "基础版"}

var salesByRegion = map[string][]string{
	"华东": []string{"张三", "李四", "王五"},
	"华南": []string{"赵六", "钱七", "孙八"},
	"华北": []string{"周九", "吴十", "郑冬"},
	"西南": []string{"陈春", "林夏", "黄秋"},
	"西北": []string{"刘一", "杨二", "马三"},
}

var salesCustomers = []string{
	"腾讯科技", "阿里巴巴", "字节跳动", "美团点评", "京东集团",
	"百度在线", "网易公司", "小米科技", "华为技术", "中兴通讯",
	"用友网络", "金蝶软件", "浪潮集团", "东软集团", "科大讯飞",
	"三一重工", "中联重科", "格力电器", "美的集团", "海尔智家",
}

type weatherData struct {
	weatherType   string
	currentTemp   int
	highTemp      int
	lowTemp       int
	humidity      int
	windDirection string
	windLevel     string
	airQuality    string
}

var cityCoordinates = map[string][2]float64{
	"北京":  {39.9, 116.4},
	"上海":  {31.2, 121.5},
	"广州":  {23.1, 113.3},
	"深圳":  {22.5, 114.1},
	"杭州":  {30.3, 120.2},
	"成都":  {30.6, 104.1},
	"武汉":  {30.6, 114.3},
	"南京":  {32.1, 118.8},
	"西安":  {34.3, 108.9},
	"重庆":  {29.6, 106.5},
	"长沙":  {28.2, 112.9},
	"天津":  {39.1, 117.2},
	"苏州":  {31.3, 120.6},
	"郑州":  {34.7, 113.6},
	"青岛":  {36.1, 120.4},
	"大连":  {38.9, 121.6},
	"厦门":  {24.5, 118.1},
	"昆明":  {25.0, 102.7},
	"哈尔滨": {45.8, 126.5},
	"三亚":  {18.3, 109.5},
}

var weatherTypesSpring = []string{"晴", "多云", "阴", "小雨", "阵雨", "多云转晴"}
var weatherTypesSummer = []string{"晴", "多云", "雷阵雨", "大雨", "暴雨", "多云转阴"}
var weatherTypesAutumn = []string{"晴", "多云", "阴", "小雨", "晴转多云", "多云转晴"}
var weatherTypesWinter = []string{"晴", "多云", "阴", "小雪", "中雪", "晴转多云", "雾"}

func buildWeatherCurrentResult(city string) string {
	today := time.Now()
	weather := generateWeatherForDate(city, today)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("【%s 今日天气】\n\n", city))
	sb.WriteString(fmt.Sprintf("日期: %s\n", today.Format("2006年01月02日")))
	sb.WriteString(fmt.Sprintf("天气: %s\n", weather.weatherType))
	sb.WriteString(fmt.Sprintf("当前温度: %d°C\n", weather.currentTemp))
	sb.WriteString(fmt.Sprintf("最高温度: %d°C\n", weather.highTemp))
	sb.WriteString(fmt.Sprintf("最低温度: %d°C\n", weather.lowTemp))
	sb.WriteString(fmt.Sprintf("相对湿度: %d%%\n", weather.humidity))
	sb.WriteString(fmt.Sprintf("风向: %s\n", weather.windDirection))
	sb.WriteString(fmt.Sprintf("风力: %s\n", weather.windLevel))
	sb.WriteString(fmt.Sprintf("空气质量: %s\n", weather.airQuality))

	if strings.Contains(weather.weatherType, "雨") || strings.Contains(weather.weatherType, "雪") {
		sb.WriteString("\n提示: 今日有降水，出行请携带雨具。")
	} else if weather.highTemp >= 35 {
		sb.WriteString("\n提示: 今日高温，注意防暑降温。")
	} else if weather.lowTemp <= 0 {
		sb.WriteString("\n提示: 今日气温较低，注意防寒保暖。")
	}

	return strings.TrimSpace(sb.String())
}

func buildWeatherForecastResult(city string, days int) string {
	today := time.Now()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("【%s 未来%d天天气预报】\n\n", city, days))
	for i := 0; i < days; i++ {
		date := today.AddDate(0, 0, i)
		weather := generateWeatherForDate(city, date)
		dayLabel := "今天"
		switch i {
		case 1:
			dayLabel = "明天"
		case 2:
			dayLabel = "后天"
		default:
			if i > 2 {
				dayLabel = date.Format("01月02日")
			}
		}
		sb.WriteString(fmt.Sprintf("📅 %s（%s）\n", dayLabel, date.Format("01-02")))
		sb.WriteString(fmt.Sprintf("   天气: %s | 温度: %d°C ~ %d°C\n", weather.weatherType, weather.lowTemp, weather.highTemp))
		sb.WriteString(fmt.Sprintf("   湿度: %d%% | %s %s\n\n", weather.humidity, weather.windDirection, weather.windLevel))
	}

	todayWeather := generateWeatherForDate(city, today)
	lastDayWeather := generateWeatherForDate(city, today.AddDate(0, 0, days-1))
	tempTrend := lastDayWeather.highTemp - todayWeather.highTemp
	if abs(tempTrend) >= 5 {
		sb.WriteString(fmt.Sprintf("趋势: 未来%d天气温%s，注意%s。",
			days,
			ifElse(tempTrend > 0, "逐渐升高", "逐渐下降"),
			ifElse(tempTrend > 0, "防暑", "保暖"),
		))
	}
	return strings.TrimSpace(sb.String())
}

func generateWeatherForDate(city string, date time.Time) weatherData {
	coords := cityCoordinates[city]
	latitude := coords[0]
	seed := date.Unix()*31 + int64(cityHash(city))
	rng := rand.New(rand.NewSource(seed))

	month := date.Month()
	season := 3
	if month >= 3 && month <= 5 {
		season = 0
	} else if month >= 6 && month <= 8 {
		season = 1
	} else if month >= 9 && month <= 11 {
		season = 2
	}

	baseTemp := 0.0
	switch season {
	case 0:
		baseTemp = 15 - (latitude-25)*0.5
	case 1:
		baseTemp = 30 - (latitude-25)*0.3
	case 2:
		baseTemp = 18 - (latitude-25)*0.5
	default:
		baseTemp = 5 - (latitude-25)*0.8
	}

	highTemp := int(baseTemp + 3 + float64(rng.Intn(6)))
	lowTemp := int(baseTemp - 3 - float64(rng.Intn(5)))
	if highTemp <= lowTemp {
		highTemp = lowTemp + 1
	}
	currentTemp := lowTemp + rng.Intn(maxInt(1, highTemp-lowTemp))

	weatherTypes := weatherTypesWinter
	switch season {
	case 0:
		weatherTypes = weatherTypesSpring
	case 1:
		weatherTypes = weatherTypesSummer
	case 2:
		weatherTypes = weatherTypesAutumn
	}
	weatherType := weatherTypes[rng.Intn(len(weatherTypes))]

	humidity := 40 + rng.Intn(30)
	if season == 1 {
		humidity = 60 + rng.Intn(30)
	} else if season == 3 {
		humidity = 20 + rng.Intn(30)
	}
	if strings.Contains(weatherType, "雨") || strings.Contains(weatherType, "雪") {
		humidity = minInt(95, humidity+20)
	}

	directions := []string{"东风", "南风", "西风", "北风", "东南风", "西北风", "东北风", "西南风"}
	windDirection := directions[rng.Intn(len(directions))]
	windForce := 1 + rng.Intn(5)
	windLevel := fmt.Sprintf("%d-%d级", windForce, windForce+1)

	aqiBase := 30 + rng.Intn(120)
	if latitude > 35 {
		aqiBase += 20
	}
	airQuality := "中度污染"
	switch {
	case aqiBase <= 50:
		airQuality = "优"
	case aqiBase <= 100:
		airQuality = "良"
	case aqiBase <= 150:
		airQuality = "轻度污染"
	}

	return weatherData{
		weatherType:   weatherType,
		currentTemp:   currentTemp,
		highTemp:      highTemp,
		lowTemp:       lowTemp,
		humidity:      humidity,
		windDirection: windDirection,
		windLevel:     windLevel,
		airQuality:    airQuality,
	}
}

func cityHash(city string) uint32 {
	sum := sha256.Sum256([]byte(city))
	return hexToUint32(hex.EncodeToString(sum[:4]))
}

func hexToUint32(s string) uint32 {
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0
	}
	return uint32(v)
}

func stringArg(args map[string]interface{}, key string) string {
	if args == nil {
		return ""
	}
	if v, ok := args[key]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

func intArg(args map[string]interface{}, key string) int {
	if args == nil {
		return 0
	}
	if v, ok := args[key]; ok && v != nil {
		switch n := v.(type) {
		case int:
			return n
		case int8:
			return int(n)
		case int16:
			return int(n)
		case int32:
			return int(n)
		case int64:
			return int(n)
		case float32:
			return int(n)
		case float64:
			return int(n)
		case json.Number:
			i, _ := n.Int64()
			return int(i)
		default:
			i, err := strconv.Atoi(fmt.Sprint(v))
			if err == nil {
				return i
			}
		}
	}
	return 0
}

func isValidFreshness(value string) bool {
	switch value {
	case "day", "week", "month", "year":
		return true
	default:
		return false
	}
}

type youComItem struct {
	URL         string   `json:"url"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Snippets    []string `json:"snippets"`
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstSnippet(values []string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func ifElse(cond bool, whenTrue, whenFalse string) string {
	if cond {
		return whenTrue
	}
	return whenFalse
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
