package db

import (
	"database/sql/driver"
	"testing"
)

func TestJSONTextValue(t *testing.T) {
	cases := []struct {
		name  string
		input JSONText
		want  driver.Value
	}{
		{"空串落库为 NULL", "", nil},
		{"合法 JSON 原样落库", `{"maxChars":256}`, `{"maxChars":256}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.input.Value()
			if err != nil {
				t.Fatalf("value: %v", err)
			}
			if got != tc.want {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
		})
	}
}

func TestJSONTextScan(t *testing.T) {
	var j JSONText
	if err := j.Scan(nil); err != nil || j != "" {
		t.Fatalf("scan nil: err=%v j=%q", err, j)
	}
	if err := j.Scan("abc"); err != nil || j != "abc" {
		t.Fatalf("scan string: err=%v j=%q", err, j)
	}
	if err := j.Scan([]byte("def")); err != nil || j != "def" {
		t.Fatalf("scan bytes: err=%v j=%q", err, j)
	}
	if err := j.Scan(123); err == nil {
		t.Fatalf("scan int should fail")
	}
}
