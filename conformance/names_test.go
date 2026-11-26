//go:build conformance

package conformance

import (
	"strings"
	"testing"
)

// From test_name_spoofing.py: names that look like the human's are refused in create,
// invite, open-invite join and operator create; hidden characters and non-NFKC names
// fail validation; ordinary Unicode names work.
func TestReservedAndLookalikeNames(t *testing.T) {
	s := newScenario(t)
	lookalikes := []string{
		"Human", "You", "you", "\u041duman", "Hum\u0430n", "h\u054dman", "H u m a n",
		"Hum-an", "Y\u043eu", "\u0570uman", "HUMAN!",
	}
	for _, name := range lookalikes {
		s.member("initiator "+name, "create", fields{"brief": "Review", "initiator": name, "invitees": []any{"W"}})
	}
	created := s.member("lead creates an open room", "create", fields{
		"brief": "Review", "initiator": "Lead", "invitees": []any{"W"}, "open_invite": true,
	})
	lead := created.text("token")
	s.member("invitee Human", "create", fields{"brief": "Review", "initiator": "Lead", "invitees": []any{"Human"}})
	s.member("invite You", "invite", fields{"token": lead, "name": "You"})
	s.member("open join as Hum\u0430n", "join", fields{"code": created.text("open_invite", "code"), "name": "Hum\u0430n"})
	s.operator("operator lead named you", "operator_create", fields{"brief": "Review", "lead": "you", "invitees": []any{"W"}})
	hidden := []string{"Human\u200b", "HUMAN\u2060", "Hu\u200eman", "Hu\u202eman", "Hu\x00man", "Line\u2028Break"}
	for _, name := range hidden {
		s.member("hidden character in invite", "invite", fields{"token": lead, "name": name})
	}
	notNFKC := []string{"\uff28uman", "\ufb01le", "Le\u00a0ad", "e\u0301"}
	for _, name := range notNFKC {
		s.member("non-NFKC name in invite", "invite", fields{"token": lead, "name": name})
	}
	valid := []string{"\u041c\u0430\u0440\u0438\u044f", "Jos\u00e9", "Worker 2", "Agent7", "Humanoid", "Youth", strings.Repeat("\u00e9", 64)}
	for _, name := range valid {
		s.member("valid name "+name, "invite", fields{"token": lead, "name": name})
	}
	s.member("65 characters is too long", "invite", fields{"token": lead, "name": strings.Repeat("\u00e9", 65)})
	s.member("the roster lists the valid names", "status", fields{"token": lead})
}

// From test_name_spoofing.py: bodies reject control characters but keep newlines and
// tabs; whitespace is stripped; the human's messages carry role "human".
func TestMessageBodyText(t *testing.T) {
	s := newScenario(t)
	created := s.operator("human creates", "operator_create", fields{"brief": "Review", "lead": "Lead", "invitees": []any{"Worker"}})
	conversation := created.text("conversation")
	lead := s.member("lead joins", "join", fields{"code": created.invitationCode("Lead")}).text("token")
	s.post("a bell character is refused", lead, fields{"body": "Ring\u0007"})
	s.post("an escape character is refused", lead, fields{"body": "Color \u001b[31m"})
	s.post("a body of only spaces is refused", lead, fields{"body": "   \n\t "})
	s.post("8193 characters is too long", lead, fields{"body": strings.Repeat("x", 8193)})
	s.post("tabs, newlines and carriage returns are kept, outer space stripped", lead, fields{"body": "  line one\r\n\tline two  \n"})
	s.post("format characters are allowed in a body", lead, fields{"body": "zero\u200bwidth and \u00a0nbsp"})
	s.post("unicode and HTML characters", lead, fields{"body": "<b>Gr\u00fc\u00dfe</b> & \u65e5\u672c\u8a9e \"quoted\" 'single'"})
	s.operator("the human posts a note", "operator_post", fields{"conversation": conversation, "submission_id": "h1", "body": "From the user"})
	s.operator("the human cannot post a task kind", "operator_post", fields{"conversation": conversation, "submission_id": "h2", "body": "Do it", "kind": "task"})
	s.operator("the human cannot set due_minutes", "operator_post", fields{"conversation": conversation, "submission_id": "h3", "body": "Soon", "due_minutes": 5})
	s.operator("the human asks a question", "operator_post", fields{"conversation": conversation, "submission_id": "h4", "body": "Why?", "kind": "question", "to": "lead"})
	s.member("lead reads every message with roles", "read", fields{"token": lead})
}
