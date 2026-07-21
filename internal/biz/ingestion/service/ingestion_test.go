package service

import "testing"

func TestNormalizeSourceTypeAliases(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want string
	}{
		{in: "file", want: "file"},
		{in: "localfile", want: "file"},
		{in: "local_file", want: "file"},
		{in: "url", want: "url"},
		{in: "LOCAL-FILE", want: "file"},
	} {
		if got := normalizeSourceType(tt.in); got != tt.want {
			t.Fatalf("normalizeSourceType(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
