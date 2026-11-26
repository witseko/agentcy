//go:build conformance

package conformance

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

var (
	// joinCodePattern matches a workspace-tagged join code: 22 URL-safe characters, a dot
	// and a 12-digit hex tag.
	joinCodePattern = regexp.MustCompile(`[A-Za-z0-9_-]{22}\.[0-9a-f]{12}`)
	// urlSafeRunPattern matches a maximal run of URL-safe characters. Its length and alphabet
	// tell a token, a conversation ID or a workspace key from ordinary words.
	urlSafeRunPattern = regexp.MustCompile(`[A-Za-z0-9_-]+`)
	hexPattern        = regexp.MustCompile(`^[0-9a-f]+$`)
)

const (
	// An integer within two days of the daemon start is a timestamp. The longest offset the
	// protocol creates is a task due in 1440 minutes; token counts and IDs are far from now.
	timestampWindowSeconds = 2 * 24 * 60 * 60
	// A timestamp within this many seconds of the daemon start counts as "now".
	nowToleranceSeconds = 30
	// Requests show long strings by length only, so a golden stays readable.
	longRequestString = 120
)

// normalizer replaces values that differ between runs with stable placeholders. The first
// distinct value of a kind becomes <kind-1>, the next <kind-2>, and every later occurrence
// of the same value gets the same placeholder, in requests and responses alike.
type normalizer struct {
	// literals are exact strings such as temp paths and the operator key, replaced first.
	literals     []literal
	placeholders map[string]string
	counts       map[string]int
	// startUnix is the Unix time just before the daemon started.
	startUnix int64
}

type literal struct {
	value       string
	placeholder string
}

func newNormalizer(startUnix int64) *normalizer {
	return &normalizer{
		placeholders: map[string]string{},
		counts:       map[string]int{},
		startUnix:    startUnix,
	}
}

// addLiteral registers an exact string, such as a temp path, to replace with a fixed name.
// Longer strings are replaced first, so a path is replaced before its parent directory.
func (n *normalizer) addLiteral(value, placeholder string) {
	if value == "" {
		return
	}
	n.literals = append(n.literals, literal{value: value, placeholder: placeholder})
	sort.SliceStable(n.literals, func(i, j int) bool {
		return len(n.literals[i].value) > len(n.literals[j].value)
	})
}

// placeholder returns the stable placeholder of one raw value of the given kind.
func (n *normalizer) placeholder(kind, raw string) string {
	key := kind + "\x00" + raw
	if existing, found := n.placeholders[key]; found {
		return existing
	}
	n.counts[kind]++
	name := fmt.Sprintf("<%s-%d>", kind, n.counts[kind])
	n.placeholders[key] = name
	return name
}

// value normalises a decoded JSON value. The key is the object key that holds the value,
// or "" for array items and the top level.
func (n *normalizer) value(value any, key string) any {
	switch typed := value.(type) {
	case orderedObject:
		normalised := make(orderedObject, len(typed))
		for index, member := range typed {
			normalised[index] = objectMember{key: member.key, value: n.value(member.value, member.key)}
		}
		return normalised
	case []any:
		normalised := make([]any, len(typed))
		for index, item := range typed {
			normalised[index] = n.value(item, "")
		}
		return normalised
	case string:
		return n.text(typed)
	case json.Number:
		return n.number(typed, key)
	}
	return value
}

// number replaces idle seconds and timestamps, which depend on the clock.
func (n *normalizer) number(number json.Number, key string) any {
	if key == "idle_seconds" {
		return "<idle-seconds>"
	}
	whole, err := number.Int64()
	if err != nil || whole < n.startUnix-timestampWindowSeconds || whole > n.startUnix+timestampWindowSeconds {
		return number
	}
	return n.timestamp(whole)
}

// timestamp writes a Unix time relative to the daemon start: <now> within the tolerance,
// otherwise the offset rounded to whole minutes, such as <now+10m> for a deadline. Exact
// seconds would make the golden depend on when a second boundary falls during the run.
func (n *normalizer) timestamp(unix int64) string {
	offset := unix - n.startUnix
	if offset >= -nowToleranceSeconds && offset <= nowToleranceSeconds {
		return "<now>"
	}
	minutes := (offset + 30) / 60
	if offset < 0 {
		minutes = (offset - 30) / 60
	}
	return fmt.Sprintf("<now%+dm>", minutes)
}

// text replaces literals, join codes, tokens, conversation and proposal IDs and workspace
// keys inside one string, wherever they appear in it.
func (n *normalizer) text(text string) string {
	for _, item := range n.literals {
		text = strings.ReplaceAll(text, item.value, item.placeholder)
	}
	text = joinCodePattern.ReplaceAllStringFunc(text, func(code string) string {
		return n.placeholder("code", code)
	})
	return urlSafeRunPattern.ReplaceAllStringFunc(text, n.classifyRun)
}

// classifyRun names a run of URL-safe characters by its length and alphabet, or keeps it.
func (n *normalizer) classifyRun(run string) string {
	isHex := hexPattern.MatchString(run)
	switch {
	case len(run) == 64 && isHex:
		return n.placeholder("workspace", run)
	case len(run) == 43:
		// secrets.token_urlsafe(32): participant tokens and the operator key.
		return n.placeholder("token", run)
	case len(run) == 32 && isHex:
		return n.placeholder("proposal", run)
	case len(run) == 24 && isHex:
		return n.placeholder("room", run)
	}
	return run
}

// shortenLongStrings replaces long request strings with their length in characters.
// It runs on requests only; responses are always recorded in full.
func shortenLongStrings(value any) any {
	switch typed := value.(type) {
	case orderedObject:
		shortened := make(orderedObject, len(typed))
		for index, member := range typed {
			shortened[index] = objectMember{key: member.key, value: shortenLongStrings(member.value)}
		}
		return shortened
	case []any:
		shortened := make([]any, len(typed))
		for index, item := range typed {
			shortened[index] = shortenLongStrings(item)
		}
		return shortened
	case string:
		length := utf8.RuneCountInString(typed)
		if length > longRequestString {
			return fmt.Sprintf("<%d characters starting %q>", length, string([]rune(typed)[:12]))
		}
	}
	return value
}
