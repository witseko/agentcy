package conversation

import (
	"context"
	"database/sql"
	"strings"

	"github.com/witseko/agentcy/internal/wire"
)

// The search limits (search.py).
const (
	searchLimit   = 50
	maxTerms      = 8
	snippetTokens = 12
)

// searchTerms splits free text into Python's Unicode \w+ runs: letters, numbers and the
// underscore, without combining marks.
func searchTerms(query string) []string {
	return strings.FieldsFunc(query, func(character rune) bool {
		return !isPythonAlphanumeric(character) && character != '_'
	})
}

// matchExpression turns free text into an FTS5 query that matches every word as a
// prefix. Each word is quoted, so FTS5 operators and punctuation have no effect.
func matchExpression(query string) (string, error) {
	terms := searchTerms(query)
	if len(terms) == 0 {
		return "", reject("Search needs at least one letter or digit")
	}
	terms = terms[:min(len(terms), maxTerms)]
	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		quoted = append(quoted, `"`+term+`"*`)
	}
	return strings.Join(quoted, " "), nil
}

const selectSearchMatches = `SELECT conversation_search.room, ` + titleSourceColumn + `, conversation.archived, conversation_search.message_id, participant.label AS author, coalesce(message.created, conversation.created) AS created, snippet(conversation_search, ?, ?, ?, ?, ?) AS snippet 
FROM conversation_search JOIN conversation ON conversation.id = conversation_search.room LEFT OUTER JOIN message ON message.id = conversation_search.message_id LEFT OUTER JOIN participant ON participant.id = message.author 
WHERE conversation_search MATCH ? ORDER BY conversation_search.rank
 LIMIT ? OFFSET ?`

// searchConversations finds the query's words in every brief and published message,
// best match first. A brief match has no message, so its time is the conversation's.
func searchConversations(ctx context.Context, tx *sql.Tx, query string) (wire.Value, error) {
	expression, err := matchExpression(query)
	if err != nil {
		return wire.Value{}, err
	}
	rows, err := tx.QueryContext(ctx, selectSearchMatches, 1, titleLength+1, 0, "", "", "…", snippetTokens, expression, searchLimit, 0)
	if err != nil {
		return wire.Value{}, err
	}
	defer rows.Close()
	hits := []wire.Value{}
	for rows.Next() {
		var room, source, snippet string
		var archived bool
		var messageID, created sql.NullInt64
		var author sql.NullString
		if err := rows.Scan(&room, &source, &archived, &messageID, &author, &created, &snippet); err != nil {
			return wire.Value{}, err
		}
		hit := wire.NewObject()
		hit.Set("conversation", wire.TextValue(room))
		hit.Set("title", wire.TextValue(sessionTitle(source)))
		hit.Set("archived", wire.BoolValue(archived))
		hit.Set("message_id", nullableInt(messageID))
		hit.Set("author", nullableText(author))
		hit.Set("created", nullableInt(created))
		hit.Set("snippet", wire.TextValue(snippet))
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return wire.Value{}, err
	}
	return singleField("hits", wire.ListValue(hits)), nil
}
