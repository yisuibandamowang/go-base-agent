package migrate

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Options describes the database migration workflow.
type Options struct {
	SchemaFile string
	InitFile   string
	CheckDB    func(context.Context) error
	ExecuteSQL func(context.Context, string) error
}

// Run executes the schema and initial data scripts.
func Run(ctx context.Context, opts Options) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(opts.SchemaFile) == "" {
		return errors.New("schema file 不能为空")
	}
	if opts.CheckDB != nil {
		if err := opts.CheckDB(ctx); err != nil {
			return fmt.Errorf("数据库检查失败: %w", err)
		}
	}
	executeSQL := opts.ExecuteSQL
	if executeSQL == nil {
		return errors.New("execute sql func 不能为空")
	}
	if err := executeSQL(ctx, opts.SchemaFile); err != nil {
		return err
	}
	if strings.TrimSpace(opts.InitFile) != "" {
		if err := executeSQL(ctx, opts.InitFile); err != nil {
			return err
		}
	}
	return nil
}
