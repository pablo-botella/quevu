package quevu

import "sync"

// Op identifies a request sent up a feed and the delivery that answers it.
type Op int

const (
	// OpLive marks a delivery of new chunks, sent while the feed follows
	// the log. It is never a request.
	OpLive Op = iota + 1
	// OpFollow asks for the last N chunks and, from there on, the live
	// output. The answer replaces whatever the console was showing.
	OpFollow
	// OpPause hangs the "do not disturb" sign: the source stops sending
	// live output to this feed until the next OpFollow.
	OpPause
	// OpTail asks for the last N chunks without changing the feed state.
	OpTail
	// OpBefore asks for the N chunks that precede Seq.
	OpBefore
	// OpAfter asks for the N chunks that follow Seq.
	OpAfter
	// OpHistoryBefore is OpBefore answered from the history. A console
	// sends it only because its user asked for the history: nothing
	// reaches the historical provider on its own.
	OpHistoryBefore
	// OpHistoryAfter is OpAfter for a console that is in the history: it
	// walks forwards through it and out of it, back into recent output.
	OpHistoryAfter
)

func (o Op) String() string {
	switch o {
	case OpLive:
		return "live"
	case OpFollow:
		return "follow"
	case OpPause:
		return "pause"
	case OpTail:
		return "tail"
	case OpBefore:
		return "before"
	case OpAfter:
		return "after"
	case OpHistoryBefore:
		return "history-before"
	case OpHistoryAfter:
		return "history-after"
	}
	return "unknown"
}

// Request travels from the console to the source.
type Request struct {
	Op  Op
	Seq uint64
	N   int
}

// Delivery travels from the source to the console.
type Delivery struct {
	// Op is OpLive for live output, otherwise the Op of the request this
	// delivery answers.
	Op     Op
	Chunks []Chunk
	// Full is set on an OpPause delivery that no request asked for: the
	// console fell behind, its buffer filled up and the source hung the
	// sign by itself. Live output sent meanwhile is not kept for it.
	Full bool
	// History is set on an answer to OpBefore that came back short: the
	// recent output ends here, and there is a history that could be asked
	// for. It is an offer for the user, not something to act on.
	History bool
	Err     error
}

// Feed connects one console to one source through two one-way lanes: Data
// brings deliveries down, Req takes requests up. Deliveries arrive in the
// order the source produced them.
//
// A feed is born paused. The console starts it with an OpFollow request,
// and resumes it the same way after a pause: it does not catch up with
// what it missed, it jumps to the end. A console keeps only what the user
// can see; anything older is asked for again by position.
//
// The console must keep reading Data: requests are answered there, and Req
// has a small buffer that fills up if nobody listens to the answers. Data
// is closed when the feed ends.
type Feed struct {
	Data <-chan Delivery
	Req  chan<- Request

	closeFn func()
	once    sync.Once
}

// NewFeed builds a feed over the two lanes. It is meant for implementers
// of Source; closeFn is called once, the first time the feed is closed.
func NewFeed(data <-chan Delivery, req chan<- Request, closeFn func()) *Feed {
	return &Feed{Data: data, Req: req, closeFn: closeFn}
}

// Close ends the feed. It is safe to call more than once.
func (f *Feed) Close() {
	f.once.Do(func() {
		if f.closeFn != nil {
			f.closeFn()
		}
	})
}

// Source is what a console shows in a tab: something with a name that
// gives chunks by position and can open a live feed. The screen of a log
// implements it in the application's process, alone or joined with a
// stored-data provider; the wire client implements it at the other end
// of a connection. A console only cares about who sends it the chunks.
type Source interface {
	Provider
	// Name identifies the log. It is what a tab is titled with.
	Name() string
	// Subscribe opens a feed. buffer is how many live chunks may wait for
	// the console before the source pauses the feed by itself; zero or
	// less means a default.
	Subscribe(buffer int) *Feed
}
