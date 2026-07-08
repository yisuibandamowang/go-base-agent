package convention

// PageResp 统一分页响应体，对齐 Java 版 PageResp。
type PageResp[T any] struct {
	Records []T   `json:"records"`
	Total   int64 `json:"total"`
	Size    int   `json:"size"`
	Current int   `json:"current"`
	Pages   int   `json:"pages"`
}

// NewPageResp 根据 GORM 查询结果构造分页响应。
func NewPageResp[T any](records []T, total int64, current, size int) *PageResp[T] {
	pages := int(total) / size
	if int(total)%size != 0 {
		pages++
	}
	if pages == 0 {
		pages = 1
	}
	return &PageResp[T]{
		Records: records,
		Total:   total,
		Size:    size,
		Current: current,
		Pages:   pages,
	}
}
