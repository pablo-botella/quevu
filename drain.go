package quevu

import (
	"errors"
	"sync"
)

// Batcher is the shape of a writer that works in batches: it takes a few
// consecutive chunks at a time and is closed at the end. Most stores are
// like this, because writing many rows at once is what makes them fast.
type Batcher interface {
	// Write takes a batch of consecutive chunks. The batch is not reused
	// by the caller.
	Write(batch []Chunk) error
	// Close flushes and releases. It is called once, after the last Write.
	Close() error
}

// DefaultBatch is the largest batch Drain makes when it is not told.
const DefaultBatch = 512

// Failures is the body of a Writer that works in batches, and the count
// of what it could not write. The zero value is ready to use; a writer
// holds one and builds its two methods on it:
//
//	func (d *Device) Run(in <-chan quevu.Chunk) error { return d.fails.Drain(in, 0, d) }
//	func (d *Device) Failed() (uint64, error)         { return d.fails.Failed() }
type Failures struct {
	mu   sync.Mutex
	n    uint64
	last error
}

// Drain receives from in until it is closed, hands what arrives to b in
// batches of at most max chunks (DefaultBatch when max is not positive)
// and closes b at the end. A batch is whatever is already waiting: Drain
// never waits to fill one.
//
// A batch that fails is counted and the work goes on, because a writer
// that stops receiving holds back everything behind it. Drain returns the
// first of those errors, joined with the one of Close.
func (f *Failures) Drain(in <-chan Chunk, max int, b Batcher) error {
	if max <= 0 {
		max = DefaultBatch
	}
	var first error
	for c := range in {
		batch := make([]Chunk, 1, min(max, len(in)+1))
		batch[0] = c
	fill:
		for len(batch) < max {
			select {
			case c, ok := <-in:
				if !ok {
					break fill
				}
				batch = append(batch, c)
			default:
				break fill
			}
		}
		if err := b.Write(batch); err != nil {
			f.mu.Lock()
			f.n += uint64(len(batch))
			f.last = err
			f.mu.Unlock()
			if first == nil {
				first = err
			}
		}
	}
	return errors.Join(first, b.Close())
}

// Failed returns the chunks lost so far in failed batches, and the last
// error.
func (f *Failures) Failed() (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n, f.last
}

// Batches makes a Writer out of a Batcher: what a hub is connected to
// when the destination is something that only knows how to take batches.
// max is the largest batch, DefaultBatch when it is not positive.
func Batches(b Batcher, max int) Writer { return &batches{b: b, max: max} }

type batches struct {
	b     Batcher
	max   int
	fails Failures
}

func (w *batches) Run(in <-chan Chunk) error { return w.fails.Drain(in, w.max, w.b) }
func (w *batches) Failed() (uint64, error)   { return w.fails.Failed() }

// LastSeq passes on the last position of a batcher that knows it.
func (w *batches) LastSeq() (uint64, error) {
	if s, ok := w.b.(SeqWriter); ok {
		return s.LastSeq()
	}
	return 0, nil
}

var (
	_ Writer      = (*batches)(nil)
	_ FailCounter = (*batches)(nil)
	_ SeqWriter   = (*batches)(nil)
)
