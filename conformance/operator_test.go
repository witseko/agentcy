//go:build conformance

package conformance

import (
	"fmt"
	"strings"
	"testing"
)

// From test_operator.py: the human creates a conversation with an invitation for every
// slot, the lead joins, status reports the human, and posts are retry safe.
func TestOperatorCreateShowAndPost(t *testing.T) {
	s := newScenario(t)
	created := s.operator("human creates with a co-lead and open invite", "operator_create", fields{
		"brief": "Review retries\nDetails follow", "lead": "Lead",
		"invitees": []any{"Worker", fields{"name": "Partner", "role": "co-lead"}}, "open_invite": true,
	})
	conversation := created.text("conversation")
	lead := s.member("lead joins", "join", fields{"code": created.invitationCode("Lead")}).text("token")
	s.member("lead status reports the human", "status", fields{"token": lead})
	s.operator("the human posts", "operator_post", fields{"conversation": conversation, "submission_id": "n1", "body": "Go ahead."})
	s.operator("the human retries the post", "operator_post", fields{"conversation": conversation, "submission_id": "n1", "body": "Go ahead."})
	s.operator("the human reuses the ID with another body", "operator_post", fields{"conversation": conversation, "submission_id": "n1", "body": "Changed"})
	s.operator("the human addresses an unknown member", "operator_post", fields{"conversation": conversation, "submission_id": "n2", "body": "Hi", "to": "Ghost"})
	s.operator("show returns status, brief and transcript", "operator_show", fields{"conversation": conversation})
	s.member("lead reads the human's message with role human", "read", fields{"token": lead})
	s.operator("list shows the conversation", "operator_list", fields{})
	s.operator("show an unknown conversation", "operator_show", fields{"conversation": "0123456789abcdef01234567"})
	s.operator("post to an unknown conversation", "operator_post", fields{"conversation": "0123456789abcdef01234567", "submission_id": "x", "body": "Hi"})
	s.operator("operator create without invitees or open invite", "operator_create", fields{"brief": "Bad", "lead": "Lead"})
	s.operator("operator create with duplicate names", "operator_create", fields{"brief": "Bad", "lead": "Lead", "invitees": []any{"lead"}})
	s.operator("operator create with a sealed round", "operator_create", fields{"brief": "Sealed", "lead": "Lead", "invitees": []any{"W"}, "sealed": true})
	s.operator("operator create rejects a deadline without a round", "operator_create", fields{"brief": "Bad", "lead": "Lead", "invitees": []any{"W"}, "deadline_minutes": 5})
	s.operator("a literal CODE in the brief and slot name stays", "operator_create", fields{"brief": "Fix CODE handling", "lead": "CODE", "invitees": []any{"CODE reviewer"}})
}

// From test_operator.py: forward pages after a cursor, the tail page and older pages before
// an ID, and conflicting pagination modes.
func TestOperatorShowPagination(t *testing.T) {
	s := newScenario(t)
	created := s.operator("human creates", "operator_create", fields{"brief": "Paging", "lead": "Lead", "invitees": []any{"Worker"}})
	conversation := created.text("conversation")
	for number := 0; number < 103; number++ {
		s.operator(fmt.Sprintf("human posts note %d", number), "operator_post", fields{
			"conversation": conversation, "submission_id": fmt.Sprintf("n%d", number), "body": fmt.Sprintf("Note %d", number),
		})
	}
	tail := s.operator("tail returns the newest page", "operator_show", fields{"conversation": conversation, "tail": true})
	middle := s.operator("before returns the older page", "operator_show", fields{"conversation": conversation, "before": tail.number("messages", 0, "id")})
	s.operator("before the oldest page", "operator_show", fields{"conversation": conversation, "before": middle.number("messages", 0, "id")})
	s.operator("after a cursor", "operator_show", fields{"conversation": conversation, "after": 100})
	s.operator("after the end", "operator_show", fields{"conversation": conversation, "after": 500})
	s.operator("before 1 returns an empty page", "operator_show", fields{"conversation": conversation, "before": 1})
	s.operator("after and tail together", "operator_show", fields{"conversation": conversation, "after": 5, "tail": true})
	s.operator("after and before together", "operator_show", fields{"conversation": conversation, "after": 5, "before": 10})
	s.operator("before and tail together", "operator_show", fields{"conversation": conversation, "before": 10, "tail": true})
	s.operator("before of zero", "operator_show", fields{"conversation": conversation, "before": 0})
	s.operator("tail false with before is allowed", "operator_show", fields{"conversation": conversation, "before": 3, "tail": false})
}

// From test_operator.py: list order, titles, member counts, rename and the first write that
// adds the human to a conversation a chat created.
func TestOperatorListRenameAndHuman(t *testing.T) {
	s := newScenario(t)
	chat := s.member("a chat creates a conversation", "create", fields{"brief": "Chat brief", "initiator": "Lead", "invitees": []any{"W"}})
	chatConversation := chat.text("conversation")
	s.operator("show does not add the human", "operator_show", fields{"conversation": chatConversation})
	s.operator("the first human post adds the human", "operator_post", fields{"conversation": chatConversation, "submission_id": "h", "body": "Please also check timeouts."})
	s.member("the chat's lead sees the human in the roster", "read", fields{"token": chat.text("token")})
	s.operator("human creates a long-titled conversation", "operator_create", fields{"brief": strings.Repeat("x", 200), "lead": "Lead", "invitees": []any{"W"}})
	newer := s.operator("human creates the newest conversation", "operator_create", fields{"brief": "Review retries", "lead": "Lead", "invitees": []any{"W", "V"}})
	s.member("one slot joins", "join", fields{"code": newer.invitationCode("W")})
	s.operator("list is newest first with member counts", "operator_list", fields{})
	s.operator("rename sets a custom title", "operator_rename", fields{"conversation": chatConversation, "title": "  Custom title  "})
	s.operator("list shows the custom title", "operator_list", fields{})
	s.operator("show uses the custom title", "operator_show", fields{"conversation": chatConversation, "after": 1})
	s.operator("rename with 80 characters", "operator_rename", fields{"conversation": chatConversation, "title": strings.Repeat("\u00fc", 80)})
	s.operator("rename with 81 characters", "operator_rename", fields{"conversation": chatConversation, "title": strings.Repeat("\u00fc", 81)})
	s.operator("rename with a newline", "operator_rename", fields{"conversation": chatConversation, "title": "Two\nlines"})
	s.operator("rename with a tab", "operator_rename", fields{"conversation": chatConversation, "title": "Tab\there"})
	s.operator("rename with only spaces", "operator_rename", fields{"conversation": chatConversation, "title": "   "})
	s.operator("rename without a title field", "operator_rename", fields{"conversation": chatConversation})
	s.operator("rename to null uses the brief again", "operator_rename", fields{"conversation": chatConversation, "title": nil})
	s.operator("rename an unknown conversation", "operator_rename", fields{"conversation": "0123456789abcdef01234567", "title": "X"})
	s.member("the chat's lead closes", "close", fields{"token": chat.text("token")})
	s.operator("rename a closed conversation", "operator_rename", fields{"conversation": chatConversation, "title": "Closed title"})
	s.member("list_closed shows the renamed title", "list_closed", fields{})
}

// From test_operator.py and test_operator_core.py: archive needs a closed conversation,
// reopen restores archived ones, and members keep read access.
func TestOperatorArchiveAndReopen(t *testing.T) {
	s := newScenario(t)
	created := s.operator("human creates", "operator_create", fields{"brief": "Archive me", "lead": "Lead", "invitees": []any{"Worker"}})
	conversation := created.text("conversation")
	lead := s.member("lead joins", "join", fields{"code": created.invitationCode("Lead")}).text("token")
	s.operator("archive an open conversation", "operator_archive", fields{"conversation": conversation, "archived": true})
	s.operator("archive without the archived field", "operator_archive", fields{"conversation": conversation})
	s.operator("unarchive an open conversation is allowed", "operator_archive", fields{"conversation": conversation, "archived": false})
	s.operator("the human closes", "operator_close", fields{"conversation": conversation})
	s.operator("closing again", "operator_close", fields{"conversation": conversation, "summary": "Ignored"})
	s.operator("the human archives", "operator_archive", fields{"conversation": conversation, "archived": true})
	s.operator("active list is empty", "operator_list", fields{})
	s.operator("archived list has it", "operator_list", fields{"archived": true})
	s.member("the lead still reads", "read", fields{"token": lead})
	s.operator("show reports archived", "operator_show", fields{"conversation": conversation})
	s.operator("the human reopens with a note, which unarchives", "operator_reopen", fields{"conversation": conversation, "note": "Back again"})
	s.operator("reopening again", "operator_reopen", fields{"conversation": conversation})
	s.operator("active list has it again", "operator_list", fields{})
	s.member("the lead posts again", "post", fields{"token": lead, "submission_id": "again", "body": "Working"})
	s.operator("show after reopen", "operator_show", fields{"conversation": conversation})
}

// From test_operator.py and test_operator_core.py: search over briefs and published messages,
// query words as prefixes, punctuation and operators ignored, archived flag on hits.
func TestOperatorSearch(t *testing.T) {
	s := newScenario(t)
	s.operator("search without a store", "operator_search", fields{"query": "retry"})
	first := s.operator("human creates the first conversation", "operator_create", fields{"brief": "Investigate retry backoff ceilings", "lead": "Lead", "invitees": []any{"Worker"}})
	conversation := first.text("conversation")
	lead := s.member("lead joins", "join", fields{"code": first.invitationCode("Lead")}).text("token")
	s.post("lead posts about backoff", lead, fields{"body": "The backoff ceiling is 30 seconds in retry.py"})
	s.operator("the human posts about backoff", "operator_post", fields{"conversation": conversation, "submission_id": "h", "body": "Check the backoff ceiling."})
	s.operator("human creates a second conversation", "operator_create", fields{"brief": "Unrelated timeout work", "lead": "Lead", "invitees": []any{"Worker"}})
	s.operator("search for backoff", "operator_search", fields{"query": "backoff"})
	s.operator("search by prefix", "operator_search", fields{"query": "back ceil"})
	s.operator("search ignores punctuation", "operator_search", fields{"query": "\"backoff\" (ceiling)!"})
	s.operator("search treats operators as words", "operator_search", fields{"query": "backoff OR timeout"})
	s.operator("search with unicode words", "operator_search", fields{"query": "\u00fcber"})
	s.operator("search needs a word", "operator_search", fields{"query": "!!! ???"})
	s.operator("an empty query fails validation", "operator_search", fields{"query": ""})
	s.operator("a 201-character query fails validation", "operator_search", fields{"query": strings.Repeat("q", 201)})
	s.operator("only the first eight words count", "operator_search", fields{"query": "backoff ceiling one two three four five six seven"})
	s.operator("the human closes the first", "operator_close", fields{"conversation": conversation})
	s.operator("the human archives the first", "operator_archive", fields{"conversation": conversation, "archived": true})
	s.operator("hits of an archived conversation say so", "operator_search", fields{"query": "ceiling"})
}
