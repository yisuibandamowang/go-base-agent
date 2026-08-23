// Package config 提供初始化器专用 properties 配置加载能力。
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config 是已经完成环境变量展开的初始化器配置。
type Config struct {
	source string
	values map[string]string
}

// Load 从 UTF-8 properties 文件加载配置，并展开 ${ENV} 或 ${ENV:default}。
func Load(path string) (*Config, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("初始化器配置文件不能为空")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析初始化器配置路径失败: %w", err)
	}
	abs := filepath.Clean(absolute)
	file, err := os.Open(abs)
	if err != nil {
		return nil, fmt.Errorf("打开初始化器配置失败 %s: %w", abs, err)
	}
	defer file.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		separator := strings.IndexAny(line, "=:")
		if separator <= 0 {
			return nil, fmt.Errorf("初始化器配置第 %d 行缺少键值分隔符", lineNumber)
		}
		key := strings.TrimSpace(line[:separator])
		value := strings.TrimSpace(line[separator+1:])
		if key == "" {
			return nil, fmt.Errorf("初始化器配置第 %d 行键为空", lineNumber)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取初始化器配置失败 %s: %w", abs, err)
	}

	for key, value := range values {
		expanded, err := ExpandPlaceholders(value)
		if err != nil {
			return nil, fmt.Errorf("展开配置项 %s 失败: %w", key, err)
		}
		values[key] = strings.TrimSpace(expanded)
	}
	return &Config{source: abs, values: values}, nil
}

// Get 返回配置项，不存在或为空时返回 defaultValue。
func (c *Config) Get(key, defaultValue string) string {
	if c == nil {
		return defaultValue
	}
	value, ok := c.values[strings.TrimSpace(key)]
	if !ok || strings.TrimSpace(value) == "" {
		return defaultValue
	}
	return strings.TrimSpace(value)
}

// Require 返回必填配置项。
func (c *Config) Require(key string) (string, error) {
	value := c.Get(key, "")
	if value == "" {
		return "", fmt.Errorf("缺少配置项 %s，文件: %s", key, c.source)
	}
	return value, nil
}

// GetInt 返回整数配置项，不存在时返回默认值。
func (c *Config) GetInt(key string, defaultValue int) (int, error) {
	value := c.Get(key, "")
	if value == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("配置项不是整数 %s=%s: %w", key, value, err)
	}
	return parsed, nil
}

// GetBool 返回布尔配置项，不存在时返回默认值。
func (c *Config) GetBool(key string, defaultValue bool) (bool, error) {
	value := c.Get(key, "")
	if value == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("配置项不是布尔值 %s=%s: %w", key, value, err)
	}
	return parsed, nil
}

// GetList 返回逗号分隔的配置列表。
func (c *Config) GetList(key string) []string {
	value := c.Get(key, "")
	if value == "" {
		return nil
	}
	items := strings.Split(value, ",")
	result := make([]string, 0, len(items))
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

// ResolvePath 将配置项解析为相对于 properties 文件目录的绝对路径。
func (c *Config) ResolvePath(key string) (string, error) {
	value, err := c.Require(key)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value), nil
	}
	return filepath.Clean(filepath.Join(filepath.Dir(c.source), value)), nil
}

// Source 返回配置文件的绝对路径。
func (c *Config) Source() string {
	if c == nil {
		return ""
	}
	return c.source
}

// ExpandPlaceholders 展开 ${ENV} 和 ${ENV:default} 占位符。
func ExpandPlaceholders(input string) (string, error) {
	var builder strings.Builder
	for index := 0; index < len(input); {
		start := strings.Index(input[index:], "${")
		if start < 0 {
			builder.WriteString(input[index:])
			break
		}
		start += index
		builder.WriteString(input[index:start])
		endOffset := strings.IndexByte(input[start+2:], '}')
		if endOffset < 0 {
			return "", fmt.Errorf("环境变量占位符未闭合: %s", input)
		}
		end := start + 2 + endOffset
		expression := input[start+2 : end]
		separator := strings.IndexByte(expression, ':')
		name := expression
		fallback := ""
		hasFallback := false
		if separator >= 0 {
			name = expression[:separator]
			fallback = expression[separator+1:]
			hasFallback = true
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return "", fmt.Errorf("环境变量名称为空: %s", input)
		}
		value, exists := os.LookupEnv(name)
		if !exists {
			if !hasFallback {
				return "", fmt.Errorf("缺少环境变量: %s", name)
			}
			value = fallback
		}
		builder.WriteString(value)
		index = end + 1
	}
	return builder.String(), nil
}
