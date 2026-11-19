package quevu

import (
	"context"
	"errors"
	"sync"
)

// Unit is one piece of an application that can be started and stopped: a
// web server, a mailer, a periodic task, a group of workers. Each one is
// of its own nature; this interface is the only thing they share.
//
// The application implements it, and with it decides what stopping means
// and when it is safe. The panel only asks.
type Unit interface {
	// Start launches the piece and returns once it is running. It must
	// not block for the life of the piece. ctx bounds the start itself.
	Start(ctx context.Context) error
	// Stop asks the piece to stop in an orderly way and returns when it
	// has: no new work accepted, work in hand finished. ctx is how long
	// the caller is willing to wait; when it expires Stop returns its
	// error and the piece is not considered stopped.
	Stop(ctx context.Context) error
}

// Exiter is implemented by units that may end on their own. Done returns a
// channel that yields the result of the current run when it ends; the
// panel uses it to notice a piece that stopped without being asked.
type Exiter interface {
	Done() <-chan error
}

// State is where a unit is in its lifecycle.
type State int

const (
	Stopped State = iota
	Starting
	Running
	Stopping
	// Failed means the last start or stop returned an error, or the unit
	// ended by itself with one.
	Failed
)

func (s State) String() string {
	switch s {
	case Stopped:
		return "stopped"
	case Starting:
		return "starting"
	case Running:
		return "running"
	case Stopping:
		return "stopping"
	case Failed:
		return "failed"
	}
	return "unknown"
}

// UnitInfo is the visible state of a unit.
type UnitInfo struct {
	Name  string
	State State
	// Err is the error that led to Failed, as text is all a remote
	// controller can carry.
	Err string
}

// Func builds a Unit from two functions. Either may be nil.
func Func(start, stop func(ctx context.Context) error) Unit {
	return funcUnit{start: start, stop: stop}
}

type funcUnit struct {
	start, stop func(ctx context.Context) error
}

func (u funcUnit) Start(ctx context.Context) error {
	if u.start == nil {
		return nil
	}
	return u.start(ctx)
}

func (u funcUnit) Stop(ctx context.Context) error {
	if u.stop == nil {
		return nil
	}
	return u.stop(ctx)
}

// Go builds a Unit from a function that runs for the life of the piece.
// Start launches run in a goroutine with a context of its own; Stop
// cancels that context and waits for run to return. run decides how to
// wind down when its context is done: that is where "finish what is in
// hand" lives. A run that returns context.Canceled is a clean stop.
//
// The unit implements Exiter, so the panel notices when run returns
// without having been asked to.
func Go(run func(ctx context.Context) error) Unit {
	return &goUnit{run: run}
}

type goUnit struct {
	run func(ctx context.Context) error

	mu     sync.Mutex
	cancel context.CancelFunc
	fin    chan struct{}
	done   chan error
	err    error
}

func (u *goUnit) Start(context.Context) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.fin != nil {
		select {
		case <-u.fin:
		default:
			return errors.New("quevu: unit already running")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	fin := make(chan struct{})
	done := make(chan error, 1)
	u.cancel, u.fin, u.done, u.err = cancel, fin, done, nil
	go func() {
		err := u.run(ctx)
		if errors.Is(err, context.Canceled) {
			err = nil
		}
		cancel()
		u.mu.Lock()
		u.err = err
		u.mu.Unlock()
		done <- err
		close(done)
		close(fin)
	}()
	return nil
}

func (u *goUnit) Stop(ctx context.Context) error {
	u.mu.Lock()
	cancel, fin := u.cancel, u.fin
	u.mu.Unlock()
	if fin == nil {
		return nil
	}
	cancel()
	select {
	case <-fin:
		u.mu.Lock()
		defer u.mu.Unlock()
		return u.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (u *goUnit) Done() <-chan error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.done
}
