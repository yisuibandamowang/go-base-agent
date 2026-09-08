package db

import (
	"database/sql/driver"
	"fmt"
)

// JSONText 是映射 JSONB 列的字符串类型。
//
// 空字符串在该类型上表示"未配置"，落库时必须写 NULL 而不是 ”——
// PostgreSQL 的 JSONB 列会直接拒绝空字符串（SQLSTATE 22P02）。
// 因此 Value 将空串转为 NULL，Scan 将 NULL 读回空串，保持 Go 侧语义不变。
type JSONText string

// Value 实现 driver.Valuer，空串落库为 NULL。
func (j JSONText) Value() (driver.Value, error) {
	if j == "" {
		return nil, nil
	}
	return string(j), nil
}

// Scan 实现 sql.Scanner，NULL 读回空串。
func (j *JSONText) Scan(value any) error {
	if value == nil {
		*j = ""
		return nil
	}
	switch v := value.(type) {
	case string:
		*j = JSONText(v)
	case []byte:
		*j = JSONText(v)
	default:
		return fmt.Errorf("JSONText: unsupported scan source type %T", value)
	}
	return nil
}
