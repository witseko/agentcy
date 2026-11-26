package core

import (
	"context"
	"time"

	"github.com/witseko/agentcy/internal/wire"
)

// Handler applies the conversation rules. The daemon validates each request line, checks
// the operator key, frames the reply, and runs waiting reads; the Handler does everything
// that touches a store. internal/conversation implements it.
//
// Contract for every method:
//
//   - The daemon calls methods from many goroutines at once, one per connection. The
//     Handler serialises its own store access (plan fix L9: one write mutex per store).
//   - now is the request time from Options.Now. Use it instead of reading the clock, so
//     tests can fix it.
//   - ctx ends when the daemon shuts down. Long store work may stop early.
//   - A returned *Rejection reaches the client as {"error": message}, like a ValueError in
//     the Python core. Any other error, and any panic, reaches the client as
//     "Core operation failed" and is logged without its text, so an error may hold paths
//     or SQL but never a token, code, key, or message body.
//   - Results are wire.Value objects, so field order is kept exactly as written.
//   - No method may hold a store connection or transaction after it returns. Waiting reads
//     call ReadPage again after each wake instead.
type Handler interface {
	// Member applies one validated member request and returns its result and the rooms to
	// wake. It is never called for a read with WaitSeconds above zero: the daemon runs
	// those through OpenWait, ReadPage, and RecordDelivery.
	//
	// Port of Core.apply in core.py: after a committed write, Notify names the room of the
	// authenticated member, or of the joined conversation for "join". For "launch",
	// validate the checkout with CheckLaunchCheckout before queueing.
	Member(ctx context.Context, request wire.Request, now time.Time) (Outcome, error)

	// Operator applies one validated operator request whose key the daemon has already
	// accepted. Port of Core.operate in core.py after its key check: resolve the checkout
	// with paths.WorkspaceKeyCache, list or search nothing for a checkout without a store,
	// and after a committed write of one of NOTIFYING_OPERATOR_ACTIONS name the request's
	// conversation in Notify.
	Operator(ctx context.Context, request wire.OperatorRequest, now time.Time) (Outcome, error)

	// OpenWait authenticates the token of a waiting read and returns the participant's
	// room and the sealed round deadline. It starts no transaction that outlives the call.
	// Port of the first transaction of wait_for_read in concurrency.py.
	OpenWait(ctx context.Context, request *wire.Read, now time.Time) (WaitStart, error)

	// ReadPage refreshes the participant and loads one page, without recording token
	// usage. The page is the result that the read returns, with the keys the daemon looks
	// at: "messages" (a list), and optionally "closed", "retired", "wake", "more", and
	// "round_deadline" (Unix seconds or null). Once a round deadline has passed, the page
	// must no longer carry it (repository.refresh_participant releases the round), or the
	// wait polls without sleeping until its own deadline, as in Python.
	// Port of read_page in concurrency.py.
	ReadPage(ctx context.Context, request *wire.Read, now time.Time) (wire.Value, error)

	// RecordDelivery records the estimated input tokens of a page that a waiting read
	// delivers. The daemon calls it once per waiting read, only for a ready page with at
	// least one message. Port of _record_delivered_usage in concurrency.py.
	RecordDelivery(ctx context.Context, request *wire.Read, page wire.Value, now time.Time) error

	// HasActiveWork reports whether any workspace store has an open conversation or a
	// pending assignment, stores of earlier processes included. The daemon exits after an
	// idle interval when it reports false. Port of Core.handle_timeout in core.py.
	HasActiveWork(ctx context.Context, now time.Time) (bool, error)
}

// Room names one conversation of one workspace. Waiting reads wait on it, and writes
// wake it.
type Room struct {
	Workspace    string
	Conversation string
}

// Outcome is the successful answer to one request.
type Outcome struct {
	// Result is sent as {"result": Result}. It is usually an object.
	Result wire.Value
	// Notify lists the rooms whose waiting reads wake. The daemon wakes them after the
	// method returns, which must be after the write committed, and before it replies.
	Notify []Room
}

// WaitStart is what a waiting read needs before its first page.
type WaitStart struct {
	Room Room
	// RoundDeadline is the sealed round deadline in Unix seconds, or nil without one. The
	// wait wakes at this time even without a write, so the round can be released.
	RoundDeadline *int64
}

// Rejection is a refusal whose message the client may see, such as
// "Invalid participant token". The message must not hold a secret.
type Rejection struct {
	Message string
}

// Error returns the message.
func (rejection *Rejection) Error() string { return rejection.Message }

// Reject returns a *Rejection with message.
func Reject(message string) error {
	return &Rejection{Message: message}
}
