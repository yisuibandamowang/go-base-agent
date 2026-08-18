package migrate

import (
	"context"
	"testing"
)

func TestRunExecutesSchemaAndInit(t *testing.T) {
	var calls []string
	err := Run(context.Background(), Options{
		SchemaFile: "resources/database/schema_pg.sql",
		InitFile:   "resources/database/init_data_pg.sql",
		CheckDB: func(context.Context) error {
			calls = append(calls, "db")
			return nil
		},
		ExecuteSQL: func(_ context.Context, path string) error {
			calls = append(calls, path)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("run migrate: %v", err)
	}
	want := []string{"db", "resources/database/schema_pg.sql", "resources/database/init_data_pg.sql"}
	if len(calls) != len(want) {
		t.Fatalf("unexpected calls: %#v", calls)
	}
	for i, got := range calls {
		if got != want[i] {
			t.Fatalf("unexpected calls: %#v", calls)
		}
	}
}

func TestRunRejectsMissingSchema(t *testing.T) {
	err := Run(context.Background(), Options{
		ExecuteSQL: func(context.Context, string) error {
			t.Fatal("execute sql should not run")
			return nil
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}
