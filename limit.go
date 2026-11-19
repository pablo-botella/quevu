package quevu

import "time"

// That something consumes the output of a log does not mean anything
// gives it back. A consumer only receives. Reading stored output is the job
// of a stored-data provider, a piece of its own that is attached on
// purpose (see Join), and that provider decides what it hands
// over: how much at a time, how far back, whether it lets itself be
// searched and for how long.
//
// The rest of quevu takes its word. A provider returning fewer chunks
// than asked, or none, is not questioned: the console shows what it got
// and treats an empty answer as the end of the way.

// Limits is what a stored-data provider is willing to give.
type Limits struct {
	// Page caps how many chunks one request returns. Zero means no cap.
	Page int
	// MaxAge hides chunks older than this. Zero means no limit. To the
	// reader the log simply starts there.
	MaxAge time.Duration
	// SearchBudget caps how many chunks one step of a search examines,
	// whatever the query asks for. Zero means the query decides.
	SearchBudget int
	// NoSearch refuses searches: every step finds nothing and reports
	// that there is nothing further.
	NoSearch bool
}

// Limit wraps a provider so that it gives no more than the limits allow.
// It is how a stored-data provider states its terms without each
// implementation having to grow the same options.
func Limit(p Provider, l Limits) Provider {
	return &limited{p: p, l: l, now: time.Now}
}

type limited struct {
	p   Provider
	l   Limits
	now func() time.Time
}

func (m *limited) page(n int) int {
	if m.l.Page > 0 && n > m.l.Page {
		return m.l.Page
	}
	return n
}

// recent drops the chunks older than MaxAge. Chunks come in ascending
// order, so the old ones are at the front.
func (m *limited) recent(cs []Chunk, err error) ([]Chunk, error) {
	if err != nil || m.l.MaxAge <= 0 {
		return cs, err
	}
	cutoff := m.now().Add(-m.l.MaxAge)
	i := 0
	for i < len(cs) && cs[i].Time.Before(cutoff) {
		i++
	}
	return cs[i:], nil
}

func (m *limited) Tail(n int) ([]Chunk, error) {
	return m.recent(m.p.Tail(m.page(n)))
}

func (m *limited) Before(seq uint64, n int) ([]Chunk, error) {
	return m.recent(m.p.Before(seq, m.page(n)))
}

func (m *limited) After(seq uint64, n int) ([]Chunk, error) {
	return m.recent(m.p.After(seq, m.page(n)))
}

// Search is the provider's say on being searched.
func (m *limited) Search(q Query) (Result, error) {
	if m.l.NoSearch {
		return Result{Reached: q.From, Done: true}, nil
	}
	if m.l.SearchBudget > 0 && (q.Budget <= 0 || q.Budget > m.l.SearchBudget) {
		q.Budget = m.l.SearchBudget
	}
	if m.l.MaxAge <= 0 {
		// Nothing is hidden: the provider underneath searches itself as
		// it sees fit, within the budget set here.
		return Search(m.p, q)
	}
	// Through the limited view, so a search sees no more than a reader.
	return Scan(m, q)
}

var (
	_ Provider = (*limited)(nil)
	_ Searcher = (*limited)(nil)
)
