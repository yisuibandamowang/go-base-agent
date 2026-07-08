package rag

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"go-base-agent/prompts"
)

// PromptLoader loads and renders prompt templates from embedded files or external paths.
// Aligns with Java PromptTemplateLoader.
type PromptLoader struct {
	externalDir string
	cache       map[string]*template.Template
}

// NewPromptLoader creates a loader. If externalDir is non-empty, files are
// loaded from disk instead of the embedded defaults. This allows runtime
// prompt customization without recompilation.
func NewPromptLoader(externalDir string) *PromptLoader {
	return &PromptLoader{
		externalDir: externalDir,
		cache:       make(map[string]*template.Template),
	}
}

// Render loads a template from path (relative to resources/prompts/ or external dir)
// and renders it with the given data.
func (l *PromptLoader) Render(name string, data interface{}) (string, error) {
	tmpl, err := l.loadTemplate(name)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render prompt %s: %w", name, err)
	}
	return buf.String(), nil
}

func (l *PromptLoader) loadTemplate(name string) (*template.Template, error) {
	if t, ok := l.cache[name]; ok {
		return t, nil
	}

	var content []byte
	var err error

	if l.externalDir != "" {
		path := filepath.Join(l.externalDir, name)
		content, err = os.ReadFile(path)
	} else {
		content, err = prompts.FS.ReadFile(name)
	}
	if err != nil {
		return nil, fmt.Errorf("load prompt %s: %w", name, err)
	}

	tmpl, err := template.New(name).Parse(string(content))
	if err != nil {
		return nil, fmt.Errorf("parse prompt %s: %w", name, err)
	}

	l.cache[name] = tmpl
	return tmpl, nil
}
