package model

import (
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestKnowledgeInternalURLImportTaskCreateDoesNotWriteEmptyResultJSON(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{DryRun: true})
	if err != nil {
		t.Fatalf("open dry-run sqlite: %v", err)
	}
	task := &KnowledgeInternalURLImportTask{
		KbID:           "kb-1",
		SourceLocation: "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=1",
		Status:         "running",
		CreatedBy:      "tester",
		UpdatedBy:      "tester",
	}

	tx := gdb.Create(task)
	if tx.Error != nil {
		t.Fatalf("build create statement: %v", tx.Error)
	}
	sql := tx.Statement.SQL.String()
	if !strings.Contains(sql, "`result_json`") {
		return
	}
	columnsStart := strings.Index(sql, "(")
	columnsEnd := strings.Index(sql, ")")
	if columnsStart < 0 || columnsEnd <= columnsStart {
		t.Fatalf("unexpected create statement: %s", sql)
	}
	columns := strings.Split(sql[columnsStart+1:columnsEnd], ",")
	for idx, column := range columns {
		if strings.Trim(column, "` ") == "result_json" {
			if idx >= len(tx.Statement.Vars) {
				t.Fatalf("missing var for result_json, sql=%s vars=%#v", sql, tx.Statement.Vars)
			}
			if tx.Statement.Vars[idx] == "" {
				t.Fatalf("create task should not write empty string into result_json, sql=%s vars=%#v", sql, tx.Statement.Vars)
			}
			return
		}
	}
}
