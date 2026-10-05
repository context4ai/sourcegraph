// Package asynclog provides bounded, best-effort access logging. It is not an
// audit log: overload and process shutdown may discard records.
package asynclog

import (
	"context"
	"log/slog"
	"sync/atomic"
)

type entry struct {
	handler slog.Handler
	record  slog.Record
}
type queue struct {
	events  chan entry
	dropped atomic.Uint64
	stopped atomic.Bool
}
type handler struct {
	target slog.Handler
	queue  *queue
}

// New starts one writer. Cancellation does not wait for a blocked output sink.
// The queue never closes, so requests finishing during shutdown cannot panic.
func New(ctx context.Context, target *slog.Logger, capacity int) (*slog.Logger, func() uint64) {
	if capacity < 1 {
		capacity = 1
	}
	q := &queue{events: make(chan entry, capacity)}
	go func() {
		defer q.stopped.Store(true)
		for {
			if ctx.Err() != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case e := <-q.events:
				if e.handler.Enabled(ctx, e.record.Level) {
					e.record.AddAttrs(slog.Uint64("access_logs_dropped_total", q.dropped.Load()))
					_ = e.handler.Handle(ctx, e.record)
				}
			}
		}
	}()
	return slog.New(&handler{target.Handler(), q}), q.dropped.Load
}

// No sink calls (including Enabled) run in the request goroutine.
func (h *handler) Enabled(context.Context, slog.Level) bool { return true }
func (h *handler) Handle(_ context.Context, r slog.Record) error {
	if h.queue.stopped.Load() {
		h.queue.dropped.Add(1)
		return nil
	}
	select {
	case h.queue.events <- entry{h.target, r.Clone()}:
	default:
		h.queue.dropped.Add(1)
	}
	return nil
}
func (h *handler) WithAttrs(a []slog.Attr) slog.Handler {
	return &handler{h.target.WithAttrs(a), h.queue}
}
func (h *handler) WithGroup(g string) slog.Handler { return &handler{h.target.WithGroup(g), h.queue} }
