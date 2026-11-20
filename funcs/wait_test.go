package funcs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRetrySucceedsAfterTransientFailures(t *testing.T) {
	calls := 0
	err := Retry(context.Background(), time.Second, time.Millisecond, "op", func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("not yet")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Retry = %v, want nil", err)
	}
	if calls != 3 {
		t.Errorf("op called %d times, want 3", calls)
	}
}

func TestRetryReturnsLastErrorOnTimeout(t *testing.T) {
	err := Retry(context.Background(), 30*time.Millisecond, time.Millisecond, "readiness", func(context.Context) error {
		return errors.New("phase=Pending want Ready")
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "readiness") || !strings.Contains(err.Error(), "phase=Pending") {
		t.Errorf("error should wrap desc + last failure: %v", err)
	}
}

func TestPollStopsWhenDone(t *testing.T) {
	n := 0
	err := Poll(context.Background(), time.Second, time.Millisecond, "ready", func(context.Context) (bool, error) {
		n++
		return n >= 2, nil
	})
	if err != nil {
		t.Fatalf("Poll = %v, want nil", err)
	}
	if n != 2 {
		t.Errorf("cond called %d times, want 2", n)
	}
}

func TestPollAbortsOnConditionError(t *testing.T) {
	boom := errors.New("boom")
	calls := 0
	err := Poll(context.Background(), time.Second, time.Millisecond, "thing", func(context.Context) (bool, error) {
		calls++
		return false, boom
	})
	if !errors.Is(err, boom) {
		t.Errorf("Poll error = %v, want boom", err)
	}
	if calls != 1 {
		t.Errorf("cond called %d times, want 1 (abort immediately)", calls)
	}
}

func TestPollHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Poll(ctx, time.Minute, time.Millisecond, "x", func(context.Context) (bool, error) {
		return false, nil
	})
	if err == nil {
		t.Error("expected error from a cancelled context")
	}
}

func TestDefaultIntervalUsedForNonPositive(t *testing.T) {
	// A zero interval must not spin or panic; it should fall back to the default and
	// still succeed on the immediate first check.
	err := Poll(context.Background(), time.Second, 0, "immediate", func(context.Context) (bool, error) {
		return true, nil
	})
	if err != nil {
		t.Fatalf("Poll with zero interval = %v, want nil", err)
	}
}
