package rag

import (
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// ConditionEvaluator evaluates ingestion node conditions against an IngestionContext.
// It supports the JSON rule shape used by the Java ConditionEvaluator: all/any/not
// composition and field/operator/value comparisons.
type ConditionEvaluator struct{}

// NewConditionEvaluator creates a default ingestion condition evaluator.
func NewConditionEvaluator() *ConditionEvaluator {
	return &ConditionEvaluator{}
}

// Evaluate returns whether the condition allows the node to execute.
func (e *ConditionEvaluator) Evaluate(ctx *IngestionContext, condition any) bool {
	if condition == nil {
		return true
	}
	switch c := condition.(type) {
	case bool:
		return c
	case string:
		return e.evaluateString(ctx, c)
	case map[string]any:
		return e.evaluateObject(ctx, c)
	default:
		return true
	}
}

func (e *ConditionEvaluator) evaluateString(ctx *IngestionContext, condition string) bool {
	condition = strings.TrimSpace(condition)
	if condition == "" {
		return true
	}
	if v, err := strconv.ParseBool(condition); err == nil {
		return v
	}
	if strings.HasPrefix(condition, "!") {
		return !e.evaluateString(ctx, strings.TrimSpace(strings.TrimPrefix(condition, "!")))
	}
	if parts := splitConditionExpression(condition, "||"); len(parts) > 1 {
		for _, part := range parts {
			if e.evaluateString(ctx, part) {
				return true
			}
		}
		return false
	}
	if parts := splitConditionExpression(condition, "&&"); len(parts) > 1 {
		for _, part := range parts {
			if !e.evaluateString(ctx, part) {
				return false
			}
		}
		return true
	}
	if field, arg, ok := parseConditionMethod(condition, ".contains("); ok {
		return conditionContains(readConditionField(ctx, normalizeConditionField(field)), arg)
	}
	if field, arg, ok := parseConditionMethod(condition, ".matches("); ok {
		return compareConditionValue(readConditionField(ctx, normalizeConditionField(field)), arg, "regex")
	}
	for _, op := range []string{"!=", "==", ">=", "<=", ">", "<"} {
		if left, right, ok := splitConditionComparison(condition, op); ok {
			operator := mapConditionOperator(op)
			if strings.EqualFold(right, "null") {
				if op == "!=" {
					return readConditionField(ctx, normalizeConditionField(left)) != nil
				}
				if op == "==" {
					return readConditionField(ctx, normalizeConditionField(left)) == nil
				}
			}
			return compareConditionValue(readConditionField(ctx, normalizeConditionField(left)), parseConditionLiteral(right), operator)
		}
	}
	return false
}

func (e *ConditionEvaluator) evaluateObject(ctx *IngestionContext, condition map[string]any) bool {
	if all, ok := condition["all"]; ok {
		for _, item := range anySlice(all) {
			if !e.Evaluate(ctx, item) {
				return false
			}
		}
		return true
	}
	if any, ok := condition["any"]; ok {
		items := anySlice(any)
		if len(items) == 0 {
			return true
		}
		for _, item := range items {
			if e.Evaluate(ctx, item) {
				return true
			}
		}
		return false
	}
	if not, ok := condition["not"]; ok {
		return !e.Evaluate(ctx, not)
	}

	field := strings.TrimSpace(stringValue(condition["field"]))
	if field == "" {
		return true
	}
	operator := strings.TrimSpace(stringValue(condition["operator"]))
	if operator == "" {
		operator = "eq"
	}
	return compareConditionValue(readConditionField(ctx, field), condition["value"], operator)
}

func compareConditionValue(left any, right any, operator string) bool {
	switch strings.ToLower(operator) {
	case "ne":
		return !conditionEqual(left, right)
	case "in":
		return conditionContains(right, left) || conditionContains(left, right)
	case "contains":
		return conditionContains(left, right)
	case "regex":
		if left == nil || right == nil {
			return false
		}
		re, err := regexp.Compile(strings.TrimSpace(stringValue(right)))
		return err == nil && re.MatchString(stringValue(left))
	case "gt":
		return compareConditionNumber(left, right) > 0
	case "gte":
		return compareConditionNumber(left, right) >= 0
	case "lt":
		return compareConditionNumber(left, right) < 0
	case "lte":
		return compareConditionNumber(left, right) <= 0
	case "exists":
		return left != nil
	case "not_exists":
		return left == nil
	default:
		return conditionEqual(left, right)
	}
}

func readConditionField(ctx *IngestionContext, path string) any {
	var current any = ctx
	for _, part := range splitConditionPath(path) {
		part = strings.TrimSpace(part)
		if part == "" || current == nil {
			return nil
		}
		current = readConditionPart(current, part)
	}
	return current
}

func readConditionPart(current any, part string) any {
	value := reflect.ValueOf(current)
	for value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}

	switch value.Kind() {
	case reflect.Struct:
		field := value.FieldByNameFunc(func(name string) bool {
			return strings.EqualFold(name, part)
		})
		if !field.IsValid() || !field.CanInterface() {
			return nil
		}
		return field.Interface()
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return nil
		}
		key := reflect.ValueOf(part).Convert(value.Type().Key())
		if item := value.MapIndex(key); item.IsValid() {
			return item.Interface()
		}
		iter := value.MapRange()
		for iter.Next() {
			if strings.EqualFold(iter.Key().String(), part) {
				return iter.Value().Interface()
			}
		}
	}
	return nil
}

func splitConditionExpression(expr, sep string) []string {
	parts := make([]string, 0, 2)
	start := 0
	quote := rune(0)
	for i := 0; i+len(sep) <= len(expr); i++ {
		ch := rune(expr[i])
		if (ch == '\'' || ch == '"') && (i == 0 || expr[i-1] != '\\') {
			if quote == 0 {
				quote = ch
			} else if quote == ch {
				quote = 0
			}
			continue
		}
		if quote == 0 && strings.HasPrefix(expr[i:], sep) {
			parts = append(parts, strings.TrimSpace(expr[start:i]))
			start = i + len(sep)
			i += len(sep) - 1
		}
	}
	if len(parts) == 0 {
		return nil
	}
	parts = append(parts, strings.TrimSpace(expr[start:]))
	return parts
}

func parseConditionMethod(expr, method string) (string, any, bool) {
	idx := strings.Index(expr, method)
	if idx < 0 || !strings.HasSuffix(expr, ")") {
		return "", nil, false
	}
	field := strings.TrimSpace(expr[:idx])
	rawArg := strings.TrimSpace(expr[idx+len(method) : len(expr)-1])
	return field, parseConditionLiteral(rawArg), true
}

func splitConditionComparison(expr, op string) (string, string, bool) {
	idx := strings.Index(expr, op)
	if idx <= 0 {
		return "", "", false
	}
	return strings.TrimSpace(expr[:idx]), strings.TrimSpace(expr[idx+len(op):]), true
}

func mapConditionOperator(op string) string {
	switch op {
	case "!=":
		return "ne"
	case ">":
		return "gt"
	case ">=":
		return "gte"
	case "<":
		return "lt"
	case "<=":
		return "lte"
	default:
		return "eq"
	}
}

func normalizeConditionField(path string) string {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "#ctx.")
	path = strings.TrimPrefix(path, "ctx.")
	return path
}

func parseConditionLiteral(raw string) any {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 2 {
		quote := raw[0]
		if (quote == '\'' || quote == '"') && raw[len(raw)-1] == quote {
			return strings.ReplaceAll(raw[1:len(raw)-1], `\`+string(quote), string(quote))
		}
	}
	if strings.EqualFold(raw, "null") {
		return nil
	}
	if v, err := strconv.ParseBool(raw); err == nil {
		return v
	}
	if v, err := strconv.ParseFloat(raw, 64); err == nil {
		return v
	}
	return raw
}

func splitConditionPath(path string) []string {
	parts := make([]string, 0)
	for _, part := range strings.Split(path, ".") {
		part = strings.TrimSpace(part)
		for {
			open := strings.Index(part, "[")
			if open < 0 || !strings.HasSuffix(part, "]") {
				break
			}
			if open > 0 {
				parts = append(parts, part[:open])
			}
			key := strings.TrimSpace(part[open+1 : len(part)-1])
			if len(key) >= 2 && (key[0] == '\'' || key[0] == '"') && key[len(key)-1] == key[0] {
				key = key[1 : len(key)-1]
			}
			parts = append(parts, key)
			part = ""
			break
		}
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func conditionEqual(left any, right any) bool {
	leftNumber, leftOK := conditionFloat(left)
	rightNumber, rightOK := conditionFloat(right)
	if leftOK && rightOK {
		return leftNumber == rightNumber
	}
	return strings.TrimSpace(stringValue(left)) == strings.TrimSpace(stringValue(right))
}

func conditionContains(left any, right any) bool {
	if left == nil || right == nil {
		return false
	}
	if text, ok := left.(string); ok {
		return strings.Contains(text, stringValue(right))
	}
	value := reflect.ValueOf(left)
	for value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if value.IsNil() {
			return false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Slice && value.Kind() != reflect.Array {
		return false
	}
	for i := range value.Len() {
		if conditionEqual(value.Index(i).Interface(), right) {
			return true
		}
	}
	return false
}

func compareConditionNumber(left any, right any) int {
	leftNumber, leftOK := conditionFloat(left)
	rightNumber, rightOK := conditionFloat(right)
	if !leftOK || !rightOK {
		return 0
	}
	if leftNumber > rightNumber {
		return 1
	}
	if leftNumber < rightNumber {
		return -1
	}
	return 0
}

func conditionFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return n, err == nil
	default:
		return 0, false
	}
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func anySlice(value any) []any {
	rv := reflect.ValueOf(value)
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil
	}
	result := make([]any, 0, rv.Len())
	for i := range rv.Len() {
		result = append(result, rv.Index(i).Interface())
	}
	return result
}
