package main

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DatasourceTarget is a database endpoint parsed from project code.
type DatasourceTarget struct {
	Dialect  string
	Host     string
	Port     int
	Database string
	Username string
	Password string
	Source   string
	Score    int
}

func scanDatasourceTargetsFromCode(repoPath string, service string) []DatasourceTarget {
	repoPath = strings.TrimSpace(repoPath)
	info, err := os.Stat(repoPath)
	if err != nil || !info.IsDir() {
		return nil
	}
	targets := make([]DatasourceTarget, 0)
	_ = filepath.WalkDir(repoPath, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry == nil {
			return nil
		}
		if entry.IsDir() {
			if shouldSkipDatasourceDir(entry.Name(), path, repoPath) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isDatasourceConfigFile(entry.Name()) {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		targets = append(targets, datasourceTargetsFromText(path, string(content), service)...)
		return nil
	})
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Score == targets[j].Score {
			return targets[i].Source < targets[j].Source
		}
		return targets[i].Score > targets[j].Score
	})
	if len(targets) > 10 {
		targets = targets[:10]
	}
	return targets
}

func datasourceTargetsFromText(path string, text string, service string) []DatasourceTarget {
	username := resolveConfigValue(firstConfigValue(text, []string{"username", "user"}))
	password := resolveConfigValue(firstConfigValue(text, []string{"password", "passwd"}))
	targets := make([]DatasourceTarget, 0)
	targets = append(targets, mysqlDatasourceTargetsFromText(path, text, service)...)
	for _, match := range regexp.MustCompile(`jdbc:postgresql://([^/\s"'\\:]+)(?::([0-9]+))?/([^?\s"'\\]+)`).FindAllStringSubmatch(text, -1) {
		target := DatasourceTarget{
			Dialect:  "postgres",
			Host:     resolveConfigValue(match[1]),
			Port:     parsePort(match[2], 5432),
			Database: trimDatabaseName(resolveConfigValue(match[3])),
			Username: username,
			Password: password,
			Source:   path,
			Score:    datasourceScore(path, service),
		}
		if target.Host != "" && target.Database != "" {
			targets = append(targets, target)
		}
	}
	for _, match := range regexp.MustCompile(`postgres(?:ql)?://[^\s"'\\]+`).FindAllString(text, -1) {
		target, ok := datasourceTargetFromURL(path, match, username, password, service)
		if ok {
			targets = append(targets, target)
		}
	}
	return uniqueDatasourceTargets(targets)
}

func mysqlDatasourceTargetsFromText(path string, text string, service string) []DatasourceTarget {
	targets := make([]DatasourceTarget, 0)
	blockPattern := regexp.MustCompile(`(?ms)^\s*(mySQL_[A-Za-z0-9_]+|mysql_[A-Za-z0-9_]+):\s*\n(.*?)(?:\n\S|\z)`)
	for _, match := range blockPattern.FindAllStringSubmatch(text, -1) {
		block := match[2]
		target := DatasourceTarget{
			Dialect:  "mysql",
			Host:     resolveConfigValue(firstConfigValue(block, []string{"host_name", "host"})),
			Port:     parsePort(firstConfigValue(block, []string{"port"}), 3306),
			Database: trimDatabaseName(resolveConfigValue(firstConfigValue(block, []string{"db_name", "database", "dbname"}))),
			Username: resolveConfigValue(firstConfigValue(block, []string{"user_name", "username", "user"})),
			Password: resolveConfigValue(firstConfigValue(block, []string{"password", "passwd"})),
			Source:   path,
			Score:    datasourceScore(path, service) + mysqlDatasourceScore(match[1]),
		}
		if target.Host != "" && target.Database != "" {
			targets = append(targets, target)
		}
	}
	return targets
}

func mysqlDatasourceScore(name string) int {
	name = strings.ToLower(strings.TrimSpace(name))
	score := 0
	if strings.Contains(name, "ad_platform") {
		score += 8
	}
	if strings.Contains(name, "online") {
		score += 2
	}
	return score
}

func datasourceTargetFromURL(path string, rawURL string, fallbackUser string, fallbackPassword string, service string) (DatasourceTarget, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return DatasourceTarget{}, false
	}
	port := parsePort(parsed.Port(), 5432)
	username := fallbackUser
	password := fallbackPassword
	if parsed.User != nil {
		username = resolveConfigValue(parsed.User.Username())
		if value, ok := parsed.User.Password(); ok {
			password = resolveConfigValue(value)
		}
	}
	target := DatasourceTarget{
		Dialect:  "postgres",
		Host:     resolveConfigValue(parsed.Hostname()),
		Port:     port,
		Database: trimDatabaseName(resolveConfigValue(strings.TrimPrefix(parsed.Path, "/"))),
		Username: username,
		Password: password,
		Source:   path,
		Score:    datasourceScore(path, service),
	}
	return target, target.Host != "" && target.Database != ""
}

func firstConfigValue(text string, keys []string) string {
	for _, key := range keys {
		patterns := []string{
			`(?im)^\s*[A-Za-z0-9_.-]*` + regexp.QuoteMeta(key) + `\s*[:=]\s*["']?([^"'\s#]+)`,
			`(?im)^\s*` + regexp.QuoteMeta(key) + `\s*[:=]\s*["']?([^"'\s#]+)`,
		}
		for _, pattern := range patterns {
			match := regexp.MustCompile(pattern).FindStringSubmatch(text)
			if len(match) == 2 {
				return strings.TrimSpace(match[1])
			}
		}
	}
	return ""
}

func resolveConfigValue(value string) string {
	value = strings.Trim(strings.TrimSpace(value), `"'`)
	if value == "" {
		return ""
	}
	if match := regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)(?::([^}]*))?}$`).FindStringSubmatch(value); len(match) > 0 {
		if envValue := strings.TrimSpace(os.Getenv(match[1])); envValue != "" {
			return envValue
		}
		if len(match) == 3 {
			return strings.TrimSpace(match[2])
		}
		return ""
	}
	return value
}

func shouldSkipDatasourceDir(name string, path string, root string) bool {
	if path == root {
		return false
	}
	switch name {
	case ".git", ".idea", ".gradle", ".mvn", "node_modules", "target", "dist", "build", "vendor":
		return true
	default:
		return strings.HasPrefix(name, ".")
	}
}

func isDatasourceConfigFile(name string) bool {
	lower := strings.ToLower(name)
	for _, suffix := range []string{".yml", ".yaml", ".properties", ".toml", ".env", ".conf", ".go", ".java", ".xml"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

func parsePort(value string, fallback int) int {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	port, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || port <= 0 {
		return fallback
	}
	return port
}

func trimDatabaseName(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.IndexAny(value, "?#"); index >= 0 {
		value = value[:index]
	}
	return strings.Trim(value, "/")
}

func datasourceScore(path string, service string) int {
	score := 1
	lowerPath := strings.ToLower(path)
	service = strings.ToLower(strings.TrimSpace(service))
	if service != "" && service != "all" && strings.Contains(lowerPath, "/"+service+"/") {
		score += 10
	}
	if strings.Contains(lowerPath, "application") || strings.Contains(lowerPath, "bootstrap") {
		score += 3
	}
	return score
}

func uniqueDatasourceTargets(targets []DatasourceTarget) []DatasourceTarget {
	seen := map[string]struct{}{}
	out := make([]DatasourceTarget, 0, len(targets))
	for _, target := range targets {
		key := target.Dialect + "|" + target.Host + "|" + strconv.Itoa(target.Port) + "|" + target.Database + "|" + target.Username
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, target)
	}
	return out
}
