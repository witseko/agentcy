package core

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/witseko/agentcy/internal/paths"
	"github.com/witseko/agentcy/internal/wire"
)

// lockIsFree reports whether another process could take core.lock now.
func lockIsFree(t *testing.T, dataDir string) bool {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(dataDir, lockFileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil
}

// holdLock takes core.lock for the rest of the test, as a running daemon would.
func holdLock(t *testing.T, dataDir string) {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(dataDir, lockFileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
}

// leaveStaleSocket creates a socket file that nobody listens on, as a killed daemon leaves.
func leaveStaleSocket(t *testing.T, socketPath string) {
	t.Helper()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	listener.Close()
}

func TestSecondDaemonExitsCleanlyWhileTheFirstHoldsTheLock(t *testing.T) {
	first := startDaemon(t, &fakeHandler{}, Options{})
	key := first.operatorKeyText()
	second := launchDaemonIn(t, first.dataDir, &fakeHandler{}, Options{})
	if err := second.wait(5 * time.Second); err != nil {
		t.Errorf("second daemon returned %v, want nil like the Python exit status 0", err)
	}
	if first.operatorKeyText() != key {
		t.Error("the second daemon rewrote the key without the lock")
	}
	if handled, _ := resultOf(t, first.send(statusLine(), false)).Get("handled"); handled.StringOf() != "status" {
		t.Error("the first daemon stopped serving")
	}
}

func TestStaleSocketIsRemovedOnlyAfterTakingTheLock(t *testing.T) {
	dataDir := shortDataDir(t)
	socketPath := filepath.Join(dataDir, socketFileName)
	leaveStaleSocket(t, socketPath)
	holdLock(t, dataDir)
	blocked := launchDaemonIn(t, dataDir, &fakeHandler{}, Options{})
	if err := blocked.wait(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(socketPath); err != nil {
		t.Errorf("a daemon without the lock removed the socket: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dataDir, operatorKeyFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a daemon without the lock wrote a key: %v", err)
	}
}

func TestStaleSocketIsReplaced(t *testing.T) {
	dataDir := shortDataDir(t)
	leaveStaleSocket(t, filepath.Join(dataDir, socketFileName))
	daemon := startDaemonIn(t, dataDir, &fakeHandler{}, Options{})
	if handled, _ := resultOf(t, daemon.send(statusLine(), false)).Get("handled"); handled.StringOf() != "status" {
		t.Error("the daemon does not serve on the replaced socket")
	}
}

func TestSocketAndLockFileHaveMode0600(t *testing.T) {
	daemon := startDaemon(t, &fakeHandler{}, Options{})
	for _, name := range []string{socketFileName, lockFileName} {
		info, err := os.Lstat(filepath.Join(daemon.dataDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %o, want 600", name, info.Mode().Perm())
		}
	}
}

func TestDataDirWithGroupOrOtherBitsIsRefused(t *testing.T) {
	dataDir := shortDataDir(t)
	if err := os.Chmod(dataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	daemon := launchDaemonIn(t, dataDir, &fakeHandler{}, Options{})
	if err := daemon.wait(5 * time.Second); !errors.Is(err, paths.ErrDataDirUnsafe) {
		t.Errorf("run returned %v, want ErrDataDirUnsafe", err)
	}
	if _, err := os.Lstat(filepath.Join(dataDir, lockFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Error("the daemon created files in an unsafe directory")
	}
}

func TestIdleExitWithoutActiveWork(t *testing.T) {
	var checks atomic.Int32
	handler := &fakeHandler{hasActiveWork: func() (bool, error) {
		return checks.Add(1) <= 2, nil
	}}
	daemon := startDaemon(t, handler, Options{IdleInterval: 50 * time.Millisecond})
	if err := daemon.wait(5 * time.Second); err != nil {
		t.Fatalf("idle exit returned %v", err)
	}
	if checks.Load() != 3 {
		t.Errorf("%d idle checks, want 3: two with work, one without", checks.Load())
	}
	if _, err := os.Lstat(daemon.socket); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket left after idle exit: %v", err)
	}
	if !lockIsFree(t, daemon.dataDir) {
		t.Error("lock held after idle exit")
	}
}

func TestIdleTimerRestartsAtEachConnection(t *testing.T) {
	var checks atomic.Int32
	handler := &fakeHandler{hasActiveWork: func() (bool, error) {
		checks.Add(1)
		return false, nil
	}}
	daemon := startDaemon(t, handler, Options{IdleInterval: 400 * time.Millisecond})
	for range 10 {
		daemon.send(statusLine(), false)
		time.Sleep(100 * time.Millisecond)
	}
	if checks.Load() != 0 {
		t.Errorf("%d idle checks while requests kept arriving, want 0", checks.Load())
	}
	if err := daemon.wait(5 * time.Second); err != nil || checks.Load() != 1 {
		t.Errorf("after the requests stopped: run %v, %d checks, want nil and 1", err, checks.Load())
	}
}

func TestIdleCheckFailureStopsTheDaemon(t *testing.T) {
	failure := errors.New("store unreadable")
	handler := &fakeHandler{hasActiveWork: func() (bool, error) { return false, failure }}
	daemon := startDaemon(t, handler, Options{IdleInterval: 50 * time.Millisecond})
	if err := daemon.wait(5 * time.Second); !errors.Is(err, failure) {
		t.Errorf("run returned %v, want the check's error", err)
	}
	if _, err := os.Lstat(daemon.socket); !errors.Is(err, os.ErrNotExist) {
		t.Error("socket left after a failed idle check")
	}
}

func TestShutdownLetsInFlightRequestsFinish(t *testing.T) {
	started := make(chan struct{})
	handler := &fakeHandler{member: func(_ context.Context, request wire.Request) (Outcome, error) {
		close(started)
		time.Sleep(300 * time.Millisecond)
		return resultNaming(request.Action()), nil
	}}
	daemon := startDaemon(t, handler, Options{})
	replies := make(chan string, 1)
	go func() {
		reply, _ := exchange(daemon.socket, []byte(statusLine()), false)
		replies <- reply
	}()
	<-started
	daemon.cancel()
	if handled, _ := resultOf(t, <-replies).Get("handled"); handled.StringOf() != "status" {
		t.Error("the in-flight request lost its reply")
	}
	if err := daemon.wait(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := net.Dial("unix", daemon.socket); err == nil {
		t.Error("the daemon still accepts after shutdown")
	}
	if _, err := os.Lstat(daemon.socket); !errors.Is(err, os.ErrNotExist) {
		t.Error("socket left after shutdown")
	}
	if !lockIsFree(t, daemon.dataDir) {
		t.Error("lock held after shutdown")
	}
}

func TestShutdownGraceCancelsRequestsThatDoNotFinish(t *testing.T) {
	started := make(chan struct{})
	handler := &fakeHandler{member: func(ctx context.Context, _ wire.Request) (Outcome, error) {
		close(started)
		<-ctx.Done()
		return Outcome{}, ctx.Err()
	}}
	daemon := startDaemon(t, handler, Options{ShutdownGrace: 100 * time.Millisecond})
	replies := make(chan string, 1)
	go func() {
		reply, _ := exchange(daemon.socket, []byte(statusLine()), false)
		replies <- reply
	}()
	<-started
	stoppedAt := time.Now()
	if err := daemon.stop(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(stoppedAt); elapsed > 2*time.Second {
		t.Errorf("shutdown took %v with a 100 ms grace", elapsed)
	}
	if got := errorOf(t, <-replies); got != coreOperationFailed {
		t.Errorf("cancelled request got %q, want %q", got, coreOperationFailed)
	}
}

func TestSIGTERMStopsTheDaemonAndRemovesTheSocket(t *testing.T) {
	dataDir := shortDataDir(t)
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), &fakeHandler{}, Options{DataDir: dataDir, Log: &lockedBuffer{}})
	}()
	socketPath := filepath.Join(dataDir, socketFileName)
	served := eventually(t, 5*time.Second, func() bool {
		connection, err := net.Dial("unix", socketPath)
		if err == nil {
			connection.Close()
		}
		return err == nil
	})
	if !served {
		t.Fatal("daemon did not start")
	}
	// Run handles SIGTERM itself while it runs, so the signal does not end the test binary.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v after SIGTERM", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SIGTERM did not stop the daemon")
	}
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Error("socket left after SIGTERM")
	}
}
