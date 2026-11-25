package qkernel

import (
	"context"
	"sync"
)

// Work is a unit made of jobs launched on demand, each in a goroutine of
// its own: the report somebody asked for, the payment a request started.
// Nothing runs for the life of the piece, so there is no loop to tell to
// stop; what there is, is work that began and must be allowed to end.
//
// Stopping it means: no new jobs, and wait for the ones running. A job is
// never cut from here. Whoever launches work asks Accepting first and
// refuses the request when the answer is no; that refusal, and what it
// tells the user, belongs to the application.
//
// Like every unit it is born stopped: it accepts jobs once started.
type Work struct {
	mu      sync.Mutex
	started bool
	running int
	idle    chan struct{} // closed while nothing runs
}

// NewWork returns the unit.
func NewWork() *Work {
	w := &Work{idle: make(chan struct{})}
	close(w.idle)
	return w
}

// Accepting says whether new jobs are welcome: false while the unit is
// stopped or stopping.
func (w *Work) Accepting() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.started
}

// Go runs fn as a job of the unit. It does not refuse: a job that is
// launched anyway while the unit is stopping is still waited for, which
// is better than a job nobody knows of. Ask Accepting before.
func (w *Work) Go(fn func()) {
	w.mu.Lock()
	if w.running == 0 {
		w.idle = make(chan struct{})
	}
	w.running++
	w.mu.Unlock()
	go func() {
		defer func() {
			w.mu.Lock()
			w.running--
			if w.running == 0 {
				close(w.idle)
			}
			w.mu.Unlock()
		}()
		fn()
	}()
}

// Running says how many jobs are running now.
func (w *Work) Running() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running
}

// Start makes the unit accept jobs.
func (w *Work) Start(context.Context) error {
	w.mu.Lock()
	w.started = true
	w.mu.Unlock()
	return nil
}

// Stop refuses new jobs and waits for the running ones. When ctx expires
// first it returns its error and the jobs go on: the unit stays closed to
// new work, and is not stopped.
func (w *Work) Stop(ctx context.Context) error {
	for {
		w.mu.Lock()
		w.started = false
		idle, n := w.idle, w.running
		w.mu.Unlock()
		if n == 0 {
			return nil
		}
		select {
		case <-idle:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

var _ Unit = (*Work)(nil)
