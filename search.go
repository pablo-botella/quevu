package quevu

import (
	"bytes"
	"unicode/utf8"
)

// A log may hold millions of chunks, so a search is never "the whole
// log". It is a step: it starts at a position, walks in one direction,
// examines a bounded number of chunks and stops, saying how far it got.
// Whoever asked decides whether to take another step from there. The
// cost of one step is known in advance, whatever the size of the log.

// DefaultSearchBudget is how many chunks one step examines when the
// query does not say.
const DefaultSearchBudget = 100_000

// Query describes one step of a search.
type Query struct {
	// Text is what to look for. The match ignores case and ignores the
	// ANSI color sequences an application may have put in its output: it
	// is made against the text as a console shows it.
	Text string
	// From is where the step starts, and is itself not examined. Going
	// backwards, zero means the end of the log.
	From uint64
	// Forward walks towards newer chunks instead of older ones.
	Forward bool
	// Max ends the step once this many matches are found. Zero means 1.
	Max int
	// Budget ends the step once this many chunks have been examined,
	// found or not. Zero means DefaultSearchBudget.
	Budget int
	// History lets the step go into the history. It is set only because
	// the user asked: without it a search stays in recent output.
	History bool
}

// Result is what one step of a search found.
type Result struct {
	// Matches are the chunks found, in ascending order of position.
	Matches []Chunk
	// Reached is the last position examined. The next step, if there is
	// to be one, starts from it.
	Reached uint64
	// ReachedTime is when the chunk at Reached entered the log: a more
	// useful way to tell a person how far the step got.
	ReachedTime int64
	// Examined is how many chunks the step looked at.
	Examined int
	// Done means the step ran out of log, not out of budget: there is
	// nothing further in this direction.
	Done bool
	// History is set when the step ran out of recent output and there is
	// a history it did not look into. It is an offer for the user.
	History bool
}

// Searcher is implemented by providers that can search their own store
// better than by reading it chunk by chunk (an index, a query language).
// The contract is the one of Search: one bounded step.
type Searcher interface {
	Search(q Query) (Result, error)
}

// Search runs one step of a search over a provider. If the provider
// knows how to search itself it is asked to; otherwise the step reads the
// provider page by page, which works for any provider and costs what the
// budget says.
func Search(p Provider, q Query) (Result, error) {
	if s, ok := p.(Searcher); ok {
		return s.Search(q)
	}
	return Scan(p, q)
}

// Scan is the search that only needs a Provider: it reads pages of
// chunks in the direction asked and tests each one. It is exported for
// implementers of Searcher that want it as a fallback.
func Scan(p Provider, q Query) (Result, error) {
	const page = 1000
	max := q.Max
	if max <= 0 {
		max = 1
	}
	budget := q.Budget
	if budget <= 0 {
		budget = DefaultSearchBudget
	}
	needle := bytes.ToLower([]byte(q.Text))
	res := Result{Reached: q.From}
	if len(needle) == 0 {
		res.Done = true
		return res, nil
	}

	var scratch []byte
	test := func(c Chunk) {
		res.Examined++
		res.Reached, res.ReachedTime = c.Seq, c.Time.UnixNano()
		scratch = PlainText(scratch[:0], c.Data)
		if bytes.Contains(bytes.ToLower(scratch), needle) {
			res.Matches = append(res.Matches, c)
		}
	}

	for res.Examined < budget && len(res.Matches) < max {
		n := min(page, budget-res.Examined)
		var cs []Chunk
		var err error
		switch {
		case q.Forward:
			cs, err = p.After(res.Reached, n)
		case res.Reached == 0:
			cs, err = p.Tail(n)
		default:
			cs, err = p.Before(res.Reached, n)
		}
		if err != nil {
			return res, err
		}
		if len(cs) == 0 {
			res.Done = true
			break
		}
		if q.Forward {
			for _, c := range cs {
				if test(c); len(res.Matches) >= max {
					break
				}
			}
		} else {
			for i := len(cs) - 1; i >= 0; i-- {
				if test(cs[i]); len(res.Matches) >= max {
					break
				}
			}
		}
		if !q.Forward && res.Reached <= 1 {
			res.Done = len(res.Matches) < max
			break
		}
	}
	if !q.Forward {
		// Found newest first; handed over oldest first, like everything
		// else a provider returns.
		for i, j := 0, len(res.Matches)-1; i < j; i, j = i+1, j-1 {
			res.Matches[i], res.Matches[j] = res.Matches[j], res.Matches[i]
		}
	}
	return res, nil
}

// PlainText appends to dst the text of a chunk as a console shows it:
// without the ANSI escape sequences an application may have written for
// a terminal, and with invalid UTF-8 left as it is. It is what a search
// matches against, so a phrase is found even when a change of color
// falls in the middle of it.
func PlainText(dst, data []byte) []byte {
	for i := 0; i < len(data); {
		b := data[i]
		if b != 0x1b {
			_, size := utf8.DecodeRune(data[i:])
			dst = append(dst, data[i:i+size]...)
			i += size
			continue
		}
		i++
		if i >= len(data) {
			break
		}
		if data[i] != '[' {
			// A two-byte escape.
			i++
			continue
		}
		// A control sequence: parameters, then one final byte.
		i++
		for i < len(data) {
			f := data[i]
			i++
			if f >= 0x40 && f <= 0x7e {
				break
			}
		}
	}
	return dst
}
