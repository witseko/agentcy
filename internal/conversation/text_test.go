package conversation

import (
	"strings"
	"testing"
)

func TestNameSkeletonFoldsLookalikesAndDropsMarks(t *testing.T) {
	cases := map[string]string{
		"Human":    "human",
		"HUMAN":    "human",
		"Hυman":    "human",
		"Ｈｕｍａｎ":    "human",
		"hu-man":   "human",
		"Straße":   "strasse",
		"é":       "é",
		"कि":       "क",
		"Worker ²": "worker2",
		"Ⅻ":        "xii",
		"_x":       "x",
		"Ωmega":    "ωmega",
	}
	for input, want := range cases {
		if got := nameSkeleton(input); got != want {
			t.Errorf("nameSkeleton(%q) = %q, want %q", input, got, want)
		}
	}
	for _, reserved := range []string{"Human", "you", "Y o u", "Hυman"} {
		if checkName(reserved) == nil {
			t.Errorf("checkName(%q) accepted a reserved name", reserved)
		}
	}
}

func TestTitlesUseThreeDifferentShortenings(t *testing.T) {
	long := strings.Repeat("a", 90)
	if got := sessionTitle("  " + long + "\nsecond"); got != strings.Repeat("a", 77)+"..." {
		t.Errorf("sessionTitle = %q", got)
	}
	if got := sessionTitle("Short\nsecond"); got != "Short" {
		t.Errorf("sessionTitle = %q", got)
	}
	words := strings.Repeat("word ", 30)
	if got := taskTitle("\n  \n" + words); got != strings.TrimRight(words[:119], " ")+"…" {
		t.Errorf("taskTitle = %q", got)
	}
	if got := taskTitle("First second"); got != "First" {
		t.Errorf("taskTitle splits on U+2028: got %q", got)
	}
	if got := taskTitle("a \t b\r\nc"); got != "a b" {
		t.Errorf("taskTitle collapses white space: got %q", got)
	}
}

func TestSearchTermsAreQuotedPrefixWords(t *testing.T) {
	cases := map[string]string{
		"retry rule":    `"retry"* "rule"*`,
		`a"b OR c*`:     `"a"* "b"* "OR"* "c"*`,
		"test_case ⅷ ½": `"test_case"* "ⅷ"* "½"*`,
		"été":          `"e"* "té"*`,
		"one two three four five six seven eight nine": `"one"* "two"* "three"* "four"* "five"* "six"* "seven"* "eight"*`,
	}
	for input, want := range cases {
		got, err := matchExpression(input)
		if err != nil || got != want {
			t.Errorf("matchExpression(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := matchExpression("!!! ..."); err == nil {
		t.Error("a query without words was accepted")
	}
}
