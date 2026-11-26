package core

import (
	"context"
	"time"

	"github.com/witseko/agentcy/internal/wire"
)

// waitForRead waits on committed room changes and settles a newly arrived burst. Port of
// wait_for_read in concurrency.py:
//
//   - A ready first page returns at once.
//   - Otherwise the read sleeps until a notify, its deadline, or the sealed round deadline,
//     whichever comes first, and reads the page again after each wake.
//   - After the first ready page it keeps reading for up to the settle window, unless the
//     page is closed, retired, or full.
//   - Without a ready page it returns the last page at its deadline.
//
// Deadlines use the real clock, not Options.Now. No store connection is held while it
// sleeps. A shutdown ends the wait with the current
// page.
func (server *server) waitForRead(ctx context.Context, request *wire.Read) (wire.Value, error) {
	start, err := server.handler.OpenWait(ctx, request, server.settings.now())
	if err != nil {
		return wire.Value{}, err
	}
	listener := server.hub.join(start.Room)
	defer listener.leave()
	deadline := time.Now().Add(time.Duration(request.WaitSeconds) * time.Second)
	roundDeadline := start.RoundDeadline
	changed := listener.changed()
	page, err := server.handler.ReadPage(ctx, request, server.settings.now())
	if err != nil {
		return wire.Value{}, err
	}
	if pageIsReady(page) {
		return page, server.recordDelivery(ctx, request, page)
	}
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 || server.stopping(ctx) {
			return page, nil
		}
		if roundDeadline != nil {
			remaining = min(remaining, max(0, time.Until(time.Unix(*roundDeadline, 0))))
		}
		server.sleep(ctx, changed, remaining)
		changed = listener.changed()
		if page, err = server.handler.ReadPage(ctx, request, server.settings.now()); err != nil {
			return wire.Value{}, err
		}
		roundDeadline = pageRoundDeadline(page)
		if pageIsReady(page) {
			settled, err := server.settle(ctx, request, listener, changed, page)
			if err != nil {
				return wire.Value{}, err
			}
			return settled, server.recordDelivery(ctx, request, settled)
		}
	}
}

// settle collects a short burst, returning at once when the page is closed, retired, or
// full. Port of settle in concurrency.py.
func (server *server) settle(ctx context.Context, request *wire.Read, listener *listener, changed <-chan struct{}, page wire.Value) (wire.Value, error) {
	deadline := time.Now().Add(server.settings.settleWindow)
	for !pageEndsSettle(page) && !server.stopping(ctx) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		server.sleep(ctx, changed, remaining)
		changed = listener.changed()
		next, err := server.handler.ReadPage(ctx, request, server.settings.now())
		if err != nil {
			return wire.Value{}, err
		}
		page = next
	}
	return page, nil
}

// recordDelivery records token usage once, for a ready page that delivers messages.
func (server *server) recordDelivery(ctx context.Context, request *wire.Read, page wire.Value) error {
	if !pageIsReady(page) || pageMessages(page).Len() == 0 {
		return nil
	}
	return server.handler.RecordDelivery(ctx, request, page, server.settings.now())
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

// pageIsReady recognises relevant messages, a close, and a retirement even without
// outcome text: page.closed or page.retired or page.get("wake", bool(page.messages)).
// Port of ready in concurrency.py.
func pageIsReady(page wire.Value) bool {
	if pageFlag(page, "closed") || pageFlag(page, "retired") {
		return true
	}
	if wake, found := page.Get("wake"); found {
		return wake.Truthy()
	}
	return pageMessages(page).Len() > 0
}

// pageEndsSettle reports a closed, retired, or full page, which ends the settle window.
func pageEndsSettle(page wire.Value) bool {
	return pageFlag(page, "closed") || pageFlag(page, "retired") || pageFlag(page, "more")
}

// pageFlag reads one optional flag with Python truthiness.
func pageFlag(page wire.Value, name string) bool {
	value, _ := page.Get(name)
	return value.Truthy()
}

// pageMessages returns the page's message list, or an empty value.
func pageMessages(page wire.Value) wire.Value {
	messages, _ := page.Get("messages")
	return messages
}

// pageRoundDeadline returns the page's sealed round deadline in Unix seconds, or nil
// when the page has none.
func pageRoundDeadline(page wire.Value) *int64 {
	value, found := page.Get("round_deadline")
	if !found {
		return nil
	}
	seconds, isInteger := value.IntOf()
	if !isInteger {
		return nil
	}
	return &seconds
}
