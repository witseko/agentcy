package core

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackgroundRunsWhileServingAndEndsBeforeTheLockIsReleased(t *testing.T) {
	var running atomic.Bool
	started := make(chan func(Room), 1)
	options := Options{Background: func(ctx context.Context, wake func(Room)) {
		running.Store(true)
		started <- wake
		<-ctx.Done()
		running.Store(false)
	}}
	daemon := startDaemon(t, &fakeHandler{}, options)
	select {
	case wake := <-started:
		// Waking a room without listeners is allowed and does nothing.
		wake(Room{Workspace: "workspace", Conversation: "room"})
	case <-time.After(5 * time.Second):
		t.Fatal("background work did not start")
	}
	if err := daemon.stop(); err != nil {
		t.Fatal(err)
	}
	if running.Load() {
		t.Error("the daemon returned before its background work ended")
	}
	if !lockIsFree(t, daemon.dataDir) {
		t.Error("the lock is still held")
	}
}

func TestBackgroundDoesNotRunWithoutTheLock(t *testing.T) {
	first := startDaemon(t, &fakeHandler{}, Options{})
	var ran atomic.Bool
	second := launchDaemonIn(t, first.dataDir, &fakeHandler{}, Options{Background: func(context.Context, func(Room)) {
		ran.Store(true)
	}})
	if err := second.wait(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if ran.Load() {
		t.Error("a daemon without the lock ran background work")
	}
}
