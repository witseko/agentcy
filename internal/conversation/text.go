package conversation

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// titleLength is the longest list-view title in code points (TITLE_LENGTH in models.py).
const titleLength = 80

// taskTitleLength is the longest task title in code points (TASK_TITLE_LENGTH in
// tasks.py).
const taskTitleLength = 120

// foldCase returns Python's str.casefold() of text: full Unicode case folding.
func foldCase(text string) string {
	// A Caser keeps state, so each call gets its own.
	return cases.Fold().String(text)
}

// sameName compares two names the way Python does: label.casefold() == name.casefold().
func sameName(left, right string) bool {
	return foldCase(left) == foldCase(right)
}

// isPythonAlphanumeric is str.isalnum() for one character: a letter (categories Lu, Ll,
// Lt, Lm, Lo) or a number (Nd, Nl, No). Combining marks are not alphanumeric.
func isPythonAlphanumeric(character rune) bool {
	return unicode.IsLetter(character) || unicode.IsNumber(character)
}

// isPythonSpace is str.isspace() for one character. Python also counts the separators
// U+001C to U+001F as white space, which Go does not.
func isPythonSpace(character rune) bool {
	return unicode.IsSpace(character) || (character >= 0x1c && character <= 0x1f)
}

// pythonStrip is str.strip() with no argument.
func pythonStrip(text string) string {
	return strings.TrimFunc(text, isPythonSpace)
}

// pythonRightStrip is str.rstrip() with no argument.
func pythonRightStrip(text string) string {
	return strings.TrimRightFunc(text, isPythonSpace)
}

// pythonSplitWords is str.split() with no argument: the runs between white space.
func pythonSplitWords(text string) []string {
	return strings.FieldsFunc(text, isPythonSpace)
}

// isLineBoundary reports the characters at which str.splitlines() breaks a line.
func isLineBoundary(character rune) bool {
	switch character {
	case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

// pythonSplitLines is str.splitlines(): the lines of text without their line endings,
// where "\r\n" counts as one ending and a final ending adds no empty line.
func pythonSplitLines(text string) []string {
	var lines []string
	start := 0
	for index := 0; index < len(text); {
		character, size := utf8.DecodeRuneInString(text[index:])
		if !isLineBoundary(character) {
			index += size
			continue
		}
		lines = append(lines, text[start:index])
		index += size
		if character == '\r' && index < len(text) && text[index] == '\n' {
			index++
		}
		start = index
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	return lines
}

// firstCodePoints returns the first count code points of text.
func firstCodePoints(text string, count int) string {
	runes := []rune(text)
	if len(runes) <= count {
		return text
	}
	return string(runes[:count])
}

// codePointLength is Python's len() of a str.
func codePointLength(text string) int {
	return utf8.RuneCountInString(text)
}

// sessionTitle shortens the first line of a brief or a custom title for list views and
// join prompts: 77 code points and "..." when the line is longer than titleLength.
func sessionTitle(brief string) string {
	firstLine, _, _ := strings.Cut(pythonStrip(brief), "\n")
	firstLine = pythonStrip(firstLine)
	if codePointLength(firstLine) > titleLength {
		return firstCodePoints(firstLine, titleLength-3) + "..."
	}
	return firstLine
}

// taskTitle is the first non-empty line of a task body with its white space collapsed,
// cut to 119 code points plus "…" when longer than taskTitleLength.
func taskTitle(body string) string {
	for _, rawLine := range pythonSplitLines(body) {
		line := strings.Join(pythonSplitWords(rawLine), " ")
		if line == "" {
			continue
		}
		if codePointLength(line) > taskTitleLength {
			return pythonRightStrip(firstCodePoints(line, taskTitleLength-1)) + "…"
		}
		return line
	}
	return ""
}

// lookalikeLetters maps a small, non-exhaustive list of look-alike letters to ASCII. The
// message role is the real proof of identity.
var lookalikeLetters = map[rune]rune{
	'н': 'h', 'һ': 'h', 'հ': 'h', 'η': 'h',
	'υ': 'u', 'ս': 'u',
	'у': 'y',
	'м': 'm', 'μ': 'm',
	'а': 'a', 'α': 'a',
	'ո': 'n',
	'о': 'o', 'ο': 'o',
}

// nameSkeleton reduces a name to lowercase letters and digits with look-alike letters
// mapped to ASCII: NFKC, casefold, the look-alike map, then only alphanumeric characters
// (name_skeleton in repository.py).
func nameSkeleton(name string) string {
	folded := foldCase(norm.NFKC.String(name))
	var builder strings.Builder
	for _, character := range folded {
		if replacement, found := lookalikeLetters[character]; found {
			character = replacement
		}
		if isPythonAlphanumeric(character) {
			builder.WriteRune(character)
		}
	}
	return builder.String()
}

// checkName keeps the human's reserved names, and names that look like them, out of
// every slot.
func checkName(name string) error {
	skeleton := nameSkeleton(name)
	if skeleton == nameSkeleton(humanLabel) || skeleton == nameSkeleton(humanAlias) {
		return rejectf("The name %s is reserved for the user. Use a different name", humanLabel)
	}
	return nil
}
