package wire

import (
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// The limits of the request fields, from src/agentcy/conversations/models.py.
const (
	MaxLabelLength     = 64
	MaxBodyLength      = 8192
	MaxKeyLength       = 128
	MaxDirectoryLength = 4096
	MaxWaitSeconds     = 45
	MaxDueMinutes      = 1440
	MaxDeadlineMinutes = 60
	MaxSlots           = 16
	MaxTokenCount      = 1_000_000_000
	MaxCursor          = math.MaxInt64
)

// The patterns of the request fields. Go's regexp anchors "$" at the end of the text, as
// pydantic's Rust regex does, so a trailing newline never matches.
var (
	workspacePattern    = regexp.MustCompile(`^[a-f0-9]{64}$`)
	modelNamePattern    = regexp.MustCompile(`^[a-zA-Z0-9._:/-]{1,128}$`)
	pluginPattern       = regexp.MustCompile(`^[a-zA-Z0-9-]{1,64}$`)
	conversationPattern = regexp.MustCompile(`^[a-f0-9]{24}$`)
	mcpTaskPattern      = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	integerTextPattern  = regexp.MustCompile(`^[+-]?[0-9]+(_[0-9]+)*(\.0+)?$`)
)

// Pattern texts as pydantic prints them in errors.
const (
	workspacePatternText    = `^[a-f0-9]{64}$`
	modelNamePatternText    = `^[a-zA-Z0-9._:/-]{1,128}$`
	pluginPatternText       = `^[a-zA-Z0-9-]{1,64}$`
	conversationPatternText = `^[a-f0-9]{24}$`
	mcpTaskPatternText      = `^[A-Za-z0-9_-]{1,128}$`
)

// The texts that pydantic's lax boolean accepts, compared in lower case.
var (
	trueWords  = map[string]bool{"1": true, "on": true, "t": true, "true": true, "y": true, "yes": true}
	falseWords = map[string]bool{"0": true, "off": true, "f": true, "false": true, "n": true, "no": true}
)

// maxIntegralFloat is 2^63, the first float that no longer fits int64.
const maxIntegralFloat = 9223372036854775808.0

// IntRange is an inclusive integer range with pydantic's wording for each end.
type IntRange struct {
	Minimum int64
	Maximum int64
	// Exclusive is set for a lower bound written as "gt" in the Python model.
	Exclusive bool
}

// The ranges of the integer fields.
var (
	CursorRange = IntRange{Minimum: 0, Maximum: MaxCursor}
	WaitRange   = IntRange{Minimum: 0, Maximum: MaxWaitSeconds}
	// ListenWaitRange is the wait of a listen, which is always a waiting read.
	ListenWaitRange = IntRange{Minimum: 1, Maximum: MaxWaitSeconds}
	MessageRange    = IntRange{Minimum: 1, Maximum: math.MaxInt64}
	DueRange        = IntRange{Minimum: 1, Maximum: MaxDueMinutes}
	DeadlineRange   = IntRange{Minimum: 1, Maximum: MaxDeadlineMinutes}
	CountRange      = IntRange{Minimum: 0, Maximum: MaxTokenCount}
	BudgetRange     = IntRange{Minimum: 0, Maximum: MaxTokenCount, Exclusive: true}
)

// problem builds a FieldError without a location.
func problem(message, errorType string) *FieldError {
	return &FieldError{Message: message, Type: errorType}
}

// CoerceString accepts only a JSON string.
func CoerceString(value Value) (string, *FieldError) {
	if value.Kind != String {
		return "", problem("Input should be a valid string", "string_type")
	}
	return value.Text, nil
}

// CoerceStrictInt accepts only an integer literal, like a pydantic strict int: no
// strings, no floats such as 5.0, no booleans. The result is range-checked.
func CoerceStrictInt(value Value, limits IntRange) (int64, *FieldError) {
	if value.Kind != Number || strings.ContainsAny(value.Literal, ".eE") {
		return 0, problem("Input should be a valid integer", "int_type")
	}
	number, ok := new(big.Int).SetString(value.Literal, 10)
	if !ok {
		return 0, problem("Input should be a valid integer", "int_type")
	}
	return checkRange(number, limits)
}

// CoerceLaxInt accepts what a pydantic lax int accepts: integers, integral floats,
// booleans, and decimal strings with optional sign, underscores between digits and a zero
// fraction. Only the cursor field ("after") is lax.
func CoerceLaxInt(value Value, limits IntRange) (int64, *FieldError) {
	switch value.Kind {
	case Bool:
		if value.Boolean {
			return checkRange(big.NewInt(1), limits)
		}
		return checkRange(big.NewInt(0), limits)
	case Number:
		return laxIntFromNumber(value.Literal, limits)
	case String:
		return laxIntFromText(value.Text, limits)
	default:
		return 0, problem("Input should be a valid integer", "int_type")
	}
}

// laxIntFromNumber converts an integer literal exactly and a float literal when it is
// integral and below 2^63.
func laxIntFromNumber(literal string, limits IntRange) (int64, *FieldError) {
	if !strings.ContainsAny(literal, ".eE") {
		number, _ := new(big.Int).SetString(literal, 10)
		return checkRange(number, limits)
	}
	number, err := strconv.ParseFloat(literal, 64)
	if err != nil || math.IsInf(number, 0) || math.Abs(number) >= maxIntegralFloat {
		return 0, problem("Unable to parse input string as an integer, exceeded maximum size", "int_parsing_size")
	}
	if number != math.Trunc(number) {
		return 0, problem("Input should be a valid integer, got a number with a fractional part", "int_from_float")
	}
	return checkRange(big.NewInt(int64(number)), limits)
}

// laxIntFromText parses a decimal string after stripping surrounding whitespace.
func laxIntFromText(text string, limits IntRange) (int64, *FieldError) {
	trimmed := strings.TrimSpace(text)
	if !integerTextPattern.MatchString(trimmed) {
		return 0, problem("Input should be a valid integer, unable to parse string as an integer", "int_parsing")
	}
	whole, _, _ := strings.Cut(trimmed, ".")
	number, _ := new(big.Int).SetString(strings.ReplaceAll(whole, "_", ""), 10)
	return checkRange(number, limits)
}

// checkRange applies the bounds of an integer field.
func checkRange(number *big.Int, limits IntRange) (int64, *FieldError) {
	minimum := big.NewInt(limits.Minimum)
	if limits.Exclusive && number.Cmp(minimum) <= 0 {
		return 0, problem(fmt.Sprintf("Input should be greater than %d", limits.Minimum), "greater_than")
	}
	if number.Cmp(minimum) < 0 {
		return 0, problem(fmt.Sprintf("Input should be greater than or equal to %d", limits.Minimum), "greater_than_equal")
	}
	if number.Cmp(big.NewInt(limits.Maximum)) > 0 {
		return 0, problem(fmt.Sprintf("Input should be less than or equal to %d", limits.Maximum), "less_than_equal")
	}
	return number.Int64(), nil
}

// CoerceLaxBool accepts what a pydantic lax bool accepts: booleans, the integers 0 and 1,
// the floats 0.0 and 1.0, and the words in trueWords and falseWords in any case.
func CoerceLaxBool(value Value) (bool, *FieldError) {
	unreadable := problem("Input should be a valid boolean, unable to interpret input", "bool_parsing")
	switch value.Kind {
	case Bool:
		return value.Boolean, nil
	case String:
		word := strings.ToLower(value.Text)
		if trueWords[word] || falseWords[word] {
			return trueWords[word], nil
		}
		return false, unreadable
	case Number:
		return boolFromNumber(value.Literal, unreadable)
	default:
		return false, problem("Input should be a valid boolean", "bool_type")
	}
}

// boolFromNumber maps 0 and 1 to booleans. Other integers cannot be read, and other
// floats are the wrong type, as in pydantic.
func boolFromNumber(literal string, unreadable *FieldError) (bool, *FieldError) {
	if !strings.ContainsAny(literal, ".eE") {
		switch literal {
		case "0", "-0":
			return false, nil
		case "1":
			return true, nil
		}
		return false, unreadable
	}
	number, err := strconv.ParseFloat(literal, 64)
	switch {
	case err == nil && number == 0:
		return false, nil
	case err == nil && number == 1:
		return true, nil
	}
	return false, problem("Input should be a valid boolean", "bool_type")
}

// CheckLength applies a length limit counted in code points, as Python counts.
func CheckLength(text string, minimum, maximum int) *FieldError {
	length := utf8.RuneCountInString(text)
	if length < minimum {
		return problem(fmt.Sprintf("String should have at least %d %s", minimum, characters(minimum)), "string_too_short")
	}
	if length > maximum {
		return problem(fmt.Sprintf("String should have at most %d %s", maximum, characters(maximum)), "string_too_long")
	}
	return nil
}

// characters picks the singular or plural noun for a length message.
func characters(count int) string {
	if count == 1 {
		return "character"
	}
	return "characters"
}

// CheckPattern applies one of the anchored field patterns.
func CheckPattern(text string, pattern *regexp.Regexp, patternText string) *FieldError {
	if pattern.MatchString(text) {
		return nil
	}
	return problem(fmt.Sprintf("String should match pattern '%s'", patternText), "string_pattern_mismatch")
}

// CheckKey validates a credential or ID field: 1 to 128 code points, kept as written.
func CheckKey(text string) *FieldError {
	return CheckLength(text, 1, MaxKeyLength)
}

// NormalizeLabel strips a participant name and validates it like models.Label: 1 to 64
// code points, no hidden characters, and NFKC normal form.
func NormalizeLabel(text string) (string, *FieldError) {
	stripped := strings.TrimSpace(text)
	if err := CheckLength(stripped, 1, MaxLabelLength); err != nil {
		return "", err
	}
	for _, character := range stripped {
		if isHiddenCharacter(character) {
			return "", problem("Value error, Name must not contain control or format characters", "value_error")
		}
	}
	if norm.NFKC.String(stripped) != stripped {
		return "", problem("Value error, Name must be in Unicode NFKC normal form", "value_error")
	}
	return stripped, nil
}

// NormalizeBody strips a message text and validates it like models.Body: 1 to 8192 code
// points and no control characters other than newline, tab, and carriage return.
func NormalizeBody(text string) (string, *FieldError) {
	stripped := strings.TrimSpace(text)
	if err := CheckLength(stripped, 1, MaxBodyLength); err != nil {
		return "", err
	}
	for _, character := range stripped {
		if unicode.Is(unicode.Cc, character) && character != '\n' && character != '\t' && character != '\r' {
			return "", problem("Value error, Body must not contain control characters", "value_error")
		}
	}
	return stripped, nil
}

// isHiddenCharacter reports a character in the categories Cc, Cf, Cs, Co, Cn, Zl or Zp.
// Go strings hold no surrogates, so Cs cannot occur.
func isHiddenCharacter(character rune) bool {
	if unicode.In(character, unicode.Cc, unicode.Cf, unicode.Co, unicode.Zl, unicode.Zp) {
		return true
	}
	assigned := unicode.In(character, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Zs, unicode.Cs)
	return !assigned
}

// CheckLiteral accepts one of the allowed texts exactly.
func CheckLiteral(value Value, allowed []string) (string, *FieldError) {
	if value.Kind == String && containsText(allowed, value.Text) {
		return value.Text, nil
	}
	quoted := make([]string, len(allowed))
	for index, text := range allowed {
		quoted[index] = "'" + text + "'"
	}
	expected := strings.Join(quoted[:len(quoted)-1], ", ") + " or " + quoted[len(quoted)-1]
	return "", problem("Input should be "+expected, "literal_error")
}

// containsText reports whether texts holds text.
func containsText(texts []string, text string) bool {
	for _, candidate := range texts {
		if candidate == text {
			return true
		}
	}
	return false
}

// CheckModelName validates a model name such as "claude-opus-4".
func CheckModelName(text string) *FieldError {
	return CheckPattern(text, modelNamePattern, modelNamePatternText)
}

// CheckConversationID validates a 24-digit lowercase hex conversation ID.
func CheckConversationID(text string) *FieldError {
	return CheckPattern(text, conversationPattern, conversationPatternText)
}

// ParseSlots reads and validates an invitees list: each item is a name or an invitation
// object without unknown fields. Error locations are relative to the list.
func ParseSlots(value Value) ([]Slot, []FieldError) {
	reader := newFieldReader(NewObject())
	reader.object.Set("invitees", value)
	slots := reader.slots("invitees")
	if len(reader.errors.fields) > 0 {
		return nil, relativeErrors(reader.errors.fields)
	}
	var check checker
	check.slots(slots)
	return slots, relativeErrors(check.errors.fields)
}

// relativeErrors drops the leading "invitees." from error locations.
func relativeErrors(fields []FieldError) []FieldError {
	for index := range fields {
		location := strings.TrimPrefix(fields[index].Location, "invitees")
		fields[index].Location = strings.TrimPrefix(location, ".")
	}
	return fields
}
