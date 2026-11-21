package quevu

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// OnFull says what a hub does with a chunk when the channel of a writer
// is full.
type OnFull int

const (
	// Wait holds the hub until the writer makes room. Nothing is lost, at
	// the price of holding back everything else. It is what a persistent
	// writer is connected with.
	Wait OnFull = iota
	// Skip leaves the chunk out for that writer and counts it. It is what
	// a volatile writer is connected with.
	Skip
)

// Defaults of the channels of a hub.
const (
	DefaultWriterBuffer = 4096 // chunks that may wait for one writer
	DefaultInputBuffer  = 1024 // entries that may wait for the hub
)

// WriterOptions says how a writer is connected to a hub.
type WriterOptions struct {
	// Name is how the hub calls the writer when it speaks of it. Default:
	// its type.
	Name string
	// Buffer is the capacity of its channel, in chunks: the limit.
	// Default DefaultWriterBuffer.
	Buffer int
	// OnFull is what happens at the limit. Default Wait.
	OnFull OnFull
}

// QHub is the hub: it distributes one log, one stream of output.
//
// It has one input channel and one output channel for each of its
// writers. It does one thing: it takes what arrives on the input, gives
// it a position, and sends it on every output. For a hub there are only
// channels. It does not know what a pipe, a socket or a file is, who
// writes into its input or what a writer does with what it receives.
//
// What flows through one hub is a log: the output of one piece, in the
// order it arrived. Whatever would go to a separate log file is what
// should have a hub of its own.
//
// The capacity of an output channel is the limit of what may wait for
// its writer. What the hub does at that limit is decided when the writer
// is connected, and it is what makes a writer persistent or volatile:
//
//   - Wait: the hub stops until the writer makes room. Nothing is lost,
//     and everything else is held back meanwhile, the other writers and
//     the producers. The hub says which writer it is waiting for.
//   - Skip: the hub leaves that chunk out for that writer, counts it, and
//     goes on. The writer sees a gap in the positions.
//
// A hub never answers a question: there is no way to ask it for data.
// Remembering recent output, showing it, giving it back by position and
// searching it are the work of others: writers and providers. It works
// the same with or without a visual part.
type QHub struct {
	name string
	now  func() time.Time

	in       chan Entry
	stop     chan struct{} // closed by Close: no more input
	finished chan struct{} // closed when the last chunk has been sent
	once     sync.Once
	err      error

	seq     atomic.Uint64
	waiting atomic.Pointer[hubOutput]
	outs    []*hubOutput
}

// hubOutput is the lane between a hub and one writer: its channel and
// what the hub knows of how it is going.
type hubOutput struct {
	name string
	w    Writer
	ch   chan Chunk
	skip bool

	gone chan struct{} // closed when Run has returned
	err  error         // what Run returned; read after gone

	written atomic.Uint64
	skipped atomic.Uint64
	waits   atomic.Uint64
	lost    atomic.Uint64 // chunks that found the writer gone
}

type hubConfig struct {
	writers []hubWriter
	input   int
	now     func() time.Time
}

type hubWriter struct {
	w   Writer
	opt WriterOptions
}

// HubOption configures a QHub.
type HubOption func(*hubConfig)

// WithWriter connects a writer with the defaults: the hub waits for it.
func WithWriter(w Writer) HubOption {
	return WithWriterOptions(w, WriterOptions{})
}

// WithWriterOptions connects a writer and says how.
func WithWriterOptions(w Writer, opt WriterOptions) HubOption {
	return func(cfg *hubConfig) { cfg.writers = append(cfg.writers, hubWriter{w, opt}) }
}

// WithInput sets the capacity of the input channel. Default
// DefaultInputBuffer.
func WithInput(buffer int) HubOption {
	return func(c *hubConfig) { c.input = buffer }
}

// WithClock replaces the clock that stamps the chunks. For tests.
func WithClock(now func() time.Time) HubOption {
	return func(c *hubConfig) { c.now = now }
}

// NewQHub creates a hub and starts it. name is the name of the log it
// distributes. A hub with no writers is legal and pointless: what is sent
// to it goes nowhere.
//
// Positions continue after the highest one any of its writers already
// holds (see SeqWriter), so they stay unique across restarts.
func NewQHub(name string, opts ...HubOption) (*QHub, error) {
	cfg := hubConfig{now: time.Now, input: DefaultInputBuffer}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.input < 0 {
		cfg.input = 0
	}
	h := &QHub{
		name:     name,
		now:      cfg.now,
		in:       make(chan Entry, cfg.input),
		stop:     make(chan struct{}),
		finished: make(chan struct{}),
	}
	var last uint64
	for _, s := range cfg.writers {
		if sw, ok := s.w.(SeqWriter); ok {
			n, err := sw.LastSeq()
			if err != nil {
				return nil, err
			}
			last = max(last, n)
		}
	}
	h.seq.Store(last)
	for _, s := range cfg.writers {
		opt := s.opt
		if opt.Buffer <= 0 {
			opt.Buffer = DefaultWriterBuffer
		}
		if opt.Name == "" {
			opt.Name = fmt.Sprintf("%T", s.w)
		}
		o := &hubOutput{
			name: opt.Name,
			w:    s.w,
			ch:   make(chan Chunk, opt.Buffer),
			skip: opt.OnFull == Skip,
			gone: make(chan struct{}),
		}
		h.outs = append(h.outs, o)
		go func() {
			o.err = o.w.Run(o.ch)
			close(o.gone)
		}()
	}
	go h.run()
	return h, nil
}

// Name is the name of the log this hub distributes.
func (h *QHub) Name() string { return h.name }

// In is the input channel: what a producer sends on. Any number of
// goroutines may send. Nobody closes it; see Done.
func (h *QHub) In() chan<- Entry { return h.in }

// Done is closed when the hub takes no more input. What is sent after
// that goes nowhere, so a producer should stop; an Inlet does.
func (h *QHub) Done() <-chan struct{} { return h.stop }

// run is the hub: receive, give a position, send on.
func (h *QHub) run() {
	defer close(h.finished)
	for {
		select {
		case e := <-h.in:
			h.deliver(e)
		case <-h.stop:
			// What was already in the input still goes out.
			for {
				select {
				case e := <-h.in:
					h.deliver(e)
				default:
					for _, o := range h.outs {
						close(o.ch)
					}
					return
				}
			}
		}
	}
}

func (h *QHub) deliver(e Entry) {
	at := e.Time
	if at.IsZero() {
		at = h.now()
	}
	c := Chunk{Seq: h.seq.Add(1), Time: at, Data: e.Data}
	for _, o := range h.outs {
		h.send(o, c)
	}
}

func (h *QHub) send(o *hubOutput, c Chunk) {
	select {
	case <-o.gone:
		o.lost.Add(1)
		return
	default:
	}
	select {
	case o.ch <- c:
		o.written.Add(1)
		return
	default:
	}
	if o.skip {
		o.skipped.Add(1)
		return
	}
	// The writer is at its limit and is not to lose anything: the hub
	// waits, and says for whom.
	o.waits.Add(1)
	h.waiting.Store(o)
	select {
	case o.ch <- c:
		o.written.Add(1)
	case <-o.gone:
		o.lost.Add(1)
	}
	h.waiting.Store(nil)
}

// Close stops taking input, sends on what had already arrived, closes
// the channels of the writers and waits for every one of them to finish.
// The hubs go last in an orderly stop. It returns what the writers
// returned.
func (h *QHub) Close() error {
	h.once.Do(func() {
		close(h.stop)
		<-h.finished
		var errs []error
		for _, o := range h.outs {
			<-o.gone
			if o.err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", o.name, o.err))
			}
		}
		h.err = errors.Join(errs...)
	})
	return h.err
}

// Seq returns the position of the last chunk the hub has given out.
func (h *QHub) Seq() uint64 { return h.seq.Load() }

// Stuffed names the writer the hub is waiting for right now, the one
// that has had more than it can take; empty when the hub is not held.
// While it returns a name nothing moves: no other writer receives and
// the producers wait.
func (h *QHub) Stuffed() string {
	if o := h.waiting.Load(); o != nil {
		return o.name
	}
	return ""
}

// Stats shows every writer, in the order they were connected.
func (h *QHub) Stats() []WriterStats {
	held := h.waiting.Load()
	out := make([]WriterStats, len(h.outs))
	for i, o := range h.outs {
		st := WriterStats{
			Name:     o.name,
			Queued:   len(o.ch),
			Capacity: cap(o.ch),
			Written:  o.written.Load(),
			Skipped:  o.skipped.Load(),
			Waiting:  held == o,
			Waits:    o.waits.Load(),
			Failed:   o.lost.Load(),
		}
		if fc, ok := o.w.(FailCounter); ok {
			n, err := fc.Failed()
			st.Failed += n
			st.LastErr = err
		}
		select {
		case <-o.gone:
			if o.err != nil {
				st.LastErr = o.err
			}
		default:
		}
		out[i] = st
	}
	return out
}

var (
	_ StatSource = (*QHub)(nil)
	_ Input      = (*QHub)(nil)
)
