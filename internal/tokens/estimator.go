package tokens

import "unicode/utf8"

// The estimate heuristics of src/agentcy/tokens/estimator.py.
const (
	// CharsPerToken is the number of characters (code points) counted as one token.
	CharsPerToken = 4
	// ToolCallOverheadTokens is the fixed cost of one MCP tool call.
	ToolCallOverheadTokens = 40
)

// EstimateText approximates the tokens of text as its code point count divided by
// CharsPerToken, rounded up. Empty text costs nothing; Python's None is "" here.
func EstimateText(text string) int64 {
	characters := int64(utf8.RuneCountInString(text))
	if characters == 0 {
		return 0
	}
	return (characters + CharsPerToken - 1) / CharsPerToken
}

// EstimatePost estimates the output tokens spent producing a message and its tool call.
func EstimatePost(body string) int64 {
	return EstimateText(body) + ToolCallOverheadTokens
}

// EstimateRead estimates the input tokens of reading a page of message bodies and the
// brief, which is "" when the page carries none.
func EstimateRead(bodies []string, brief string) int64 {
	total := EstimateText(brief)
	for _, body := range bodies {
		total += EstimateText(body)
	}
	return total
}
