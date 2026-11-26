//go:build bench

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// listenWaitSeconds is the wait an idle agent passes to read_messages.
	listenWaitSeconds = 45
	pollInterval      = time.Second
	sampleInterval    = time.Second
	// settleTime lets every listener's first waiting read reach the daemon before the window opens.
	settleTime = 2 * time.Second
	// deliveryGrace is how long the scenario waits after the window for the last wakes.
	deliveryGrace = 3 * time.Second
	probePrefix   = "Load probe "
)

// loadResult is the load scenario section of the report.
type loadResult struct {
	Seconds          int          `json:"seconds"`
	Listeners        int          `json:"listeners"`
	Processes        int          `json:"processes"`
	CPUSeconds       float64      `json:"cpu_seconds"`
	DaemonCPUSeconds float64      `json:"daemon_cpu_seconds"`
	ShimCPUSeconds   float64      `json:"shim_cpu_seconds"`
	PeakRSSMB        float64      `json:"peak_rss_mb"`
	DaemonPeakRSSMB  float64      `json:"daemon_peak_rss_mb"`
	GitSpawns        int          `json:"git_spawns"`
	Polls            int          `json:"polls"`
	PollErrors       int          `json:"poll_errors"`
	Probes           int          `json:"probes"`
	PostErrors       int          `json:"post_errors"`
	WakesObserved    int          `json:"wakes_observed"`
	WakesExpected    int          `json:"wakes_expected"`
	ListenerErrors   int          `json:"listener_errors"`
	WakeLatency      latencyStats `json:"wake_latency"`
}

// probeLog records when each writer probe was sent and how long each listener took to
// receive it. Wake latency runs from just before the post request to the listener's result.
type probeLog struct {
	mu        sync.Mutex
	sent      map[int]time.Time
	latencies []time.Duration
}

// markSent records the send time of one probe.
func (p *probeLog) markSent(sequence int, at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent[sequence] = at
}

// observe records a delivery when the body is a probe this scenario sent.
func (p *probeLog) observe(body string, at time.Time) {
	rest, found := strings.CutPrefix(body, probePrefix)
	if !found {
		return
	}
	number, _, _ := strings.Cut(rest, ":")
	sequence, err := strconv.Atoi(number)
	if err != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if sentAt, ok := p.sent[sequence]; ok {
		p.latencies = append(p.latencies, at.Sub(sentAt))
	}
}

// deliveries returns a copy of the recorded wake latencies.
func (p *probeLog) deliveries() []time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]time.Duration(nil), p.latencies...)
}

// loadRun holds the moving parts of one load scenario.
type loadRun struct {
	session        *session
	data           *dataset
	operator       *operatorClient
	listeners      []*shimProcess
	probes         *probeLog
	tracker        *usageTracker
	stopping       chan struct{}
	listenerErrors atomic.Int64
	result         loadResult
}

// runLoadScenario runs the listeners, the poller, and the writer for the configured time
// and measures the daemon and the shims. The benchmark daemon must be running.
func runLoadScenario(ctx context.Context, s *session, data *dataset) (loadResult, error) {
	if s.daemon == nil {
		return loadResult{}, errors.New("the load scenario needs the benchmark daemon")
	}
	operator, err := newOperatorClient(s)
	if err != nil {
		return loadResult{}, err
	}
	show, err := operator.showTail(data.conversation)
	if err != nil {
		return loadResult{}, err
	}
	run := &loadRun{
		session:  s,
		data:     data,
		operator: operator,
		probes:   &probeLog{sent: map[int]time.Time{}},
		tracker:  newUsageTracker(),
		stopping: make(chan struct{}),
		result:   loadResult{Seconds: s.cfg.seconds, Listeners: s.cfg.listeners},
	}
	defer run.stopListeners()
	if err := run.startListeners(show.Cursor); err != nil {
		return loadResult{}, err
	}
	if err := sleepContext(ctx, settleTime); err != nil {
		return loadResult{}, err
	}
	if err := run.measureWindow(ctx); err != nil {
		return loadResult{}, err
	}
	run.waitForDeliveries(ctx)
	run.stopListeners()
	return run.finish(), nil
}

// startListeners starts one shim per listener, runs its handshake, and starts its read loop.
func (r *loadRun) startListeners(cursor int64) error {
	for index := range r.session.cfg.listeners {
		shim, err := startShim(r.session)
		if err != nil {
			return err
		}
		r.listeners = append(r.listeners, shim)
		if err := shim.initialize(); err != nil {
			return fmt.Errorf("listener %d: %w", index+1, err)
		}
		token := r.data.listenerTokens[index%len(r.data.listenerTokens)]
		go r.listen(shim, token, cursor)
	}
	return nil
}

// listen re-issues a waiting read_messages call each time one returns, as an idle agent
// does, and records the probes it receives.
func (r *loadRun) listen(shim *shimProcess, token string, cursor int64) {
	for {
		raw, err := shim.callTool("read_messages", map[string]any{
			"token": token, "after": cursor, "wait_seconds": listenWaitSeconds,
		})
		receivedAt := time.Now()
		select {
		case <-r.stopping:
			return
		default:
		}
		var page struct {
			Messages []messageEntry `json:"messages"`
			Cursor   int64          `json:"cursor"`
		}
		if err == nil {
			err = json.Unmarshal(raw, &page)
		}
		if err != nil {
			r.listenerErrors.Add(1)
			return
		}
		for _, message := range page.Messages {
			r.probes.observe(message.Body, receivedAt)
		}
		cursor = max(cursor, page.Cursor)
	}
}

// measureWindow polls, posts, and samples processes until the configured time has passed.
// The poller and the writer run in this goroutine, so each sends one request at a time.
func (r *loadRun) measureWindow(ctx context.Context) error {
	gitBefore := r.session.gitSpawns()
	if err := r.sample(); err != nil {
		return err
	}
	pollTicker := time.NewTicker(pollInterval)
	defer pollTicker.Stop()
	postTicker := time.NewTicker(time.Duration(r.session.cfg.postEvery) * time.Second)
	defer postTicker.Stop()
	sampleTicker := time.NewTicker(sampleInterval)
	defer sampleTicker.Stop()
	deadline := time.After(time.Duration(r.session.cfg.seconds) * time.Second)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			if err := r.sample(); err != nil {
				return err
			}
			r.result.GitSpawns = gitDelta(gitBefore, r.session.gitSpawns())
			return nil
		case <-pollTicker.C:
			r.poll()
		case <-postTicker.C:
			r.post()
		case <-sampleTicker.C:
			if err := r.sample(); err != nil {
				return err
			}
		}
	}
}

// gitDelta returns the git spawns in the window, or -1 when they were not counted.
func gitDelta(before int, after int) int {
	if before < 0 || after < 0 {
		return -1
	}
	return after - before
}

// poll sends one operator_show with a tail, as the terminal UI does every second.
func (r *loadRun) poll() {
	r.result.Polls++
	if _, err := r.operator.showTail(r.data.conversation); err != nil {
		r.result.PollErrors++
	}
}

// post sends one probe message from the lead, which wakes every listening worker.
func (r *loadRun) post() {
	r.result.Probes++
	sequence := r.result.Probes
	r.probes.markSent(sequence, time.Now())
	_, err := exchange(r.session.socketPath(), r.session.memberRequest("post", map[string]any{
		"token":         r.data.leadToken,
		"submission_id": fmt.Sprintf("bench-load-%d", sequence),
		"body":          fmt.Sprintf("%s%d: please check the latest finding.", probePrefix, sequence),
	}))
	if err != nil {
		r.result.PostErrors++
	}
}

// sample records CPU time and memory of the daemon group and the listener shims.
func (r *loadRun) sample() error {
	processes, err := sampleProcesses()
	if err != nil {
		return err
	}
	shimPIDs := make([]int, len(r.listeners))
	for index, shim := range r.listeners {
		shimPIDs[index] = shim.pid()
	}
	r.tracker.record(processes, r.session.daemon.processGroup(), shimPIDs)
	return nil
}

// waitForDeliveries gives listeners a short grace period to receive the last probes.
func (r *loadRun) waitForDeliveries(ctx context.Context) {
	expected := (r.result.Probes - r.result.PostErrors) * r.session.cfg.listeners
	deadline := time.Now().Add(deliveryGrace)
	for time.Now().Before(deadline) && len(r.probes.deliveries()) < expected {
		if sleepContext(ctx, 50*time.Millisecond) != nil {
			return
		}
	}
}

// stopListeners stops every listener shim once; later calls do nothing.
func (r *loadRun) stopListeners() {
	select {
	case <-r.stopping:
		return
	default:
	}
	close(r.stopping)
	for _, shim := range r.listeners {
		shim.stop()
	}
}

// finish fills in the measured numbers.
func (r *loadRun) finish() loadResult {
	result := r.result
	result.DaemonCPUSeconds, result.ShimCPUSeconds = r.tracker.cpuSeconds()
	result.CPUSeconds = result.DaemonCPUSeconds + result.ShimCPUSeconds
	result.PeakRSSMB = float64(r.tracker.peakTotalKB) / 1024
	result.DaemonPeakRSSMB = float64(r.tracker.peakDaemonKB) / 1024
	result.Processes = r.tracker.maxProcesses
	latencies := r.probes.deliveries()
	result.WakesObserved = len(latencies)
	result.WakesExpected = (result.Probes - result.PostErrors) * r.session.cfg.listeners
	result.ListenerErrors = int(r.listenerErrors.Load())
	result.WakeLatency = summarize(latencies)
	return result
}

// sleepContext waits for the duration or until the context ends.
func sleepContext(ctx context.Context, duration time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(duration):
		return nil
	}
}
