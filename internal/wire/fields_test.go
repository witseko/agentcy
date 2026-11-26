package wire

import "testing"

func TestCheckLiteralNamesTheAllowedTexts(t *testing.T) {
	cases := []struct {
		allowed []string
		want    string
	}{
		{nil, "No input is allowed"},
		{[]string{}, "No input is allowed"},
		{[]string{"note"}, "Input should be 'note'"},
		{[]string{"note", "question"}, "Input should be 'note' or 'question'"},
		{[]string{"worker", "lead", "co-lead"}, "Input should be 'worker', 'lead' or 'co-lead'"},
	}
	for _, test := range cases {
		text, problem := CheckLiteral(TextValue("other"), test.allowed)
		if text != "" || problem == nil || problem.Message != test.want || problem.Type != "literal_error" {
			t.Errorf("CheckLiteral(%q) = %q, %+v; want message %q", test.allowed, text, problem, test.want)
		}
	}
	if text, problem := CheckLiteral(TextValue("lead"), AssignableRoles); text != "lead" || problem != nil {
		t.Errorf("CheckLiteral rejected an allowed text: %q, %+v", text, problem)
	}
}
