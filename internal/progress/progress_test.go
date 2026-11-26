package progress

import (
	"strings"
	"testing"

	"github.com/witseko/agentcy/internal/wire"
)

// statusOf parses a status result for a test.
func statusOf(t *testing.T, text string) Status {
	t.Helper()
	value, err := wire.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return StatusFromValue(value)
}

func TestLeadWaitingOnPicksTheMostUrgentGroup(t *testing.T) {
	cases := []struct {
		members string
		want    string
	}{
		{`"A": {"role": "worker", "state": "joined"}, "B": {"role": "worker", "state": "blocked"}`, "A"},
		{`"B": {"role": "worker", "state": "blocked"}, "C": {"role": "worker", "state": "invited"}`, "B"},
		{`"C": {"role": "worker", "state": "invited"}, "D": {"role": "worker", "state": "invited", "launch_state": "failed"}`, "C"},
		{`"F": {"role": "worker", "state": "invited", "launch_state": "starting"}, "C": {"role": "worker", "state": "invited"}`, "F"},
		{`"T": {"role": "worker", "state": "replied", "open_tasks": [{"id": 1}]}, "A": {"role": "worker", "state": "joined"}`, "T"},
		{`"T": {"role": "worker", "state": "replied", "open_tasks": [{"id": 1, "blocked": true}]}, "A": {"role": "worker", "state": "joined"}`, "A"},
		{`"R": {"role": "worker", "state": "retired"}, "D": {"role": "worker", "state": "done"}`, ""},
	}
	for _, test := range cases {
		status := statusOf(t, `{"members": {"Lead": {"role": "lead", "state": "replied"}, `+test.members+`}}`)
		if got := strings.Join(LeadWaitingOn(status, "Lead"), ","); got != test.want {
			t.Errorf("%s: waiting on %q, want %q", test.members, got, test.want)
		}
	}
	worker := statusOf(t, `{"members": {"W": {"role": "worker", "state": "replied"}, "A": {"role": "worker", "state": "joined"}}}`)
	if got := LeadWaitingOn(worker, "W"); got != nil {
		t.Errorf("a worker waits on %v", got)
	}
}

func TestFreshRunStates(t *testing.T) {
	status := statusOf(t, `{"members": {"Q": {"role": "worker", "state": "invited", "launch_state": "queued"},
		"E": {"role": "worker", "state": "replied", "launch_state": "finished"},
		"N": {"role": "worker", "state": "joined", "launch_state": null}}}`)
	queued, _ := status.Member("Q")
	ended, _ := status.Member("E")
	none, _ := status.Member("N")
	if !AwaitingFreshJoin(queued) || !MemberIsWorking(queued) || FreshRunEnded(queued) {
		t.Errorf("queued run: %+v", queued)
	}
	if !FreshRunEnded(ended) || MemberIsWorking(ended) {
		t.Errorf("finished run: %+v", ended)
	}
	if FreshRunEnded(none) || !none.HasLaunchState || !MemberIsWorking(none) {
		t.Errorf("null launch state: %+v", none)
	}
}

func TestQuietestWorkingPeerPrefersTheFirstOnATie(t *testing.T) {
	status := statusOf(t, `{"members": {"A": {"role": "worker", "state": "joined", "idle_seconds": 200},
		"B": {"role": "worker", "state": "joined", "idle_seconds": 200, "open_tasks": [{"id": 3}, {"id": 4, "blocked": true}]},
		"C": {"role": "worker", "state": "done", "idle_seconds": 900}}}`)
	peer, found := QuietestWorkingPeer(status.Members, []string{"B", "A", "C"}, TaskOwners(status))
	if !found || peer.Name != "B" || len(peer.TaskIDs) != 1 || peer.TaskIDs[0] != 3 {
		t.Errorf("peer %+v, found %v", peer, found)
	}
}

func TestBlockedPeersAndCurrentBlockers(t *testing.T) {
	status := statusOf(t, `{"members": {"Me": {"role": "worker", "state": "replied"},
		"S": {"role": "worker", "state": "blocked", "open_tasks": [{"id": 9, "blocked": true}]},
		"H": {"role": "human", "state": "blocked"}}}`)
	peers := BlockedPeers(status.Members, "Me")
	if len(peers) != 1 || peers[0].Name != "S" || len(peers[0].Tasks) != 1 {
		t.Errorf("peers %+v", peers)
	}
	if !IsCurrentBlocker(Message{Label: "S", Kind: "blocker", ReplyTo: 9}, status.Members, true) {
		t.Error("an open task blocker is not current")
	}
	if IsCurrentBlocker(Message{Label: "S", Kind: "blocker", ReplyTo: 8}, status.Members, true) {
		t.Error("a blocker on another task is current")
	}
	if !IsCurrentBlocker(Message{Label: "S", Kind: "blocker"}, status.Members, false) {
		t.Error("a standalone blocker of a blocked member is not current")
	}
}

func TestStatusKeepsMemberOrder(t *testing.T) {
	status := statusOf(t, `{"open": true, "participant": "Z", "members": {"Z": {"role": "lead", "state": "replied"}, "A": {"role": "worker", "state": "done"}}}`)
	if len(status.Members) != 2 || status.Members[0].Name != "Z" || !status.Open || status.Participant != "Z" {
		t.Errorf("status %+v", status)
	}
}
