//go:build conformance

package conformance

import (
	"bufio"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

// waitForMemberPresence polls status without recording the polls until member shows the
// requested listening and connected flags.
func (s *scenario) waitForMemberPresence(token, member string, wantListening, wantConnected bool) {
	s.t.Helper()
	line, _ := encodeRequest(fields{"action": "status", "workspace": s.workspace, "token": token})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		reply, err := exchange(s.daemon.socket, line, false)
		if err != nil {
			s.t.Fatalf("status poll: %v", err)
		}
		var decoded struct {
			Result struct {
				Members map[string]struct {
					Listening bool `json:"listening"`
					Connected bool `json:"connected"`
				} `json:"members"`
			} `json:"result"`
		}
		_ = json.Unmarshal([]byte(reply), &decoded)
		got := decoded.Result.Members[member]
		if got.Listening == wantListening && got.Connected == wantConnected {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.t.Fatalf("%s did not reach listening=%v connected=%v. Daemon output:\n%s",
		member, wantListening, wantConnected, s.daemon.output.String())
}

// openSessionStream opens a persistent session stream for token on the daemon socket and
// answers ping frames with pong until the returned close function runs.
func (s *scenario) openSessionStream(token string, answer bool) func() {
	s.t.Helper()
	connection, err := net.DialTimeout("unix", s.daemon.socket, 5*time.Second)
	if err != nil {
		s.t.Fatalf("dial session stream: %v", err)
	}
	payload, err := encodeRequest(fields{"action": "session", "workspace": s.workspace, "token": token})
	if err != nil {
		_ = connection.Close()
		s.t.Fatalf("encode session request: %v", err)
	}
	if _, err := connection.Write(payload); err != nil {
		_ = connection.Close()
		s.t.Fatalf("write session request: %v", err)
	}
	reader := bufio.NewReader(connection)
	ack, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(ack, `"connected":true`) {
		_ = connection.Close()
		s.t.Fatalf("unexpected session ack %q (err=%v)", ack, err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.Contains(line, `"ping":true`) {
				reply := "{\"pong\":false}\n"
				if answer {
					reply = "{\"pong\":true}\n"
				}
				if _, err := connection.Write([]byte(reply)); err != nil {
					return
				}
			}
		}
	}()
	return func() {
		_ = connection.Close()
		<-done
	}
}

// startBackgroundListen opens a blocking listen call for token and returns a function that
// waits for the reply and asserts that it carried pinged: true.
func (s *scenario) startBackgroundListen(token string) func() {
	s.t.Helper()
	payload, err := encodeRequest(fields{
		"action": "listen", "workspace": s.workspace, "token": token, "wait_seconds": 30,
	})
	if err != nil {
		s.t.Fatalf("encode listen request: %v", err)
	}
	type outcome struct {
		reply string
		err   error
	}
	finished := make(chan outcome, 1)
	go func() {
		reply, err := exchange(s.daemon.socket, payload, false)
		finished <- outcome{reply: reply, err: err}
	}()
	return func() {
		s.t.Helper()
		select {
		case got := <-finished:
			if got.err != nil {
				s.t.Fatalf("background listen failed: %v", got.err)
			}
			var decoded struct {
				Result struct {
					Page struct {
						Pinged bool `json:"pinged"`
					} `json:"page"`
				} `json:"result"`
			}
			if err := json.Unmarshal([]byte(got.reply), &decoded); err != nil {
				s.t.Fatalf("decode background listen reply: %v (%s)", err, got.reply)
			}
			if !decoded.Result.Page.Pinged {
				s.t.Fatalf("expected pinged=true in listen reply, got %s", got.reply)
			}
		case <-time.After(5 * time.Second):
			s.t.Fatalf("background listen did not wake after ping")
		}
	}
}

// TestPingAndPresence checks member and operator pings across invited, disconnected,
// session-connected, unresponsive, listening, retired and closed states, along with live
// presence flags in status.
func TestPingAndPresence(t *testing.T) {
	s := newScenario(t)
	created := s.member("lead creates", "create", fields{
		"brief": "Ping checks", "initiator": "Lead", "invitees": []any{"Worker", "Pending"},
	})
	lead := created.text("token")
	conversation := created.text("conversation")

	s.member("lead pings an invited slot", "ping", fields{"token": lead, "name": "Pending"})
	s.operator("operator pings an invited slot", "operator_ping", fields{"conversation": conversation, "name": "Pending"})

	joined := s.member("worker joins", "join", fields{"code": created.text("invitations", "Worker")})
	worker := joined.text("token")
	s.member("worker checks status to record activity", "status", fields{"token": worker})

	s.member("worker cannot ping", "ping", fields{"token": worker, "name": "Lead"})
	s.member("lead cannot ping self", "ping", fields{"token": lead, "name": "Lead"})
	s.member("lead cannot ping unknown member", "ping", fields{"token": lead, "name": "Missing"})

	s.member("lead pings a disconnected worker", "ping", fields{"token": lead, "name": "Worker"})
	s.operator("operator pings a disconnected worker", "operator_ping", fields{"conversation": conversation, "name": "Worker"})

	closeLiveSession := s.openSessionStream(worker, true)
	s.waitForMemberPresence(lead, "Worker", false, true)
	s.member("status shows connected worker", "status", fields{"token": lead})
	s.member("lead pings connected worker", "ping", fields{"token": lead, "name": "Worker"})
	s.operator("operator pings connected worker", "operator_ping", fields{"conversation": conversation, "name": "Worker"})

	waitListen := s.startBackgroundListen(worker)
	s.waitForMemberPresence(lead, "Worker", true, true)
	s.member("status shows listening and connected worker", "status", fields{"token": lead})
	s.member("lead wakes listening worker without posting", "ping", fields{"token": lead, "name": "Worker"})
	waitListen()

	closeLiveSession()
	s.waitForMemberPresence(lead, "Worker", false, false)

	closeUnresponsive := s.openSessionStream(worker, false)
	s.waitForMemberPresence(lead, "Worker", false, true)
	s.member("lead detects unresponsive host on session stream", "ping", fields{"token": lead, "name": "Worker"})
	closeUnresponsive()
	s.waitForMemberPresence(lead, "Worker", false, false)

	s.member("transcript stays empty after out-of-band pings", "read", fields{"token": lead})
	s.member("lead retires worker", "retire", fields{"token": lead, "name": "Worker"})
	s.member("lead cannot ping retired worker", "ping", fields{"token": lead, "name": "Worker"})
	s.member("lead closes conversation", "close", fields{"token": lead})
	s.member("lead cannot ping in closed conversation", "ping", fields{"token": lead, "name": "Pending"})
}
