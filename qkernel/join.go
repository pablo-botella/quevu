package qkernel

import (
	"errors"
	"io"
)

// Roles are the providers a console can turn to beyond what its own
// screen remembers. A role is given here, by configuration: nothing in a
// provider says which one it plays, and the same kind of provider can be
// set up twice to play both (one over a live table, another over an
// archive).
type Roles struct {
	// Immediate is asked automatically, as soon as the console needs
	// something its screen no longer remembers.
	Immediate Provider
	// Historical is asked only when the user says so. A console that
	// runs out of recent output tells its user, and offers the history if
	// there is one configured here; it never goes there by itself.
	//
	// There is one historical provider as far as a console is concerned.
	// If the history lives in several places, this is an aggregator that
	// knows them (see Aggregate): the console never does.
	Historical Provider
}

// Join builds the source a console is given: the screen of a log, which
// is the console's own memory and its live output, and the providers
// that stand behind it in their roles.
//
// A console escalates in three steps:
//
//  1. Its own memory: what the screen remembers.
//  2. The immediate provider, automatically.
//  3. The historical provider, only on its user's request.
//
// Positions are what make the joins seamless: every piece numbers the
// same chunks the same way.
func Join(screen Source, r Roles) Source {
	if r.Immediate == nil && r.Historical == nil {
		return screen
	}
	return &joined{screen: screen, imm: r.Immediate, hist: r.Historical}
}

type joined struct {
	screen Source
	imm    Provider
	hist   Provider
}

func (j *joined) Name() string { return j.screen.Name() }

// Tail, Before and After are the recent output: the screen and the
// immediate provider. The history is not reachable through them.

func (j *joined) Tail(n int) ([]Chunk, error) {
	cs, err := j.screen.Tail(n)
	if err != nil || len(cs) >= n || j.imm == nil {
		return cs, err
	}
	if len(cs) == 0 {
		return j.imm.Tail(n)
	}
	older, err := j.imm.Before(cs[0].Seq, n-len(cs))
	if err != nil {
		return cs, nil
	}
	return append(older, cs...), nil
}

func (j *joined) Before(seq uint64, n int) ([]Chunk, error) {
	cs, err := j.screen.Before(seq, n)
	if err != nil || len(cs) >= n || j.imm == nil {
		return cs, err
	}
	from := seq
	if len(cs) > 0 {
		from = cs[0].Seq
	}
	older, err := j.imm.Before(from, n-len(cs))
	if err != nil {
		if len(cs) > 0 {
			return cs, nil
		}
		return nil, err
	}
	return append(older, cs...), nil
}

func (j *joined) After(seq uint64, n int) ([]Chunk, error) {
	cs, err := j.screen.After(seq, n)
	if err != nil {
		return nil, err
	}
	if len(cs) > 0 && cs[0].Seq == seq+1 {
		return cs, nil
	}
	// The screen has moved past seq, or has nothing newer: what follows
	// seq, if it was kept, is with the immediate provider.
	if j.imm != nil {
		if kept, kerr := j.imm.After(seq, n); kerr == nil && len(kept) > 0 {
			return kept, nil
		}
	}
	return cs, nil
}

// historyBefore is the one way into the history.
func (j *joined) historyBefore(seq uint64, n int) ([]Chunk, error) {
	if j.hist == nil {
		return nil, nil
	}
	return j.hist.Before(seq, n)
}

// historyAfter walks forwards through the history and, where it ends,
// out of it into recent output. Leaving the history needs no permission.
func (j *joined) historyAfter(seq uint64, n int) ([]Chunk, error) {
	if j.hist != nil {
		cs, err := j.hist.After(seq, n)
		if err != nil {
			return nil, err
		}
		if len(cs) > 0 {
			return cs, nil
		}
	}
	return j.After(seq, n)
}

// Search runs one bounded step. Each piece is searched through its own
// interface and on its own terms, in the order the direction asks for,
// and the step goes on into the next piece only if the one before ran
// out of log first. The history is one of the pieces only when the query
// says the user asked for it; otherwise a step that runs out of recent
// output says there is a history and stops.
func (j *joined) Search(q Query) (Result, error) {
	if q.Budget <= 0 {
		q.Budget = DefaultSearchBudget
	}
	if q.Max <= 0 {
		q.Max = 1
	}
	// Newest first: that is the order going backwards.
	parts := []Provider{j.screen}
	if j.imm != nil {
		parts = append(parts, j.imm)
	}
	if q.History && j.hist != nil {
		parts = append(parts, j.hist)
	}
	if q.Forward {
		for a, b := 0, len(parts)-1; a < b; a, b = a+1, b-1 {
			parts[a], parts[b] = parts[b], parts[a]
		}
	}

	var res Result
	res.Reached = q.From
	step := q
	for i, p := range parts {
		part, err := Search(p, step)
		if err != nil {
			return res, err
		}
		if q.Forward {
			res.Matches = append(res.Matches, part.Matches...)
		} else {
			res.Matches = append(part.Matches, res.Matches...)
		}
		res.Examined += part.Examined
		if part.Examined > 0 || i == 0 {
			res.Reached, res.ReachedTime = part.Reached, part.ReachedTime
		}
		res.Done = part.Done
		if !part.Done || len(res.Matches) >= q.Max || res.Examined >= q.Budget {
			return res, nil
		}
		if part.OutOfRange {
			// Past the bound of the time range: no other piece, and no
			// history, can hold anything in range.
			res.OutOfRange = true
			return res, nil
		}
		step.From = res.Reached
		step.Max = q.Max - len(res.Matches)
		step.Budget = q.Budget - res.Examined
	}
	// Ran out of everything it was allowed to look at.
	res.History = !q.Forward && !q.History && j.hist != nil
	return res, nil
}

// Stats forwards the writers of the log, as the screen shows them.
func (j *joined) Stats() []WriterStats {
	if s, ok := j.screen.(StatSource); ok {
		return s.Stats()
	}
	return nil
}

// Close closes the screen and then the providers that can be closed.
func (j *joined) Close() error {
	var errs []error
	for _, x := range []any{j.screen, j.imm, j.hist} {
		if c, ok := x.(io.Closer); ok {
			errs = append(errs, c.Close())
		}
	}
	return errors.Join(errs...)
}

// Subscribe opens a feed on the screen and answers the requests to move
// by position itself, so they reach the provider that plays each role.
func (j *joined) Subscribe(buffer int) *Feed {
	inner := j.screen.Subscribe(buffer)
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
				d := Delivery{Op: r.Op}
				switch r.Op {
				case OpBefore:
					d.Chunks, d.Err = j.Before(r.Seq, r.N)
					// Recent output ends here. If there is a history,
					// say so: asking for it is the user's call.
					d.History = j.hist != nil && len(d.Chunks) < r.N
				case OpAfter:
					d.Chunks, d.Err = j.After(r.Seq, r.N)
				case OpHistoryBefore:
					d.Chunks, d.Err = j.historyBefore(r.Seq, r.N)
				case OpHistoryAfter:
					d.Chunks, d.Err = j.historyAfter(r.Seq, r.N)
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
					continue
				}
				if !send(d) {
					return
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
	_ Source     = (*joined)(nil)
	_ Searcher   = (*joined)(nil)
	_ StatSource = (*joined)(nil)
	_ io.Closer  = (*joined)(nil)
)
