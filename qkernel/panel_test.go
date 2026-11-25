package qkernel

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type recorder struct {
	mu  sync.Mutex
	log []string
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	r.log = append(r.log, s)
	r.mu.Unlock()
}

func (r *recorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.log, " ")
}

func (r *recorder) unit(name string) Unit {
	return Func(
		func(context.Context) error { r.add("start:" + name); return nil },
		func(context.Context) error { r.add("stop:" + name); return nil },
	)
}

func TestStartOrderAndReverseStop(t *testing.T) {
	var r recorder
	p := New()
	for _, n := range []string{"a", "b", "c"} {
		if err := p.Add(n, r.unit(n)); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if err := p.StartAll(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.StopAll(ctx); err != nil {
		t.Fatal(err)
	}
	want := "start:a start:b start:c stop:c stop:b stop:a"
	if got := r.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDuplicateAndUnknown(t *testing.T) {
	p := New()
	if err := p.Add("a", Func(nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := p.Add("a", Func(nil, nil)); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := p.Start(context.Background(), "nope"); !errors.Is(err, ErrUnknownUnit) {
		t.Fatalf("got %v", err)
	}
}

func TestStartFailureStopsTheSequence(t *testing.T) {
	var r recorder
	boom := errors.New("boom")
	p := New()
	p.Add("a", r.unit("a"))
	p.Add("b", Func(func(context.Context) error { return boom }, func(context.Context) error {
		r.add("stop:b")
		return nil
	}))
	p.Add("c", r.unit("c"))
	ctx := context.Background()
	if err := p.StartAll(ctx); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	us := p.Units()
	if us[0].State != Running || us[1].State != Failed || us[2].State != Stopped {
		t.Fatalf("states: %+v", us)
	}
	if us[1].Err != "boom" {
		t.Fatalf("err text: %q", us[1].Err)
	}
	if err := p.StopAll(ctx); err != nil {
		t.Fatal(err)
	}
	// b never started, so it is never asked to stop.
	if got, want := r.String(), "start:a stop:a"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestStopErrorDoesNotBlockTheRest(t *testing.T) {
	var r recorder
	stuck := errors.New("stuck")
	p := New()
	p.Add("a", r.unit("a"))
	p.Add("b", Func(nil, func(context.Context) error { return stuck }))
	ctx := context.Background()
	if err := p.StartAll(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.StopAll(ctx); !errors.Is(err, stuck) {
		t.Fatalf("got %v", err)
	}
	us := p.Units()
	if us[0].State != Stopped || us[1].State != Failed {
		t.Fatalf("states: %+v", us)
	}
}

func TestWatchSeesTransitions(t *testing.T) {
	p := New()
	p.Add("a", Func(nil, nil))
	var mu sync.Mutex
	var seen []State
	cancel := p.Watch(func(i UnitInfo) {
		mu.Lock()
		seen = append(seen, i.State)
		mu.Unlock()
	})
	ctx := context.Background()
	p.Start(ctx, "a")
	p.Stop(ctx, "a")
	cancel()
	p.Start(ctx, "a")
	mu.Lock()
	defer mu.Unlock()
	want := []State{Starting, Running, Stopping, Stopped}
	if len(seen) != len(want) {
		t.Fatalf("seen %v", seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("seen %v", seen)
		}
	}
}

func TestGoUnitFinishesWorkInHand(t *testing.T) {
	finished := false
	u := Go(func(ctx context.Context) error {
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond) // the work in hand
		finished = true
		return ctx.Err()
	})
	p := New()
	p.Add("worker", u)
	ctx := context.Background()
	if err := p.Start(ctx, "worker"); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(ctx, "worker"); err != nil {
		t.Fatal("second start should be a no-op:", err)
	}
	if err := p.Stop(ctx, "worker"); err != nil {
		t.Fatal(err)
	}
	if !finished {
		t.Fatal("stop returned before the work in hand was finished")
	}
	if s := p.Units()[0].State; s != Stopped {
		t.Fatalf("state %v", s)
	}
}

func TestStopTimeoutLeavesUnitNotStopped(t *testing.T) {
	release := make(chan struct{})
	u := Go(func(ctx context.Context) error {
		<-ctx.Done()
		<-release
		return nil
	})
	p := New()
	p.Add("slow", u)
	p.Start(context.Background(), "slow")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := p.Stop(ctx, "slow"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if s := p.Units()[0].State; s != Failed {
		t.Fatalf("state %v", s)
	}
	close(release)
	if err := p.Stop(context.Background(), "slow"); err != nil {
		t.Fatal(err)
	}
	if s := p.Units()[0].State; s != Stopped {
		t.Fatalf("state %v", s)
	}
}

func TestUnitEndingByItselfIsNoticed(t *testing.T) {
	boom := errors.New("boom")
	p := New()
	p.Add("clean", Go(func(context.Context) error { return nil }))
	p.Add("crash", Go(func(context.Context) error { return boom }))
	if err := p.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		us := p.Units()
		if us[0].State == Stopped && us[1].State == Failed && us[1].Err == "boom" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("states: %+v", us)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Both can be started again.
	if err := p.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type closingSource struct {
	Source
	closed *bool
}

func (c closingSource) Close() error { *c.closed = true; return nil }

func TestRunStopsOnContextAndClosesSources(t *testing.T) {
	var r recorder
	closed := false
	p := New()
	p.Add("a", r.unit("a"))
	p.AddSource(closingSource{closed: &closed})
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- p.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for p.Units()[0].State != Running {
		if time.Now().After(deadline) {
			t.Fatal("never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if got, want := r.String(), "start:a stop:a"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if !closed {
		t.Fatal("source not closed")
	}
}
