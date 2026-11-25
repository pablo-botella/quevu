package qkernel

import (
	"errors"
	"io"
)

// Aggregate makes one provider out of several. It is what a history
// spread over many places looks like from outside: whoever asks sees one
// provider and never learns where the chunks really are.
//
// The parts are given oldest first, and each is expected to hold a
// stretch of the same log: an archive per year, a table per month, the
// live table last. Stretches may overlap; a chunk held by two parts is
// given once.
//
// A request is answered by asking the parts in the order the direction
// calls for, and only as many of them as it takes to fill the answer.
func Aggregate(oldestFirst ...Provider) Provider {
	parts := make([]Provider, 0, len(oldestFirst))
	for _, p := range oldestFirst {
		if p != nil {
			parts = append(parts, p)
		}
	}
	return &aggregate{parts: parts}
}

type aggregate struct {
	parts []Provider
}

// backwards collects up to n chunks walking from the newest part to the
// oldest. first asks the newest part; older ones are asked for what
// precedes the oldest chunk found so far.
func (a *aggregate) backwards(seq uint64, n int, tail bool) ([]Chunk, error) {
	var out []Chunk
	var firstErr error
	for i := len(a.parts) - 1; i >= 0 && len(out) < n; i-- {
		p := a.parts[i]
		var cs []Chunk
		var err error
		switch {
		case len(out) > 0:
			cs, err = p.Before(out[0].Seq, n-len(out))
		case tail:
			cs, err = p.Tail(n)
		default:
			cs, err = p.Before(seq, n)
		}
		if err != nil {
			// One part out of reach does not hide the others.
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out = append(cs, out...)
	}
	if len(out) > 0 {
		return out, nil
	}
	return nil, firstErr
}

func (a *aggregate) Tail(n int) ([]Chunk, error) {
	if n <= 0 {
		return nil, nil
	}
	return a.backwards(0, n, true)
}

func (a *aggregate) Before(seq uint64, n int) ([]Chunk, error) {
	if n <= 0 {
		return nil, nil
	}
	return a.backwards(seq, n, false)
}

func (a *aggregate) After(seq uint64, n int) ([]Chunk, error) {
	if n <= 0 {
		return nil, nil
	}
	var out []Chunk
	var firstErr error
	from := seq
	for _, p := range a.parts {
		if len(out) >= n {
			break
		}
		cs, err := p.After(from, n-len(out))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if len(cs) > 0 {
			out = append(out, cs...)
			from = cs[len(cs)-1].Seq
		}
	}
	if len(out) > 0 {
		return out, nil
	}
	return nil, firstErr
}

// Close closes the parts that can be closed.
func (a *aggregate) Close() error {
	var errs []error
	for _, p := range a.parts {
		if c, ok := p.(io.Closer); ok {
			errs = append(errs, c.Close())
		}
	}
	return errors.Join(errs...)
}

var (
	_ Provider  = (*aggregate)(nil)
	_ io.Closer = (*aggregate)(nil)
)
