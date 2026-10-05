package wasmplugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

type idleInstance struct {
	module api.Module
	used   time.Time
}
type cached struct {
	module wazero.CompiledModule
	refs   int
	used   time.Time
	idle   map[string]idleInstance
}
type Host struct {
	Config      Config
	runtime     wazero.Runtime
	gate        chan struct{}
	compileGate chan struct{}
	mu          sync.Mutex
	entries     map[string]*cached
	idleCount   int
	closed      bool
	compiling   sync.WaitGroup
}

func New(c Config) *Host {
	r := wazero.NewRuntimeWithConfig(context.Background(), wazero.NewRuntimeConfigCompiler().WithMemoryLimitPages(uint32(c.MemoryMiB*16)).WithCloseOnContextDone(true))
	h := &Host{Config: c, runtime: r, gate: make(chan struct{}, c.Concurrency), compileGate: make(chan struct{}, 1), entries: map[string]*cached{}}
	_, err := r.NewHostModuleBuilder("sourcegraph").NewFunctionBuilder().WithFunc(hostReadFile).Export("read_file").NewFunctionBuilder().WithFunc(hostReadFiles).Export("read_files").NewFunctionBuilder().WithFunc(hostLastError).Export("last_error").Instantiate(context.Background())
	if err != nil {
		panic(err)
	} // Static host signatures, independent of repository artifacts.
	return h
}
func (h *Host) Close() {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	h.compiling.Wait()
	_ = h.runtime.Close(context.Background())
}

// The compiler does not guarantee prompt interruption in every phase. One
// bounded worker retains the compile slot until it finishes; callers can still
// return on deadline. Late artifacts are closed and never execute.
func (h *Host) compile(ctx context.Context, wasm []byte) (wazero.CompiledModule, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		<-h.compileGate
		return nil, errors.New("plugin host is stopped")
	}
	h.compiling.Add(1)
	h.mu.Unlock()
	type result struct {
		module wazero.CompiledModule
		err    error
	}
	done := make(chan result)
	go func() {
		defer h.compiling.Done()
		defer func() { <-h.compileGate }()
		module, err := h.runtime.CompileModule(ctx, wasm)
		select {
		case done <- result{module, err}:
		case <-ctx.Done():
			if module != nil {
				module.Close(context.Background())
			}
		}
	}()
	select {
	case r := <-done:
		return r.module, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (h *Host) acquire(ctx context.Context, wasm []byte) (*cached, bool, error) {
	sum := sha256.Sum256(wasm)
	key := hex.EncodeToString(sum[:])
	lookup := func() (*cached, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.closed {
			return nil, errors.New("plugin host is stopped")
		}
		if e := h.entries[key]; e != nil {
			e.refs++
			e.used = time.Now()
			return e, nil
		}
		return nil, nil
	}
	if e, err := lookup(); e != nil || err != nil {
		return e, true, err
	}
	select {
	case h.compileGate <- struct{}{}:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	if e, err := lookup(); e != nil || err != nil {
		<-h.compileGate
		return e, true, err
	}
	compiled, err := h.compile(ctx, wasm)
	if err != nil {
		return nil, false, err
	}
	// Only the three repository read imports are available; there is no WASI.
	for _, f := range compiled.ImportedFunctions() {
		module, name, _ := f.Import()
		if module != "sourcegraph" || (name != "read_file" && name != "read_files" && name != "last_error") {
			compiled.Close(ctx)
			return nil, false, fmt.Errorf("unsupported host import %s.%s", module, name)
		}
	}
	if len(compiled.ImportedMemories()) != 0 {
		compiled.Close(ctx)
		return nil, false, errors.New("imported memory is unsupported")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || ctx.Err() != nil {
		compiled.Close(context.Background())
		return nil, false, errors.New("plugin compilation cancelled")
	}
	if existing := h.entries[key]; existing != nil {
		compiled.Close(ctx)
		existing.refs++
		existing.used = time.Now()
		return existing, true, nil
	}
	if len(h.entries) >= h.Config.CacheEntries {
		var oldest string
		var date time.Time
		for k, e := range h.entries {
			if e.refs == 0 && (oldest == "" || e.used.Before(date)) {
				oldest = k
				date = e.used
			}
		}
		if oldest == "" {
			compiled.Close(ctx)
			return nil, false, errors.New("plugin compilation cache is busy")
		}
		e := h.entries[oldest]
		for _, m := range e.idle {
			m.module.Close(ctx)
			h.idleCount--
		}
		e.module.Close(ctx)
		delete(h.entries, oldest)
	}
	e := &cached{module: compiled, refs: 1, used: time.Now(), idle: map[string]idleInstance{}}
	h.entries[key] = e
	return e, false, nil
}
func (h *Host) instance(ctx context.Context, e *cached, key string) (api.Module, bool, error) {
	h.mu.Lock()
	m := e.idle[key].module
	delete(e.idle, key)
	if m != nil {
		h.idleCount--
	}
	h.mu.Unlock()
	if m != nil && !m.IsClosed() {
		return m, true, nil
	}
	m, err := h.runtime.InstantiateModule(ctx, e.module, wazero.NewModuleConfig().WithName("").WithStartFunctions())
	return m, false, err
}
func (h *Host) release(e *cached, m api.Module, key string, keep bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e.refs--
	e.used = time.Now()
	if m == nil {
		return
	}
	if keep && !h.closed && !m.IsClosed() && e.idle[key].module == nil {
		if h.idleCount >= h.Config.IdleInstances {
			var victim *cached
			var oldestKey string
			var oldest time.Time
			for _, entry := range h.entries {
				for k, idle := range entry.idle {
					if victim == nil || idle.used.Before(oldest) {
						victim = entry
						oldestKey = k
						oldest = idle.used
					}
				}
			}
			if victim != nil {
				victim.idle[oldestKey].module.Close(context.Background())
				delete(victim.idle, oldestKey)
				h.idleCount--
			}
		}
		if h.idleCount < h.Config.IdleInstances {
			e.idle[key] = idleInstance{m, time.Now()}
			h.idleCount++
			return
		}
	}
	_ = m.Close(context.Background())
}

// Run returns arbitrary valid JSON. It never applies a business result schema.
// key binds persistent guest state to a repository, SHA, scope and arguments.
func (h *Host) Run(ctx context.Context, wasm []byte, key string, input any, read Reader) json.RawMessage {
	out, _ := h.RunResult(ctx, wasm, key, input, read)
	return out
}

// RunResult distinguishes host failures from untrusted guest JSON.
func (h *Host) RunResult(ctx context.Context, wasm []byte, key string, input any, read Reader) (json.RawMessage, *Issue) {
	var issue *Issue
	out := h.run(ctx, wasm, key, input, read, func(c, msg string) { issue = &Issue{Code: c, Message: msg} })
	return out, issue
}
func (h *Host) run(ctx context.Context, wasm []byte, key string, input any, read Reader, report func(string, string)) (out json.RawMessage) {
	started := time.Now()
	code := ""
	var compileHit, instanceHit bool
	var compileTime, instanceTime, executionTime time.Duration
	var state *invocation
	defer func() {
		slog.Debug("repository plugin", "elapsed_ms", time.Since(started).Milliseconds(), "compiled_cache_hit", compileHit, "instance_cache_hit", instanceHit, "code", code, "compile_ms", compileTime.Milliseconds(), "instantiate_ms", instanceTime.Milliseconds(), "execution_ms", executionTime.Milliseconds())
		if state != nil {
			slog.Debug("repository plugin reads", "calls", state.calls, "bytes", state.bytes, "elapsed_ms", state.readTime.Milliseconds())
		}
	}()
	fail := func(c, msg string) json.RawMessage { code = c; report(c, msg); return Failure(c, msg) }
	if h.Config.Disabled {
		return fail("PLUGINS_DISABLED", "Reading enhancements are disabled; original content is unchanged.")
	}
	if len(wasm) > h.Config.ArtifactBytes {
		return fail("PLUGIN_TOO_LARGE", "Plugin exceeds the configured artifact budget.")
	}
	ctx, cancel := context.WithTimeout(ctx, h.Config.Timeout)
	defer cancel()
	select {
	case h.gate <- struct{}{}:
		defer func() { <-h.gate }()
	default:
		return fail("PLUGIN_BUSY", "Plugin workers are busy; retry the enhancement later.")
	}
	if _, err := Inspect(wasm, h.Config.MetadataBytes); err != nil {
		return fail("PLUGIN_INVALID", err.Error())
	}
	state = &invocation{read: read, config: h.Config}
	ctx = context.WithValue(ctx, invocationKey{}, state)
	phase := time.Now()
	entry, hit, err := h.acquire(ctx, wasm)
	compileTime = time.Since(phase)
	compileHit = hit
	if err != nil {
		if ctx.Err() != nil {
			return fail("PLUGIN_TIMEOUT", "Enhancement exceeded its time budget.")
		}
		return fail("PLUGIN_INVALID", "Cannot compile this Wasm plugin.")
	}
	phase = time.Now()
	m, hit, err := h.instance(ctx, entry, key)
	instanceTime = time.Since(phase)
	instanceHit = hit
	keep := false
	defer func() { h.release(entry, m, key, keep) }()
	if err != nil {
		return fail("PLUGIN_FAILED", "Cannot instantiate this Wasm plugin.")
	}
	alloc, free, enrich := m.ExportedFunction("alloc"), m.ExportedFunction("dealloc"), m.ExportedFunction("enrich")
	if m.Memory() == nil || alloc == nil || free == nil || enrich == nil {
		return fail("PLUGIN_ABI_INCOMPATIBLE", "Expected memory, alloc, dealloc and enrich exports (JSON ABI 2).")
	}
	data, err := json.Marshal(input)
	if err != nil {
		return fail("PLUGIN_INPUT_INVALID", "Cannot encode enhancement input.")
	}
	pointer, err := writeGuest(ctx, m, data)
	if err != nil {
		return fail("PLUGIN_FAILED", "Cannot allocate plugin input.")
	}
	phase = time.Now()
	result, err := enrich.Call(ctx, uint64(pointer), uint64(len(data)))
	executionTime = time.Since(phase)
	// Input and output are independent plugin-owned allocations; never return a
	// poisoned instance to the pool after any call or deallocation failure.
	if err != nil || len(result) != 1 {
		if ctx.Err() != nil {
			return fail("PLUGIN_TIMEOUT", "Enhancement exceeded its time budget; original content is unchanged.")
		}
		return fail("PLUGIN_FAILED", "Plugin execution trapped or returned an invalid ABI value.")
	}
	ptr, size := uint32(result[0]>>32), uint32(result[0])
	if size > uint32(h.Config.OutputBytes) {
		return fail("PLUGIN_OUTPUT_TOO_LARGE", "Enhancement exceeds the configured output budget.")
	}
	b, ok := m.Memory().Read(ptr, size)
	if !ok || !json.Valid(b) {
		return fail("PLUGIN_INVALID_OUTPUT", "Plugin must return valid JSON in its exported memory.")
	}
	out = append(json.RawMessage(nil), b...)
	if _, err = free.Call(ctx, uint64(ptr), uint64(size)); err != nil {
		return fail("PLUGIN_FAILED", "Cannot release plugin output.")
	}
	if _, err = free.Call(ctx, uint64(pointer), uint64(len(data))); err != nil {
		return fail("PLUGIN_FAILED", "Cannot release plugin input.")
	}
	keep = ctx.Err() == nil
	return out
}

// Precompile warms immutable code only. It never instantiates or calls enrich.
func (h *Host) Precompile(ctx context.Context, wasm []byte) error {
	if _, err := Inspect(wasm, h.Config.MetadataBytes); err != nil {
		return err
	}
	entry, _, err := h.acquire(ctx, wasm)
	if err == nil {
		h.release(entry, nil, "", false)
	}
	return err
}
