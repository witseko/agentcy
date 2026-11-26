package core

import (
	"context"
	"errors"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/witseko/agentcy/internal/paths"
)

// acceptRetryDelay spaces out retries after a failed accept, such as one that ran out of
// file descriptors, so the loop does not spin.
const acceptRetryDelay = 50 * time.Millisecond

// server is one daemon run. It is used once.
type server struct {
	handler  Handler
	settings settings
	hub      *hub
	key      operatorKey
	// draining closes when shutdown begins, which ends every waiting read at once.
	draining chan struct{}
}

// Run serves the daemon until ctx ends, SIGTERM or SIGINT arrives, or the idle check finds
// no open conversation and no pending assignment. It returns nil after a clean stop, and
// also at once, without touching the socket or the key, when another daemon holds the
// lock: the Python core exits with status 0 there too.
//
// Run changes process state: it sets the umask to 077, and while it runs it handles
// SIGTERM and SIGINT itself.
//
// Startup follows main in core.py: umask, data directory check, a non-blocking flock on
// core.lock, a new operator.key, and only then the removal of a stale core.sock and a new
// listening socket with mode 0600. Shutdown stops accepting, ends waiting reads, gives
// other requests Options.ShutdownGrace to finish, removes the socket, and releases the
// lock.
func Run(ctx context.Context, handler Handler, options Options) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return newServer(handler, options).run(ctx)
}

// newServer prepares a run with the defaults filled in.
func newServer(handler Handler, options Options) *server {
	return &server{
		handler:  handler,
		settings: withDefaults(options),
		hub:      newHub(),
		draining: make(chan struct{}),
	}
}

// run performs the startup and then serves until a stop.
func (server *server) run(ctx context.Context) error {
	syscall.Umask(0o077)
	dataDir, err := paths.DataDir(server.settings.dataDir, true)
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dataDir, lockFileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	// Closing the file releases the lock, after the socket is gone.
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil
		}
		return err
	}
	if server.key, err = writeOperatorKey(dataDir); err != nil {
		return err
	}
	socketPath := filepath.Join(dataDir, socketFileName)
	listener, err := listenPrivate(socketPath)
	if err != nil {
		return err
	}
	defer removeSocket(socketPath)
	server.settings.logger.Printf("serving")
	cancelBackground, backgroundDone := server.startBackground(ctx)
	defer cancelBackground()
	return server.serve(ctx, listener, socketPath, cancelBackground, backgroundDone)
}

// startBackground runs Options.Background, if any, and returns a cancel function and a
// channel that closes when the background function returns.
func (server *server) startBackground(ctx context.Context) (context.CancelFunc, <-chan struct{}) {
	finished := make(chan struct{})
	if server.settings.background == nil {
		close(finished)
		return func() {}, finished
	}
	backgroundCtx, cancel := context.WithCancel(ctx)
	go func() {
		defer close(finished)
		server.settings.background(backgroundCtx, server.hub.notify)
	}()
	return cancel, finished
}

// listenPrivate replaces a socket left by an earlier daemon and listens with mode 0600.
// The caller must hold the lock, so the old socket cannot belong to a live daemon.
func listenPrivate(socketPath string) (*net.UnixListener, error) {
	removeSocket(socketPath)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

// removeSocket deletes the socket file. A missing file is the normal case, and any other
// failure leaves a file that the next listen reports or the next daemon removes.
func removeSocket(socketPath string) {
	_ = os.Remove(socketPath)
}

// serve accepts connections until a stop, then shuts down. Port of the handle_request
// loop in core.py: the idle check runs after IdleInterval without a new connection.
func (server *server) serve(ctx context.Context, listener *net.UnixListener, socketPath string, cancelBackground context.CancelFunc, backgroundDone <-chan struct{}) error {
	// Requests keep running after a stop until the grace period ends.
	requestCtx, cancelRequests := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelRequests()
	var inFlight sync.WaitGroup
	activity := make(chan struct{}, 1)
	acceptDone := make(chan error, 1)
	go func() { acceptDone <- server.acceptLoop(requestCtx, listener, &inFlight, activity) }()
	result, acceptStopped := server.waitForStop(ctx, requestCtx, activity, acceptDone)
	listener.Close()
	removeSocket(socketPath)
	cancelBackground()
	if !acceptStopped {
		<-acceptDone
	}
	server.drain(&inFlight, cancelRequests, backgroundDone)
	server.settings.logger.Printf("stopped")
	return result
}

// waitForStop returns when ctx ends, the accept loop fails, or the idle check finds no
// work. acceptStopped reports whether the accept loop has already returned.
func (server *server) waitForStop(ctx, requestCtx context.Context, activity <-chan struct{}, acceptDone <-chan error) (result error, acceptStopped bool) {
	idle := time.NewTimer(server.settings.idleInterval)
	defer idle.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, false
		case <-activity:
			idle.Reset(server.settings.idleInterval)
		case err := <-acceptDone:
			return err, true
		case <-idle.C:
			active, err := server.handler.HasActiveWork(requestCtx, server.settings.now())
			if err != nil {
				// core.py lets this error end the daemon; the next shim starts a new one.
				server.settings.logger.Printf("idle check failed")
				return err, false
			}
			if !active {
				return nil, false
			}
			idle.Reset(server.settings.idleInterval)
		}
	}
}

// acceptLoop starts one goroutine per connection until the listener closes. Python's
// ThreadingUnixStreamServer has no cap on concurrent requests, and neither does this.
func (server *server) acceptLoop(ctx context.Context, listener *net.UnixListener, inFlight *sync.WaitGroup, activity chan<- struct{}) error {
	for {
		connection, err := listener.Accept()
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		if err != nil {
			// socketserver ignores a failed accept and waits for the next connection.
			timer := time.NewTimer(acceptRetryDelay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-server.draining:
				timer.Stop()
				return nil
			}
			continue
		}
		select {
		case activity <- struct{}{}:
		default:
		}
		inFlight.Add(1)
		go func() {
			defer inFlight.Done()
			server.serveConnection(ctx, connection)
		}()
	}
}

// drain ends waiting reads, waits up to the grace period for the other requests and the
// background worker, and then cancels remaining requests. Reads and writes on a
// connection have their own 5 s deadlines, so the final wait is bounded once the Handler
// honours its context.
func (server *server) drain(inFlight *sync.WaitGroup, cancelRequests context.CancelFunc, backgroundDone <-chan struct{}) {
	close(server.draining)
	finished := make(chan struct{})
	go func() {
		inFlight.Wait()
		close(finished)
	}()
	grace := time.NewTimer(server.settings.shutdownGrace)
	defer grace.Stop()
	requestsDone := false
	bgDone := false
	for !requestsDone || !bgDone {
		var finishedCh <-chan struct{}
		if !requestsDone {
			finishedCh = finished
		}
		var bgCh <-chan struct{}
		if !bgDone {
			bgCh = backgroundDone
		}
		select {
		case <-finishedCh:
			requestsDone = true
		case <-bgCh:
			bgDone = true
		case <-grace.C:
			cancelRequests()
			if !requestsDone {
				<-finished
			}
			return
		}
	}
}
