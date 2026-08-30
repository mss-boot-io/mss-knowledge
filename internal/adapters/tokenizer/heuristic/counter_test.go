package heuristictokenizer

import "testing"

func TestCounterEstimatesMixedText(t *testing.T) {
	counter := Counter{}
	tests := []struct {
		name string
		text string
		want int
	}{
		{name: "empty", text: "", want: 0},
		{name: "ascii", text: "hello", want: 2},
		{name: "ascii words", text: "hello world", want: 4},
		{name: "Chinese", text: "你好世界", want: 4},
		{name: "mixed", text: "Redis向量 search", want: 6},
		{name: "code punctuation", text: "a.b()", want: 5},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := counter.Count(test.text); got != test.want {
				t.Fatalf("Count(%q) = %d, want %d", test.text, got, test.want)
			}
		})
	}
}
