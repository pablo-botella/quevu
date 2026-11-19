package quevu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// Controller is the panel as a visual part sees it: the switches (units
// and their state) and the screens (sources). Panel implements it inside
// the application; the wire client implements it for a separate
// controller. The visual part talks to this interface and does not know
// which of the two is behind.
type Controller interface {
	// Units lists the units in the order they were added.
	Units() []UnitInfo
	// Start and Stop act on one unit.
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	// StartAll and StopAll are the global switch.
	StartAll(ctx context.Context) error
	StopAll(ctx context.Context) error
	// Watch calls fn on every change of state of a unit, until cancel is
	// called. fn must not block.
	Watch(fn func(UnitInfo)) (cancel func())
	// Sources lists the logs.
	Sources() []Source
}

// ErrUnknownUnit is returned when a unit name is not registered.
var ErrUnknownUnit = errors.New("quevu: unknown unit")

// Panel coordinates the lifecycle of an application: its units, started
// in the order they were added and stopped in reverse, and its logs,
// closed last so the final words of every unit reach their consumers.
type Panel struct {
	// StopTimeout bounds the orderly stop performed by Run. Zero means no
	// limit: the application takes the time it needs. The operating
	// system may have a limit of its own when it stops a service.
	StopTimeout time.Duration

	mu       sync.Mutex
	units    []*entry
	byName   map[string]*entry
	sources  []Source
	closers  []io.Closer
	watchers map[uint64]func(UnitInfo)
	nextW    uint64
}

type entry struct {
	name string
	unit Unit
	op   sync.Mutex // serializes Start and Stop of this unit

	// guarded by Panel.mu
	state   State
	err     error
	started bool
	gen     uint64
}

// New returns an empty panel.
func New() *Panel {
	return &Panel{
		byName:   make(map[string]*entry),
		watchers: make(map[uint64]func(UnitInfo)),
	}
}

// Add registers a unit under a name. Units start in the order they are
// added and stop in reverse.
func (p *Panel) Add(name string, u Unit) error {
	if u == nil {
		return errors.New("quevu: nil unit")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, dup := p.byName[name]; dup {
		return fmt.Errorf("quevu: duplicate unit %q", name)
	}
	e := &entry{name: name, unit: u}
	p.units = append(p.units, e)
	p.byName[name] = e
	return nil
}

// AddCloser registers something to close once every unit has stopped:
// the logs of the application. Closing a log drains its queues and
// closes its consumers, so this is what makes the last words of every
// unit arrive. They are closed in the order they were added.
func (p *Panel) AddCloser(c io.Closer) {
	p.mu.Lock()
	p.closers = append(p.closers, c)
	p.mu.Unlock()
}

// AddSource registers what a console shows in a tab: the screen of a log
// and whatever providers stand behind it. A source that implements
// io.Closer is closed by Close, after the logs.
func (p *Panel) AddSource(s Source) {
	p.mu.Lock()
	p.sources = append(p.sources, s)
	p.mu.Unlock()
}

// Sources lists the registered logs.
func (p *Panel) Sources() []Source {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Source(nil), p.sources...)
}

// Units lists the units and their state, in the order they were added.
func (p *Panel) Units() []UnitInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]UnitInfo, len(p.units))
	for i, e := range p.units {
		out[i] = e.info()
	}
	return out
}

// Watch calls fn on every change of state of a unit until cancel is
// called. fn runs on the goroutine that caused the change and must not
// block or call back into the panel.
func (p *Panel) Watch(fn func(UnitInfo)) (cancel func()) {
	p.mu.Lock()
	id := p.nextW
	p.nextW++
	p.watchers[id] = fn
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		delete(p.watchers, id)
		p.mu.Unlock()
	}
}

func (e *entry) info() UnitInfo {
	info := UnitInfo{Name: e.name, State: e.state}
	if e.err != nil {
		info.Err = e.err.Error()
	}
	return info
}

// change applies fn to the entry under the lock and tells the watchers.
func (p *Panel) change(e *entry, fn func()) {
	p.mu.Lock()
	fn()
	info := e.info()
	fns := make([]func(UnitInfo), 0, len(p.watchers))
	for _, w := range p.watchers {
		fns = append(fns, w)
	}
	p.mu.Unlock()
	for _, w := range fns {
		w(info)
	}
}

func (p *Panel) find(name string) (*entry, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byName[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownUnit, name)
	}
	return e, nil
}

func (p *Panel) isStarted(e *entry) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return e.started
}

// Start starts one unit. Starting a unit that is already running does
// nothing.
func (p *Panel) Start(ctx context.Context, name string) error {
	e, err := p.find(name)
	if err != nil {
		return err
	}
	return p.start(ctx, e)
}

func (p *Panel) start(ctx context.Context, e *entry) error {
	e.op.Lock()
	defer e.op.Unlock()
	if p.isStarted(e) {
		return nil
	}
	p.change(e, func() { e.state, e.err = Starting, nil })
	if err := e.unit.Start(ctx); err != nil {
		p.change(e, func() { e.state, e.err = Failed, err })
		return fmt.Errorf("quevu: start %s: %w", e.name, err)
	}
	var gen uint64
	p.change(e, func() {
		e.state, e.started = Running, true
		e.gen++
		gen = e.gen
	})
	if x, ok := e.unit.(Exiter); ok {
		if done := x.Done(); done != nil {
			go p.watchExit(e, gen, done)
		}
	}
	return nil
}

// watchExit notices a unit that ended without being asked to.
func (p *Panel) watchExit(e *entry, gen uint64, done <-chan error) {
	err := <-done
	p.mu.Lock()
	own := e.gen == gen && e.started && e.state == Running
	p.mu.Unlock()
	if !own {
		return
	}
	p.change(e, func() {
		if e.gen != gen || !e.started || e.state != Running {
			return
		}
		e.started = false
		if err != nil {
			e.state, e.err = Failed, err
		} else {
			e.state, e.err = Stopped, nil
		}
	})
}

// Stop stops one unit in an orderly way and returns when the unit says it
// is done. Stopping a unit that is not running does nothing.
func (p *Panel) Stop(ctx context.Context, name string) error {
	e, err := p.find(name)
	if err != nil {
		return err
	}
	return p.stop(ctx, e)
}

func (p *Panel) stop(ctx context.Context, e *entry) error {
	e.op.Lock()
	defer e.op.Unlock()
	if !p.isStarted(e) {
		return nil
	}
	p.change(e, func() { e.state = Stopping })
	if err := e.unit.Stop(ctx); err != nil {
		p.change(e, func() { e.state, e.err = Failed, err })
		return fmt.Errorf("quevu: stop %s: %w", e.name, err)
	}
	p.change(e, func() { e.state, e.err, e.started = Stopped, nil, false })
	return nil
}

func (p *Panel) entries() []*entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*entry(nil), p.units...)
}

// StartAll starts every unit in the order they were added. It stops at
// the first one that fails and leaves the ones already started running.
func (p *Panel) StartAll(ctx context.Context) error {
	for _, e := range p.entries() {
		if err := p.start(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

// StopAll stops every unit in reverse order. A unit that fails to stop
// does not keep the rest from being asked; the errors are joined.
func (p *Panel) StopAll(ctx context.Context) error {
	var errs []error
	es := p.entries()
	for i := len(es) - 1; i >= 0; i-- {
		if err := p.stop(ctx, es[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Close closes the logs and then the sources that can be closed. It
// belongs after StopAll: the logs go last, once every unit has said its
// final words, and the providers that read what they stored go after
// them.
func (p *Panel) Close() error {
	var errs []error
	p.mu.Lock()
	closers := append([]io.Closer(nil), p.closers...)
	p.mu.Unlock()
	for _, c := range closers {
		if err := c.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	for _, s := range p.Sources() {
		if c, ok := s.(io.Closer); ok {
			if err := c.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// Shutdown performs the orderly stop: every unit in reverse order, within
// StopTimeout if one is set, and then the logs.
func (p *Panel) Shutdown() error {
	ctx := context.Background()
	if p.StopTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.StopTimeout)
		defer cancel()
	}
	return errors.Join(p.StopAll(ctx), p.Close())
}

// Run is the whole life of an application without a visual part: it
// starts every unit, waits until ctx is done or the operating system asks
// the process to end (interrupt or terminate), and then shuts down in
// order. A second signal during the shutdown ends the process the hard
// way, as usual.
func (p *Panel) Run(ctx context.Context) error {
	ctx, release := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	startErr := p.StartAll(ctx)
	if startErr == nil {
		<-ctx.Done()
	}
	release()
	return errors.Join(startErr, p.Shutdown())
}

var _ Controller = (*Panel)(nil)
