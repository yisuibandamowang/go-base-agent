package service

import "testing"

func TestHeuristicTokenCountAlignsJavaRules(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{name: "blank", text: " \n\t", want: 0},
		{name: "ascii rounds by four", text: "abcdefghij", want: 3},
		{name: "cjk counts each character", text: "知识库", want: 3},
		{name: "mixed text ignores whitespace", text: "你好 world", want: 4},
		{name: "other characters round by two", text: "é😊", want: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HeuristicTokenCount(tt.text); got != tt.want {
				t.Fatalf("HeuristicTokenCount(%q) = %d, want %d", tt.text, got, tt.want)
			}
		})
	}
}
