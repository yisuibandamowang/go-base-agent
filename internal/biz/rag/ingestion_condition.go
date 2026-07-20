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
		v, err := strconv.ParseBool(strings.TrimSpace(c))
		return err == nil && v
	case map[string]any:
		return e.evaluateObject(ctx, c)
	default:
		return true
	}
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
	for _, part := range strings.Split(path, ".") {
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
