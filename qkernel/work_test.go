package qkernel

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkWaitsForWhatIsRunning(t *testing.T) {
	w := NewWork()
	if w.Accepting() {
		t.Fatal("accepting before being started")
	}
	if err := w.Stop(context.Background()); err != nil {
		t.Fatal("stopping what never started:", err)
	}
	w.Start(context.Background())
	if !w.Accepting() {
		t.Fatal("not accepting after start")
	}
	release := make(chan struct{})
	var finished atomic.Int32
	for i := 0; i < 3; i++ {
		w.Go(func() { <-release; time.Sleep(20 * time.Millisecond); finished.Add(1) })
	}
	// A stop that is not given the time cuts nothing, and says so.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	err := w.Stop(ctx)
	cancel()
	if err == nil || w.Running() != 3 || finished.Load() != 0 {
		t.Fatalf("an impatient stop: err %v, running %d, finished %d", err, w.Running(), finished.Load())
	}
	if w.Accepting() {
		t.Fatal("accepting while stopping")
	}
	// A patient one returns only when every job has finished.
	stopped := make(chan error, 1)
	go func() { stopped <- w.Stop(context.Background()) }()
	select {
	case <-stopped:
		t.Fatal("stopped with jobs running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-stopped; err != nil || finished.Load() != 3 || w.Running() != 0 {
		t.Fatalf("stop: %v, finished %d, running %d", err, finished.Load(), w.Running())
	}
	// And it goes again.
	w.Start(context.Background())
	done := make(chan struct{})
	w.Go(func() { close(done) })
	<-done
	if err := w.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Jobs that launch jobs, stops that overlap: nothing is lost and nothing
// is waited for forever.
func TestWorkUnderLoad(t *testing.T) {
	w := NewWork()
	w.Start(context.Background())
	var ran atomic.Int32
	var launched sync.WaitGroup
	for i := 0; i < 200; i++ {
		launched.Add(1)
		w.Go(func() {
			defer launched.Done()
			ran.Add(1)
			launched.Add(1)
			w.Go(func() { defer launched.Done(); ran.Add(1) })
		})
	}
	var stops sync.WaitGroup
	for i := 0; i < 4; i++ {
		stops.Add(1)
		go func() {
			defer stops.Done()
			if err := w.Stop(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	stops.Wait()
	launched.Wait()
	// A job launched by the last job may start as a stop returns; one
	// more stop waits for it.
	if err := w.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ran.Load() != 400 || w.Running() != 0 {
		t.Fatalf("%d ran, %d running", ran.Load(), w.Running())
	}
}

func TestWorkInAPanel(t *testing.T) {
	p := New()
	w := NewWork()
	if err := p.Add("jobs", w); err != nil {
		t.Fatal(err)
	}
	if err := p.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !w.Accepting() {
		t.Fatal("the panel started it and it does not accept")
	}
	release := make(chan struct{})
	w.Go(func() { <-release })
	go func() { time.Sleep(30 * time.Millisecond); close(release) }()
	if err := p.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if w.Running() != 0 || w.Accepting() {
		t.Fatal("the shutdown did not wait for the job")
	}
}
