package indexer

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

func TestIndexSupervisorStopsAfterInput(t *testing.T) {
	for _, diskFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "disk"}[diskFailure], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var ended atomic.Bool
			diskErr := errors.New("disk reserve exhausted")
			cmd := exec.Command("sh", "-c", "cat >/dev/null; sleep 30")
			started := time.Now()
			err := superviseIndex(ctx, cmd, func() error {
				if ended.Load() {
					if diskFailure {
						return diskErr
					}
					cancel()
				}
				return nil
			}, func(context.Context, io.Writer) error { ended.Store(true); return nil })
			if diskFailure && !errors.Is(err, diskErr) {
				t.Fatalf("disk error: %v", err)
			}
			if !diskFailure && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel error: %v", err)
			}
			if time.Since(started) > 3*time.Second || cmd.ProcessState == nil || cmd.ProcessState.Success() {
				t.Fatalf("worker not stopped and reaped: %v", cmd.ProcessState)
			}
		})
	}
}
func TestIndexSupervisorRejectsBeforeStart(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	cause := errors.New("low disk")
	err := superviseIndex(context.Background(), cmd, func() error { return cause }, func(context.Context, io.Writer) error { t.Fatal("fed on low disk"); return nil })
	if !errors.Is(err, cause) || cmd.Process != nil {
		t.Fatal("started with low disk")
	}
}
