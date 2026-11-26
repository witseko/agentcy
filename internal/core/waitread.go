package core

import (
	"context"
	"time"

	"github.com/witseko/agentcy/internal/wire"
)

// waitForRead runs a waiting read and records the delivery of the page it returns.
func (server *server) waitForRead(ctx context.Context, request *wire.Read) (wire.Value, error) {
	page, err := server.waitForPage(ctx, request)
	if err != nil {
		return wire.Value{}, err
	}
	if !page.Ready || !page.HasMessages {
		return page.Page, nil
	}
	return page.Page, server.handler.RecordDelivery(ctx, request, page.Page, server.settings.now())
}

// listen runs a waiting read and returns its page together with the caller's status
// (port plan fix L2), so a shim needs one exchange instead of a read and a status.
func (server *server) listen(ctx context.Context, request *wire.Listen) (wire.Value, error) {
	read := request.Read()
	page, err := server.waitForPage(ctx, read)
	if err != nil {
		return wire.Value{}, err
	}
	status, err := server.handler.FinishListen(ctx, read, page.Page, page.Ready && page.HasMessages, server.settings.now())
	if err != nil {
		return wire.Value{}, err
	}
	result := wire.NewObject()
	result.Set("page", page.Page)
	result.Set("status", status)
	return result, nil
}

// waitForPage waits on committed room changes and settles a newly arrived burst. Port of
// wait_for_read in concurrency.py, without its delivery record:
//
//   - A ready first page returns at once.
//   - Otherwise the read sleeps until a wake, its deadline, or the sealed round deadline,
//     whichever comes first, and reads the page again after each wake.
//   - After the first ready page it keeps reading for up to the settle window, unless the
//     page is closed, retired, or full.
//   - Without a ready page it returns the last page at its deadline. Every sleep ends with
//     a new read, so that page also shows messages that did not wake the read.
//
// Deadlines use the real clock, not Options.Now. No store connection is held while it
// sleeps. A shutdown ends the wait with the current page.
func (server *server) waitForPage(ctx context.Context, request *wire.Read) (WaitPage, error) {
	start, err := server.handler.OpenWait(ctx, request, server.settings.now())
	if err != nil {
		return WaitPage{}, err
	}
	listener := server.hub.join(start.Room)
	defer listener.leave()
	deadline := time.Now().Add(time.Duration(request.WaitSeconds) * time.Second)
	roundDeadline := start.RoundDeadline
	page, err := server.readPage(ctx, request, listener)
	if err != nil || page.Ready {
		return page, err
	}
	if page.RoundDeadline != nil {
		roundDeadline = page.RoundDeadline
	}
	var lastExpiredRound *int64
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 || server.stopping(ctx) {
			return page, nil
		}
		if roundDeadline != nil {
			roundRemaining := time.Until(time.Unix(*roundDeadline, 0))
			if roundRemaining > 0 {
				remaining = min(remaining, roundRemaining)
			} else if lastExpiredRound == nil || *lastExpiredRound != *roundDeadline {
				expired := *roundDeadline
				lastExpiredRound = &expired
				remaining = 0
			}
		}
		server.sleep(ctx, listener.changed(), remaining)
		if ctx.Err() != nil {
			return page, nil
		}
		if page, err = server.readPage(ctx, request, listener); err != nil {
			return WaitPage{}, err
		}
		roundDeadline = page.RoundDeadline
		if page.Ready {
			if server.stopping(ctx) {
				return page, nil
			}
			return server.settle(ctx, request, listener, page)
		}
	}
}

// readPage reads one page and narrows the listener's wakes to the reader as it read.
func (server *server) readPage(ctx context.Context, request *wire.Read, listener *listener) (WaitPage, error) {
	listener.beginRead()
	waitPage, err := server.handler.ReadPage(ctx, request, server.settings.now())
	if err != nil {
		return WaitPage{}, err
	}
	listener.setFilter(waitPage.Filter)
	return waitPage, nil
}

// settle collects a short burst, returning at once when the page is closed, retired, or
// full. Port of settle in concurrency.py. Every added message wakes the read while it
// settles, as every write did in Python, so the settled page shows the whole burst and a
// burst that fills the page ends the settle at once.
func (server *server) settle(ctx context.Context, request *wire.Read, listener *listener, page WaitPage) (WaitPage, error) {
	listener.startSettle()
	deadline := time.Now().Add(server.settings.settleWindow)
	for !page.EndsSettle && !server.stopping(ctx) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		server.sleep(ctx, listener.changed(), remaining)
		if server.stopping(ctx) {
			return page, nil
		}
		next, err := server.readPage(ctx, request, listener)
		if err != nil {
			return WaitPage{}, err
		}
		page = next
	}
	return page, nil
}

// sleep returns after duration, when changed closes, or when the daemon stops. A zero
// duration returns at once, as Condition.wait(0) does.
func (server *server) sleep(ctx context.Context, changed <-chan struct{}, duration time.Duration) {
	if duration <= 0 {
		return
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-changed:
	case <-timer.C:
	case <-server.draining:
	case <-ctx.Done():
	}
}

// stopping reports whether shutdown has begun or the request was cancelled.
func (server *server) stopping(ctx context.Context) bool {
	select {
	case <-server.draining:
		return true
	default:
		return ctx.Err() != nil
	}
}
