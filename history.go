package quevu

import (
	"errors"
	"io"
)

// WithHistory joins a live source and a stored provider of the same log
// into one source, so a console can walk from the output of this instant
// back to the first chunk ever stored without knowing that it changes
// provider on the way.
//
// The live source answers first: it is the log itself, with its short
// memory. Where it ends, the stored provider takes over. Positions are
// what makes the join seamless: both number the same chunks the same way.
//
// The stored provider is meant to be a reader with a life of its own (a
// file handle or a connection that is not the one the log writes
// through), so going through history never competes with the component
// that guarantees persistence.
func WithHistory(live Source, stored Provider) Source {
	if stored == nil {
		return live
	}
	return &historySource{live: live, stored: stored}
}

type historySource struct {
	live   Source
	stored Provider
}

func (h *historySource) Name() string { return h.live.Name() }

func (h *historySource) Tail(n int) ([]Chunk, error) {
	cs, err := h.live.Tail(n)
	if err != nil || len(cs) >= n {
		return cs, err
	}
	if len(cs) == 0 {
		return h.stored.Tail(n)
	}
	older, err := h.stored.Before(cs[0].Seq, n-len(cs))
	if err != nil {
		return cs, nil
	}
	return append(older, cs...), nil
}

func (h *historySource) Before(seq uint64, n int) ([]Chunk, error) {
	cs, err := h.live.Before(seq, n)
	if err != nil || len(cs) >= n {
		return cs, err
	}
	from := seq
	if len(cs) > 0 {
		from = cs[0].Seq
	}
	older, err := h.stored.Before(from, n-len(cs))
	if err != nil {
		if len(cs) > 0 {
			return cs, nil
		}
		return nil, err
	}
	return append(older, cs...), nil
}

func (h *historySource) After(seq uint64, n int) ([]Chunk, error) {
	cs, err := h.live.After(seq, n)
	if err != nil {
		return nil, err
	}
	if len(cs) > 0 && cs[0].Seq == seq+1 {
		return cs, nil
	}
	// The short memory has moved past seq (or has nothing newer): what
	// follows seq, if it was kept, is in the store.
	stored, serr := h.stored.After(seq, n)
	if serr == nil && len(stored) > 0 {
		return stored, nil
	}
	return cs, nil
}

// Search runs one bounded step over the joined source, so a search walks
// out of the short memory and into the store like any other reader. The
// stored provider keeps its say: if it states terms for being searched
// (see Limit), they apply to the whole step.
func (h *historySource) Search(q Query) (Result, error) {
	if l, ok := h.stored.(*limited); ok {
		if l.l.NoSearch {
			// The short memory is the log's own and stays searchable.
			return Scan(h.live, q)
		}
		if l.l.SearchBudget > 0 && (q.Budget <= 0 || q.Budget > l.l.SearchBudget) {
			q.Budget = l.l.SearchBudget
		}
	}
	return Scan(h, q)
}

// Stats forwards the device queues of the live source.
func (h *historySource) Stats() []DeviceStats {
	if s, ok := h.live.(StatSource); ok {
		return s.Stats()
	}
	return nil
}

// Close closes the live source first, so the last chunks reach the store,
// and then the stored provider.
func (h *historySource) Close() error {
	var errs []error
	if c, ok := h.live.(io.Closer); ok {
		errs = append(errs, c.Close())
	}
	if c, ok := h.stored.(io.Closer); ok {
		errs = append(errs, c.Close())
	}
	return errors.Join(errs...)
}

// Subscribe opens a feed on the live source and answers the requests to
// move by position itself, so they too reach the store.
func (h *historySource) Subscribe(buffer int) *Feed {
	inner := h.live.Subscribe(buffer)
	data := make(chan Delivery)
	req := make(chan Request, 16)
	done := make(chan struct{})

	go func() {
		defer close(data)
		send := func(d Delivery) bool {
			select {
			case data <- d:
				return true
			case <-done:
				return false
			}
		}
		for {
			select {
			case <-done:
				return
			case d, ok := <-inner.Data:
				if !ok || !send(d) {
					return
				}
			case r := <-req:
				switch r.Op {
				case OpBefore:
					cs, err := h.Before(r.Seq, r.N)
					if !send(Delivery{Op: r.Op, Chunks: cs, Err: err}) {
						return
					}
				case OpAfter:
					cs, err := h.After(r.Seq, r.N)
					if !send(Delivery{Op: r.Op, Chunks: cs, Err: err}) {
						return
					}
				default:
					// Pass it up, without ever leaving the inner feed
					// unread: it may be waiting to hand something down.
					for passed := false; !passed; {
						select {
						case inner.Req <- r:
							passed = true
						case d, ok := <-inner.Data:
							if !ok || !send(d) {
								return
							}
						case <-done:
							return
						}
					}
				}
			}
		}
	}()

	return NewFeed(data, req, func() {
		close(done)
		inner.Close()
	})
}

var (
	_ Source     = (*historySource)(nil)
	_ StatSource = (*historySource)(nil)
	_ io.Closer  = (*historySource)(nil)
)
