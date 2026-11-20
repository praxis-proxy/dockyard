package funcs

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

// recordFunc returns an env.Func that appends tag to order when invoked. The returned
// func ignores its arguments, so it is safe to call with a nil *envconf.Config.
func recordFunc(order *[]string, mu *sync.Mutex, tag string) func(context.Context, *envconf.Config) (context.Context, error) {
	return func(ctx context.Context, _ *envconf.Config) (context.Context, error) {
		mu.Lock()
		*order = append(*order, tag)
		mu.Unlock()
		return ctx, nil
	}
}

func TestDepsBuildPreservesCallOrder(t *testing.T) {
	var order []string
	var mu sync.Mutex
	fns := NewDeps().
		With(recordFunc(&order, &mu, "a")).
		With(recordFunc(&order, &mu, "b")).
		With(recordFunc(&order, &mu, "c")).
		Build()

	if len(fns) != 3 {
		t.Fatalf("Build() returned %d funcs, want 3", len(fns))
	}
	for _, fn := range fns {
		if _, err := fn(context.Background(), nil); err != nil {
			t.Fatalf("step: %v", err)
		}
	}
	if got := order; len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("call order = %v, want [a b c]", got)
	}
}

func TestDepsWithParallelIsASingleStep(t *testing.T) {
	var order []string
	var mu sync.Mutex
	fns := NewDeps().
		With(recordFunc(&order, &mu, "pre")).
		WithParallel(recordFunc(&order, &mu, "p1"), recordFunc(&order, &mu, "p2")).
		With(recordFunc(&order, &mu, "post")).
		Build()

	// pre + one parallel step + post = 3 steps, even though the parallel step wraps two.
	if len(fns) != 3 {
		t.Fatalf("Build() returned %d steps, want 3 (parallel collapses to one)", len(fns))
	}
	for _, fn := range fns {
		if _, err := fn(context.Background(), nil); err != nil {
			t.Fatalf("step: %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 4 {
		t.Fatalf("ran %d funcs, want 4", len(order))
	}
	if order[0] != "pre" || order[3] != "post" {
		t.Errorf("ordered steps not respected around the parallel group: %v", order)
	}
}

func TestRecorderImage(t *testing.T) {
	if got := RecorderImage("v1"); got != DefaultRecorderRegistry+":v1" {
		t.Errorf("RecorderImage(v1) = %q", got)
	}
	if got := RecorderImage(""); got != DefaultRecorderRegistry+":"+DefaultRecorderTag {
		t.Errorf("RecorderImage(\"\") = %q, want default tag", got)
	}
}

func TestParallelRunsAllFuncs(t *testing.T) {
	var ran int32
	fn := func(context.Context, *envconf.Config) (context.Context, error) {
		atomic.AddInt32(&ran, 1)
		return nil, nil
	}
	if _, err := Parallel(fn, fn, fn)(context.Background(), nil); err != nil {
		t.Fatalf("Parallel: %v", err)
	}
	if ran != 3 {
		t.Errorf("ran %d funcs, want 3", ran)
	}
}

func TestParallelPropagatesFirstError(t *testing.T) {
	boom := errors.New("boom")
	ok := func(context.Context, *envconf.Config) (context.Context, error) { return nil, nil }
	bad := func(context.Context, *envconf.Config) (context.Context, error) { return nil, boom }
	if _, err := Parallel(ok, bad, ok)(context.Background(), nil); !errors.Is(err, boom) {
		t.Errorf("Parallel error = %v, want boom", err)
	}
}

func TestParallelSingleFuncThreadsContext(t *testing.T) {
	type key struct{}
	fn := func(ctx context.Context, _ *envconf.Config) (context.Context, error) {
		return context.WithValue(ctx, key{}, "threaded"), nil
	}
	// The single-func fast path must return the func's (possibly mutated) context.
	out, err := Parallel(fn)(context.Background(), nil)
	if err != nil {
		t.Fatalf("Parallel: %v", err)
	}
	if out.Value(key{}) != "threaded" {
		t.Error("single-func fast path did not thread the returned context")
	}
}
