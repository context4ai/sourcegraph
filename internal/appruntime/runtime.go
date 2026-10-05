// Package appruntime owns the background components shared by production and local development.
package appruntime

import (
	"context"
	"log/slog"
	"sync"

	"github.com/context4ai/sourcegraph/internal/asynclog"
)

type Backend interface {
	Run(context.Context)
	PublishObservations(context.Context)
}
type Settings interface {
	RunRefresh(context.Context, *slog.Logger)
}

type Options struct {
	// Only a static UI fixture may disable Git/index work. Observations and settings still run.
	DisableRepositoryWorkers bool
}
type Runtime struct {
	Logger *slog.Logger
	cancel context.CancelFunc
	done   chan struct{}
}

// Start always starts telemetry (including resource sampling), settings refresh
// and asynchronous access logging. Backend.Run owns automatic/manual jobs,
// scanning, collection and disk accounting. Stop must precede closing DB/engine.
func Start(parent context.Context, backend Backend, settings Settings, logger *slog.Logger, options Options) *Runtime {
	ctx, cancel := context.WithCancel(parent)
	access, _ := asynclog.New(ctx, logger, 4096)
	r := &Runtime{Logger: access, cancel: cancel, done: make(chan struct{})}
	var workers sync.WaitGroup
	start := func(run func()) { workers.Add(1); go func() { defer workers.Done(); run() }() }
	start(func() { backend.PublishObservations(ctx) })
	start(func() { settings.RunRefresh(ctx, logger) })
	if !options.DisableRepositoryWorkers {
		start(func() { backend.Run(ctx) })
	}
	go func() { workers.Wait(); close(r.done) }()
	return r
}
func (r *Runtime) Stop() { r.cancel(); <-r.done }
