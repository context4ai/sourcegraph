package service

import (
	"context"
	"errors"
	"github.com/context4ai/sourcegraph/internal/contract"
	"testing"
	"time"
)

func TestCompletionRetryTransient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	calls := 0
	err := retryJobCompletion(ctx, func(context.Context) error {
		calls++
		if calls == 1 {
			return contract.Fail("CONTROL_STATE_UNAVAILABLE", "temporary", 503)
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}
func TestCompletionDoesNotRetryLostOwnership(t *testing.T) {
	calls := 0
	e := retryJobCompletion(context.Background(), func(context.Context) error { calls++; return contract.Fail("STALE_WORKER", "lost", 409) })
	if e == nil || calls != 1 {
		t.Fatalf("calls=%d error=%v", calls, e)
	}
}
func TestCompletionRetryBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if e := retryJobCompletion(ctx, func(context.Context) error { return errors.New("database unavailable") }); e == nil {
		t.Fatal("expected failure")
	}
}
