package service

import (
	"context"
	"log/slog"
	"maps"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type Metric struct {
	Requests int64     `json:"requests" bson:"requests"`
	Errors   int64     `json:"errors" bson:"errors"`
	TotalMS  int64     `json:"total_ms" bson:"total_ms"`
	MaxMS    int64     `json:"max_ms" bson:"max_ms"`
	Latency  [17]int64 `json:"latency_buckets" bson:"latency_buckets"`
}

// Disjoint histogram buckets, in milliseconds; the final bucket is overflow.
// A percentile is an upper bound, not an exact request latency.
var latencyBounds = [...]int64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 30000, 60000, 120000}

type Bucket struct {
	Minute      int64             `json:"minute_unix" bson:"minute_unix"`
	ByOperation map[string]Metric `json:"by_operation" bson:"by_operation"`
	ByRepo      map[string]Metric `json:"by_repo" bson:"by_repo"`
}
type observation struct {
	repo, op string
	finished time.Time
	elapsed  time.Duration
	failed   bool
}

type Metrics struct {
	queue            chan observation // allocated before accepting requests; never closed
	dropped          atomic.Uint64
	lastPersisted    time.Time
	mu               sync.Mutex
	lastPrunedMinute int64
	buckets          map[int64]*Bucket
	resources        ResourceSample
	// Snapshot construction is serialized separately from query observation.
	// Every response copies the immutable cache after releasing both locks.
	snapshotMu sync.Mutex
	cached     *statisticsSnapshot
}

type statisticsSnapshot struct {
	sampledAt, expiresAt time.Time
	buckets              []Bucket
	resources            ResourceSample
	storage              map[string]uint64
	persisted            time.Time
}

func copyBuckets(in []Bucket) []Bucket {
	out := make([]Bucket, len(in))
	for i, b := range in {
		out[i] = Bucket{Minute: b.Minute, ByOperation: maps.Clone(b.ByOperation), ByRepo: maps.Clone(b.ByRepo)}
	}
	return out
}

func (m *Metrics) Observe(repo, op string, start time.Time, failed bool) {
	finished := time.Now()
	// Invalid requests may carry arbitrarily long repository names. Do not retain
	// their request buffers in telemetry. Valid repository names fit this bound.
	if len(repo) > 512 {
		repo = "_overflow"
	}
	repo = strings.Clone(repo)
	select {
	case m.queue <- observation{repo, op, finished, finished.Sub(start), failed}:
	default:
		m.dropped.Add(1)
	}
}

// Only the background collector updates buckets. Slow snapshots or persistence
// can never make a request wait for this mutex.
func (m *Metrics) collect(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case event := <-m.queue:
			m.aggregate(event)
		}
	}
}

func (m *Metrics) aggregate(event observation) {
	repo, op, failed := event.repo, event.op, event.failed
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.buckets == nil {
		m.buckets = map[int64]*Bucket{}
	}
	minute := event.finished.Unix() / 60 * 60
	if minute > m.lastPrunedMinute {
		for k := range m.buckets {
			if k < minute-86340 {
				delete(m.buckets, k)
			}
		}
		m.lastPrunedMinute = minute
	}
	b := m.buckets[minute]
	if b == nil {
		b = &Bucket{Minute: minute, ByOperation: map[string]Metric{}, ByRepo: map[string]Metric{}}
		m.buckets[minute] = b
	}
	elapsed := event.elapsed
	ms := elapsed.Milliseconds()
	if ms < 0 {
		ms = 0
	}
	bin := sort.Search(len(latencyBounds), func(i int) bool { return elapsed <= time.Duration(latencyBounds[i])*time.Millisecond })
	add := func(v Metric) Metric {
		v.Requests++
		v.Latency[bin]++
		v.TotalMS += ms
		if ms > v.MaxMS {
			v.MaxMS = ms
		}
		if failed {
			v.Errors++
		}
		return v
	}
	b.ByOperation[op] = add(b.ByOperation[op])
	if _, ok := b.ByRepo[repo]; ok || len(b.ByRepo) < 256 {
		b.ByRepo[repo] = add(b.ByRepo[repo])
	} else {
		b.ByRepo["_overflow"] = add(b.ByRepo["_overflow"])
	}
}
func (s *Service) statisticsSnapshot(now time.Time) *statisticsSnapshot {
	s.metrics.snapshotMu.Lock()
	defer s.metrics.snapshotMu.Unlock()
	if cached := s.metrics.cached; cached != nil && now.Before(cached.expiresAt) {
		return cached
	}
	s.metrics.mu.Lock()
	buckets := []Bucket{}
	for k, v := range s.metrics.buckets {
		if k < now.Unix()/60*60-86340 {
			delete(s.metrics.buckets, k)
			continue
		}
		copy := Bucket{Minute: v.Minute, ByOperation: maps.Clone(v.ByOperation), ByRepo: maps.Clone(v.ByRepo)}
		buckets = append(buckets, copy)
	}
	resources, persisted := copyResources(s.metrics.resources), s.metrics.lastPersisted
	s.metrics.mu.Unlock()
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Minute < buckets[j].Minute })
	var fs syscall.Statfs_t
	e := syscall.Statfs(s.Root, &fs)
	var storage map[string]uint64
	if e == nil {
		storage = map[string]uint64{"total_bytes": uint64(fs.Blocks) * uint64(fs.Bsize), "available_bytes": uint64(fs.Bavail) * uint64(fs.Bsize)}
	}
	s.metrics.cached = &statisticsSnapshot{sampledAt: now.UTC(), expiresAt: now.Add(time.Second), buckets: buckets, storage: storage, resources: resources, persisted: persisted}
	return s.metrics.cached
}

func (s *Service) Statistics() any {
	v := s.statisticsSnapshot(time.Now())
	// The returned maps, slices and optional resource values belong to this
	// caller. Their allocation/copying never holds the Observe mutex.
	return map[string]any{"observations_dropped": s.metrics.dropped.Load(), "observations_pending": len(s.metrics.queue), "instance_id": s.boot, "sampled_at": v.sampledAt, "buckets": copyBuckets(v.buckets), "storage": maps.Clone(v.storage), "resources": copyResources(v.resources), "latency_bounds_ms": latencyBounds, "latency_bucket_mode": "disjoint-upper-bounds-with-overflow", "last_persisted_at": v.persisted, "scope": "current process search/read/read_many; 24-hour rolling minute buckets; P95 is a histogram upper bound; snapshot cached up to one second"}
}

// PublishObservations is independent of query execution. Missing samples stay
// missing; failed telemetry never changes readiness or delays a response.
func (s *Service) PublishObservations(ctx context.Context) {
	logsDone := make(chan struct{})
	go func() { defer close(logsDone); s.QueryLogs.Run(ctx, slog.Default()) }()
	defer func() { <-logsDone }()
	collectorDone := make(chan struct{})
	go func() { defer close(collectorDone); s.metrics.collect(ctx) }()
	defer func() { <-collectorDone }()
	resourcesDone := make(chan struct{})
	go func() { defer close(resourcesDone); s.sampleResources(ctx) }()
	defer func() { <-resourcesDone }()
	db, ok := s.DB.(interface {
		PrepareTelemetry(context.Context) error
		WriteObservation(context.Context, string, any, time.Time) error
	})
	if !ok {
		<-ctx.Done()
		return
	}
	prepared := false
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			budget, stop := context.WithTimeout(ctx, 3*time.Second)
			if !prepared {
				prepared = db.PrepareTelemetry(budget) == nil
			}
			if prepared {
				snapshot := observationPayload(s.Statistics().(map[string]any))
				if db.WriteObservation(budget, s.boot, snapshot, time.Now().UTC()) == nil {
					s.metrics.mu.Lock()
					s.metrics.lastPersisted = time.Now().UTC()
					s.metrics.mu.Unlock()
				}
			}
			stop()
		}
	}
}

// Keep persisted heartbeats bounded: no per-minute/per-repository histograms.
// The operations page reads current-process detail separately.
func observationPayload(snapshot map[string]any) map[string]any {
	cutoff := time.Now().Unix()/60*60 - 3540
	repos := map[string]int64{}
	var requests, failed int64
	for _, b := range snapshot["buckets"].([]Bucket) {
		if b.Minute < cutoff {
			continue
		}
		for _, m := range b.ByOperation {
			requests += m.Requests
			failed += m.Errors
		}
		for repo, m := range b.ByRepo {
			if _, ok := repos[repo]; !ok && len(repos) >= 256 {
				repo = "_overflow"
			}
			repos[repo] += m.Requests
		}
	}
	// Repository names may contain dots, which must not become Mongo field keys.
	type traffic struct {
		Repo     string `json:"repo" bson:"repo"`
		Requests int64  `json:"requests" bson:"requests"`
	}
	items := []traffic{}
	for repo, n := range repos {
		items = append(items, traffic{repo, n})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Repo < items[j].Repo })
	return map[string]any{"observations_dropped": snapshot["observations_dropped"], "schema": 2, "instance_id": snapshot["instance_id"], "sampled_at": snapshot["sampled_at"], "resources": snapshot["resources"], "storage": snapshot["storage"], "requests_60m": requests, "errors_60m": failed, "repo_requests_60m": items}
}
