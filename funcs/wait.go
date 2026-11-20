package funcs

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
)

// Poll calls cond every interval until it returns done=true, the timeout elapses, or ctx
// is cancelled. A non-nil error from cond ends the poll immediately (use it to abort on a
// condition you know will never recover). On timeout it returns an error naming desc.
//
// Poll replaces hand-rolled `for { ...; if time.Now().After(deadline) {...}; time.Sleep }`
// loops with one shared, context-aware implementation. A non-positive interval uses
// DefaultPollInterval. The first check runs immediately (no leading sleep).
func Poll(ctx context.Context, timeout, interval time.Duration, desc string, cond func(context.Context) (bool, error)) error {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	if err := wait.PollUntilContextTimeout(ctx, interval, timeout, true, cond); err != nil {
		return fmt.Errorf("waiting for %s: %w", desc, err)
	}
	return nil
}

// Retry calls op every interval until it returns nil, the timeout elapses, or ctx is
// cancelled, then returns op's last error wrapped with desc. Every error is treated as
// "not yet, try again" — use Retry for a wait whose not-ready reason you want surfaced on
// timeout (e.g. the observed-vs-wanted state), and Poll when some errors should abort.
//
// A non-positive interval uses DefaultPollInterval. The first attempt runs immediately.
func Retry(ctx context.Context, timeout, interval time.Duration, desc string, op func(context.Context) error) error {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	var last error
	err := wait.PollUntilContextTimeout(ctx, interval, timeout, true, func(ctx context.Context) (bool, error) {
		last = op(ctx)
		return last == nil, nil
	})
	if err != nil {
		if last != nil {
			return fmt.Errorf("%s: %w", desc, last)
		}
		return fmt.Errorf("%s: %w", desc, err)
	}
	return nil
}
