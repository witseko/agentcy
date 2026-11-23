package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestCleanKeepsVisibleTextAndMarksControls(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"unterminated OSC keeps later text", "LGTM.\x1b]hidden\nmore", "LGTM.�]hidden\nmore"},
		{"unterminated CSI keeps later digits", "Pay 1\x1b[000 000 USD", "Pay 1�[000 000 USD"},
		{"zero-width space is removed", "a​b", "ab"},
		{"bidi override is removed", "a‮b", "ab"},
		{"zero-width joiner is kept", "a‍b", "a‍b"},
		{"zero-width non-joiner is kept", "a‌b", "a‌b"},
		{"carriage return and newline", "a\r\nb", "a\nb"},
		{"C1 control is marked", "a\u009bb", "a�b"},
		{"tab becomes spaces", "a\tb", "a    b"},
		{"line separator is removed", "a b c", "abc"},
		{"invisible fillers are removed", "aㅤbᅟcᅠd", "abcd"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := clean(test.text); got != test.want {
				t.Fatalf("clean(%q) = %q, want %q", test.text, got, test.want)
			}
		})
	}
}

func TestSearchHitShowsYouOnlyForTheExactHumanName(t *testing.T) {
	m := sized(newModel(&fakeOperator{}, "/work"), 120, 30)
	cases := map[string]string{
		humanName: "You",
		"Hu‎man":  "Hu" + "man",
		"Hu​man":  "Hu" + "man",
		"Lead":    "Lead",
	}
	for author, want := range cases {
		hit := SearchHit{Title: "Chat", Author: &author, Snippet: "text"}
		lines := m.hitLines(hit, nil, 80)
		if got := ansi.Strip(lines[1]); !strings.HasPrefix(got, want+" ·") {
			t.Errorf("author %q rendered as %q, want prefix %q", author, got, want)
		}
	}
}
