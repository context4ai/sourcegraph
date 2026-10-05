package service

import (
	"testing"
	"time"
)

func TestStatisticsRetains24Hours(t *testing.T) {
	now := time.Now().Truncate(time.Minute)
	s := &Service{Root: t.TempDir()}
	for _, age := range []time.Duration{25 * time.Hour, 23 * time.Hour, 2 * time.Hour, 0} {
		s.metrics.aggregate(observation{repo: "test/repo", op: "search", finished: now.Add(-age), elapsed: time.Millisecond})
	}
	v := s.statisticsSnapshot(now)
	if len(v.buckets) != 3 {
		t.Fatalf("buckets=%d want 3", len(v.buckets))
	}
	for _, b := range v.buckets {
		if b.Minute < now.Add(-24*time.Hour).Unix() {
			t.Fatal("stale bucket retained")
		}
	}
	payload := observationPayload(s.Statistics().(map[string]any))
	if payload["requests_60m"] != int64(1) {
		t.Fatalf("heartbeat 60m scope changed: %v", payload["requests_60m"])
	}
}
