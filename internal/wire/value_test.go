package wire

import "testing"

// TestIndentedMatchesPydantic compares with pydantic_core.to_json(indent=2) output
// recorded from Python: control characters escaped, HTML and non-ASCII kept, empty
// containers inline.
func TestIndentedMatchesPydantic(t *testing.T) {
	value, err := Parse([]byte(`{"a":"\u001f\b\f` + "\x7f é" + `\"\\/<>&","b":[],"c":{},"d":[1,{"x":null}],"j":true}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"a\": \"\\u001f\\b\\f\x7f é\\\"\\\\/<>&\",\n  \"b\": [],\n  \"c\": {},\n  \"d\": [\n    1,\n    {\n      \"x\": null\n    }\n  ],\n  \"j\": true\n}"
	if got := value.Indented(); got != want {
		t.Errorf("Indented:\n got %q\nwant %q", got, want)
	}
}

func TestRepeatedKeyKeepsFirstPositionAndLastValue(t *testing.T) {
	value, err := Parse([]byte(`{"a":1,"b":2,"a":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := value.Compact(); got != `{"a":3,"b":2}` {
		t.Errorf("got %s", got)
	}
}

func TestParseRejectsNonStrictJSON(t *testing.T) {
	for _, text := range []string{`NaN`, `01`, `+1`, `"\ud800"`, "\xef\xbb\xbf{}", "\"a\nb\"", `{"a":1,}`, `{} {}`, `"\x"`, "\"\xff\""} {
		if _, err := Parse([]byte(text)); err == nil {
			t.Errorf("Parse(%q) accepted", text)
		}
	}
}

func TestSetReplacesInPlaceAndDeleteRemoves(t *testing.T) {
	object := NewObject()
	object.Set("a", IntValue(1))
	object.Set("b", IntValue(2))
	object.Set("a", IntValue(3))
	object.Delete("b")
	object.Set("c", TextValue("x"))
	if got := object.Compact(); got != `{"a":3,"c":"x"}` {
		t.Errorf("got %s", got)
	}
}
