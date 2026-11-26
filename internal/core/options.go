package core

import (
	"context"
	"io"
	"log"
	"os"
	"time"
)

// Timing of the daemon, from src/agentcy/mcp/core.py and concurrency.py.
const (
	// DefaultIdleInterval is how long the daemon waits for a new connection before it
	// checks whether any conversation is open (server.timeout in core.py).
	DefaultIdleInterval = 60 * time.Second
	// DefaultSettleWindow is how long a waiting read keeps collecting a burst after its
	// first relevant page (SETTLE_SECONDS in concurrency.py).
	DefaultSettleWindow = 2 * time.Second
	// DefaultShutdownGrace bounds how long in-flight requests may run after a shutdown
	// begins. Waiting reads end at once with their current page.
	DefaultShutdownGrace = 5 * time.Second
	// requestTimeout bounds reading the request line and writing the reply, as the 5 s
	// socket timeout does in core.py.
	requestTimeout = 5 * time.Second
)

// The files in the data directory.
const (
	lockFileName        = "core.lock"
	socketFileName      = "core.sock"
	operatorKeyFileName = "operator.key"
)

// Options configures Run. Only DataDir is required; zero values take the defaults.
type Options struct {
	// DataDir is the bridge data directory, usually from paths.DataDir. Run checks it
	// again with paths.DataDir after setting the umask.
	DataDir string
	// IdleInterval replaces DefaultIdleInterval, for tests.
	IdleInterval time.Duration
	// SettleWindow replaces DefaultSettleWindow, for tests.
	SettleWindow time.Duration
	// ShutdownGrace replaces DefaultShutdownGrace.
	ShutdownGrace time.Duration
	// Now returns the current time; time.Now when nil. Waits and timeouts still use real
	// timers, so a fixed clock only changes the times passed to the Handler.
	Now func() time.Time
	// Log receives the daemon's log lines; standard error when nil. The daemon logs
	// states and action names, never keys, tokens, codes, or message bodies.
	Log io.Writer
	// Background, when set, runs in its own goroutine once the daemon holds the lock and
	// listens, until its context ends at shutdown; Run waits for it to return before it
	// releases the lock. wake wakes the waiting reads of a room. The daemon runs the
	// fresh-run worker here.
	Background func(ctx context.Context, wake func(Room))
}

// settings is Options with every default filled in.
type settings struct {
	dataDir       string
	idleInterval  time.Duration
	settleWindow  time.Duration
	shutdownGrace time.Duration
	now           func() time.Time
	logger        *log.Logger
	background    func(ctx context.Context, wake func(Room))
}

// withDefaults fills in the zero values of options.
func withDefaults(options Options) settings {
	result := settings{
		dataDir:       options.DataDir,
		idleInterval:  options.IdleInterval,
		settleWindow:  options.SettleWindow,
		shutdownGrace: options.ShutdownGrace,
		now:           options.Now,
		background:    options.Background,
	}
	if result.idleInterval <= 0 {
		result.idleInterval = DefaultIdleInterval
	}
	if result.settleWindow <= 0 {
		result.settleWindow = DefaultSettleWindow
	}
	if result.shutdownGrace <= 0 {
		result.shutdownGrace = DefaultShutdownGrace
	}
	if result.now == nil {
		result.now = time.Now
	}
	output := options.Log
	if output == nil {
		output = os.Stderr
	}
	result.logger = log.New(output, "agentcy core: ", log.LstdFlags)
	return result
}
