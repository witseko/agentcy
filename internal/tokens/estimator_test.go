package tokens

import "testing"

// Ported from the former Python test_estimator.py.
func TestEstimateText(t *testing.T) {
	cases := []struct {
		text string
		want int64
	}{
		{"", 0}, {"a", 1}, {"abcd", 1}, {"abcde", 2},
		// Code points, not bytes: four two-byte and four four-byte characters.
		{"éééé", 1}, {"🚀🚀🚀🚀🚀", 2},
	}
	for _, test := range cases {
		if got := EstimateText(test.text); got != test.want {
			t.Errorf("EstimateText(%q) = %d, want %d", test.text, got, test.want)
		}
	}
}

func TestEstimatePostAddsToolOverhead(t *testing.T) {
	if got := EstimatePost("12345678"); got != 2+ToolCallOverheadTokens {
		t.Errorf("EstimatePost = %d", got)
	}
}

func TestEstimateReadSumsMessagesAndBrief(t *testing.T) {
	cases := []struct {
		bodies []string
		brief  string
		want   int64
	}{
		{[]string{"1234", "12345678"}, "", 3},
		{[]string{"1234", "12345678"}, "123456789012", 6},
		{nil, "", 0},
	}
	for _, test := range cases {
		if got := EstimateRead(test.bodies, test.brief); got != test.want {
			t.Errorf("EstimateRead(%q, %q) = %d, want %d", test.bodies, test.brief, got, test.want)
		}
	}
}
