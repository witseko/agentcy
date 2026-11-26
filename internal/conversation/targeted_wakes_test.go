package conversation

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/witseko/agentcy/internal/core"
	"github.com/witseko/agentcy/internal/wire"
)

// Stress sizes: listeners wait far longer than a round may take, so a lost wake fails
// the round instead of hiding behind the wait's deadline.
const (
	stressListeners   = 8
	stressWriters     = 4
	stressRounds      = 10
	stressWaitSeconds = 30
	stressRoundLimit  = 10 * time.Second
)

// stressDaemon runs the real daemon and handler on a short data directory and returns a
// client and the function that stops the daemon, which ends every waiting read.
func stressDaemon(t *testing.T) (*wire.Client, func()) {
	t.Helper()
	dataDir, err := os.MkdirTemp("/tmp", "agentcy-wake-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dataDir) })
	handler := NewHandler(newTestShared(t, dataDir, log.New(io.Discard, "", 0)), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- core.Run(ctx, handler, core.Options{DataDir: dataDir, SettleWindow: 20 * time.Millisecond, Log: io.Discard})
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			if err := <-done; err != nil {
				t.Errorf("daemon: %v", err)
			}
		})
	}
	t.Cleanup(stop)
	socket := filepath.Join(dataDir, "core.sock")
	client := &wire.Client{Connect: func(ctx context.Context) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socket)
	}}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if connection, err := net.Dial("unix", socket); err == nil {
			connection.Close()
			return client, stop
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the daemon did not start")
	return nil, nil
}

// stressListener collects every message one member reads.
type stressListener struct {
	name   string
	token  string
	listen bool
	mutex  sync.Mutex
	seen   map[string]bool
	cursor int64
}

// has reports whether the listener read a message body.
func (listener *stressListener) has(body string) bool {
	listener.mutex.Lock()
	defer listener.mutex.Unlock()
	return listener.seen[body]
}

// run reads until ctx ends. A listener uses listen or plain waiting reads throughout, as
// a new or an old shim does.
func (listener *stressListener) run(ctx context.Context, t *testing.T, client *wire.Client) {
	for ctx.Err() == nil {
		page, err := listener.readOnce(ctx, client)
		if err != nil {
			if ctx.Err() == nil {
				t.Errorf("%s: %v", listener.name, err)
			}
			return
		}
		messages, _ := page.Get("messages")
		cursor, _ := page.Get("cursor")
		listener.mutex.Lock()
		for _, message := range messages.Items {
			body, _ := message.Get("body")
			listener.seen[body.StringOf()] = true
		}
		listener.cursor, _ = cursor.IntOf()
		listener.mutex.Unlock()
	}
}

// readOnce sends one waiting read and returns its page.
func (listener *stressListener) readOnce(ctx context.Context, client *wire.Client) (wire.Value, error) {
	listener.mutex.Lock()
	after := listener.cursor
	listener.mutex.Unlock()
	if !listener.listen {
		return client.Exchange(ctx, &wire.Read{Workspace: testWorkspace, Token: listener.token, After: after, WaitSeconds: stressWaitSeconds})
	}
	result, err := client.Exchange(ctx, &wire.Listen{Workspace: testWorkspace, Token: listener.token, After: after, WaitSeconds: stressWaitSeconds})
	page, _ := result.Get("page")
	return page, err
}

// stressPost posts one message and fails the test on an error.
func stressPost(t *testing.T, client *wire.Client, token, body, kind, to string) {
	post := &wire.Post{Workspace: testWorkspace, Token: token, SubmissionID: body, Body: body, Kind: kind}
	if to != "" {
		post.To = &to
	}
	if _, err := client.Exchange(context.Background(), post); err != nil {
		t.Errorf("post %s: %v", body, err)
	}
}

// Port plan fix L3 under load: many writers add messages that wake nobody, one listener,
// or everyone, while listeners read with listen and with plain waiting reads. Each round
// ends with a message addressed to each listener, which must arrive well before the wait
// deadline; at the end every listener has read every message addressed to it or to all.
func TestTargetedWakesLoseNoMessageUnderLoad(t *testing.T) {
	client, stopDaemon := stressDaemon(t)
	names := make([]wire.Slot, 0, stressListeners+stressWriters)
	for index := range stressListeners + stressWriters {
		names = append(names, wire.Slot{Name: fmt.Sprintf("Member %02d", index)})
	}
	created, err := client.Exchange(context.Background(), &wire.Create{Workspace: testWorkspace, Brief: "Stress", Initiator: "Lead", Invitees: names})
	if err != nil {
		t.Fatal(err)
	}
	leadToken := text(created, "token")
	tokens := map[string]string{}
	for _, slot := range names {
		joined, err := client.Exchange(context.Background(), &wire.Join{Workspace: testWorkspace, Code: text(created, "invitations", slot.Name)})
		if err != nil {
			t.Fatal(err)
		}
		tokens[slot.Name] = text(joined, "token")
	}
	listeners := make([]*stressListener, stressListeners)
	for index := range listeners {
		name := names[index].Name
		listeners[index] = &stressListener{name: name, token: tokens[name], listen: index%2 == 0, seen: map[string]bool{}}
	}
	ctx, stop := context.WithCancel(context.Background())
	var group sync.WaitGroup
	for _, listener := range listeners {
		group.Add(1)
		go func() {
			defer group.Done()
			listener.run(ctx, t, client)
		}()
	}
	noise := &noiseLog{byName: map[string][]string{}}
	writersDone := make(chan struct{})
	var writers sync.WaitGroup
	for index := range stressWriters {
		writer := names[stressListeners+index].Name
		writers.Add(1)
		go func() {
			defer writers.Done()
			stressNoise(t, client, writer, tokens[writer], names, noise, writersDone)
		}()
	}
	expected := map[string][]string{}
	for round := range stressRounds {
		for _, listener := range listeners {
			body := fmt.Sprintf("round %d for %s", round, listener.name)
			expected[listener.name] = append(expected[listener.name], body)
			stressPost(t, client, leadToken, body, "note", listener.name)
		}
		if !allRead(listeners, func(listener *stressListener) string {
			return fmt.Sprintf("round %d for %s", round, listener.name)
		}) {
			t.Errorf("round %d: a listener did not read its message within %s", round, stressRoundLimit)
			break
		}
	}
	close(writersDone)
	writers.Wait()
	stressPost(t, client, leadToken, "final note to all", "note", "")
	if !allRead(listeners, func(*stressListener) string { return "final note to all" }) {
		t.Error("a listener did not read the final note to all")
	}
	stop()
	stopDaemon()
	group.Wait()
	for _, listener := range listeners {
		for _, body := range append(expected[listener.name], noise.forListener(listener.name)...) {
			if !listener.has(body) {
				t.Errorf("%s never read %q", listener.name, body)
			}
		}
	}
}

// allRead waits until every listener read the body that bodyFor names for it.
func allRead(listeners []*stressListener, bodyFor func(*stressListener) string) bool {
	deadline := time.Now().Add(stressRoundLimit)
	for time.Now().Before(deadline) {
		missing := false
		for _, listener := range listeners {
			missing = missing || !listener.has(bodyFor(listener))
		}
		if !missing {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// noiseLog remembers the noise messages of one test run that are addressed to each
// listener. Each run has its own, because a run's store holds only its own messages.
type noiseLog struct {
	mutex  sync.Mutex
	byName map[string][]string
}

// add records a noise message for a listener.
func (log *noiseLog) add(name, body string) {
	log.mutex.Lock()
	defer log.mutex.Unlock()
	log.byName[name] = append(log.byName[name], body)
}

// forListener returns the noise messages that the listener must have read.
func (log *noiseLog) forListener(name string) []string {
	log.mutex.Lock()
	defer log.mutex.Unlock()
	return append([]string{}, log.byName[name]...)
}

// stressNoise posts until done: blockers and messages to other writers, which wake no
// listener, and now and then a message to one listener, which wakes only it.
func stressNoise(t *testing.T, client *wire.Client, writer, token string, names []wire.Slot, noise *noiseLog, done <-chan struct{}) {
	for count := 0; ; count++ {
		select {
		case <-done:
			return
		default:
		}
		body := fmt.Sprintf("noise %d from %s", count, writer)
		switch count % 5 {
		case 0, 1:
			stressPost(t, client, token, body, "blocker", "")
		case 2, 3:
			target := names[stressListeners+(count%stressWriters)].Name
			if target == writer {
				target = names[stressListeners+((count+1)%stressWriters)].Name
			}
			stressPost(t, client, token, body, "question", target)
		default:
			target := names[count%stressListeners].Name
			noise.add(target, body)
			stressPost(t, client, token, body, "finding", target)
		}
		time.Sleep(time.Millisecond)
	}
}
