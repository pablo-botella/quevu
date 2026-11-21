package quevu

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// memWriter records what it receives, as slowly as told.
type memWriter struct {
	mu     sync.Mutex
	delay  time.Duration
	gate   chan struct{}
	got    []Chunk
	ended  bool
	last   uint64
	quitAt int   // give up after this many chunks, when positive
	quit   error // and return this
}

func (d *memWriter) Run(in <-chan Chunk) error {
	for c := range in {
		if d.gate != nil {
			<-d.gate
		}
		time.Sleep(d.delay)
		d.mu.Lock()
		d.got = append(d.got, c)
		n := len(d.got)
		d.mu.Unlock()
		if d.quitAt > 0 && n >= d.quitAt {
			return d.quit
		}
	}
	d.mu.Lock()
	d.ended = true
	d.mu.Unlock()
	return nil
}

func (d *memWriter) LastSeq() (uint64, error) { return d.last, nil }

func (d *memWriter) all() []Chunk {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Chunk(nil), d.got...)
}

func texts(cs []Chunk) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = string(c.Data)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWhatIsSentArrivesWithItsPosition(t *testing.T) {
	w := &memWriter{}
	h, _ := NewQHub("t", WithWriter(w))
	for _, s := range []string{"one", "two", "three"} {
		h.In() <- Entry{Data: []byte(s)}
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	got := w.all()
	if !equal(texts(got), []string{"one", "two", "three"}) {
		t.Fatalf("got %q", texts(got))
	}
	for i, c := range got {
		if c.Seq != uint64(i+1) || c.Time.IsZero() {
			t.Fatalf("chunk %d: %+v", i, c)
		}
	}
	if h.Name() != "t" || h.Seq() != 3 {
		t.Fatalf("name %q seq %d", h.Name(), h.Seq())
	}
	if !w.ended {
		t.Fatal("the channel of the writer was not closed")
	}
}

func TestAnEntryKeepsTheTimeItBrings(t *testing.T) {
	w := &memWriter{}
	h, _ := NewQHub("t", WithWriter(w))
	then := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	h.In() <- Entry{Data: []byte("now")}
	h.In() <- Entry{Data: []byte("an hour ago, elsewhere"), Time: then}
	h.Close()
	got := w.all()
	if len(got) != 2 || !got[1].Time.Equal(then) || got[0].Time.Equal(then) || got[1].Seq != 2 {
		t.Fatalf("chunks %+v", got)
	}
}

func TestAnInletIsAWayIn(t *testing.T) {
	w := &memWriter{}
	h, _ := NewQHub("t", WithWriter(w))
	in := NewInlet(h)
	fmt.Fprint(in, "one\ntwo\r\nthr")
	fmt.Fprint(in, "ee\n\nlast")
	in.Flush()
	h.Close()
	if got, want := texts(w.all()), []string{"one", "two", "three", "", "last"}; !equal(got, want) {
		t.Fatalf("got %q", got)
	}
	if _, err := in.Write([]byte("x\n")); !errors.Is(err, ErrClosed) {
		t.Fatalf("got %v", err)
	}
	select {
	case <-h.Done():
	default:
		t.Fatal("a closed hub does not say it is done")
	}
}

func TestAHubWithNobodyListening(t *testing.T) {
	h, err := NewQHub("t")
	if err != nil {
		t.Fatal(err)
	}
	h.In() <- Entry{Data: []byte("into the void")}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if h.Seq() != 1 || len(h.Stats()) != 0 {
		t.Fatalf("seq %d stats %v", h.Seq(), h.Stats())
	}
	if err := h.Close(); err != nil {
		t.Fatal("closing twice")
	}
}

func TestSeveralProducersOneOrder(t *testing.T) {
	w := &memWriter{}
	h, _ := NewQHub("t", WithWriterOptions(w, WriterOptions{Buffer: 16}), WithInput(8))
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := NewInlet(h)
			for i := 0; i < 250; i++ {
				fmt.Fprintf(in, "g%d-%d\n", g, i)
			}
		}()
	}
	wg.Wait()
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	got := w.all()
	if len(got) != 1000 {
		t.Fatalf("took %d", len(got))
	}
	next := [4]int{}
	for i, c := range got {
		if c.Seq != uint64(i+1) {
			t.Fatalf("position %d holds seq %d", i, c.Seq)
		}
		var g, n int
		fmt.Sscanf(string(c.Data), "g%d-%d", &g, &n)
		if n != next[g] {
			t.Fatalf("producer %d out of order: %q", g, c.Data)
		}
		next[g]++
	}
	st := h.Stats()[0]
	if st.Written != 1000 || st.Skipped != 0 || st.Queued != 0 || st.Capacity != 16 {
		t.Fatalf("stats %+v", st)
	}
}

func TestPositionsContinueAfterTheWriters(t *testing.T) {
	a, b := &memWriter{last: 41}, &memWriter{last: 7}
	h, _ := NewQHub("t", WithWriter(a), WithWriter(b))
	h.In() <- Entry{Data: []byte("x")}
	h.Close()
	if got := a.all(); len(got) != 1 || got[0].Seq != 42 {
		t.Fatalf("got %+v", got)
	}
}

func TestEveryWriterGetsEverything(t *testing.T) {
	a, b, c := &memWriter{}, &memWriter{delay: 200 * time.Microsecond}, &memWriter{}
	h, _ := NewQHub("t", WithWriter(a), WithWriterOptions(b, WriterOptions{Buffer: 4}), WithWriter(c))
	for i := 0; i < 200; i++ {
		h.In() <- Entry{Data: []byte(fmt.Sprintf("l%d", i))}
	}
	h.Close()
	for i, w := range []*memWriter{a, b, c} {
		got := w.all()
		if len(got) != 200 {
			t.Fatalf("writer %d took %d", i, len(got))
		}
		for j, ch := range got {
			if ch.Seq != uint64(j+1) {
				t.Fatalf("writer %d position %d holds %d", i, j, ch.Seq)
			}
		}
	}
}

func TestASlowWriterWithRoomDoesNotHoldAnother(t *testing.T) {
	gate := make(chan struct{})
	slow := &memWriter{gate: gate}
	fast := &memWriter{}
	h, _ := NewQHub("t", WithWriterOptions(slow, WriterOptions{Buffer: 10000}), WithWriter(fast))
	for i := 0; i < 100; i++ {
		h.In() <- Entry{Data: []byte("l")}
	}
	// The fast one is served while the slow one is stuck on its first
	// chunk: there is room in the slow one's channel.
	waitFor(t, "the fast writer", func() bool { return len(fast.all()) == 100 })
	if n := len(slow.all()); n != 0 {
		t.Fatalf("slow writer took %d while gated", n)
	}
	if s := h.Stuffed(); s != "" {
		t.Fatalf("the hub says it waits for %q", s)
	}
	close(gate)
	h.Close()
	if n := len(slow.all()); n != 100 {
		t.Fatalf("slow writer took %d", n)
	}
}

func TestAtTheLimitAVolatileWriterIsSkipped(t *testing.T) {
	gate := make(chan struct{})
	vol := &memWriter{gate: gate}
	other := &memWriter{}
	h, _ := NewQHub("t",
		WithWriterOptions(vol, WriterOptions{Name: "screen", Buffer: 4, OnFull: Skip}),
		WithWriter(other))
	for i := 0; i < 100; i++ {
		h.In() <- Entry{Data: []byte("l")}
	}
	// Nothing is held: the other writer gets everything.
	waitFor(t, "the other writer", func() bool { return len(other.all()) == 100 })
	st := h.Stats()[0]
	if st.Name != "screen" || st.Skipped == 0 || st.Queued > st.Capacity || st.Waits != 0 {
		t.Fatalf("stats %+v", st)
	}
	close(gate)
	h.Close()
	st = h.Stats()[0]
	if st.Written+st.Skipped != 100 || uint64(len(vol.all())) != st.Written {
		t.Fatalf("stats %+v, took %d", st, len(vol.all()))
	}
	// What it did get is in order, with gaps.
	prev := uint64(0)
	for _, c := range vol.all() {
		if c.Seq <= prev {
			t.Fatalf("seq %d after %d", c.Seq, prev)
		}
		prev = c.Seq
	}
}

func TestAtTheLimitAPersistentWriterHoldsEverythingAndIsNamed(t *testing.T) {
	gate := make(chan struct{})
	disk := &memWriter{gate: gate}
	other := &memWriter{}
	h, _ := NewQHub("t",
		WithWriterOptions(disk, WriterOptions{Name: "disk", Buffer: 2}),
		WithWriterOptions(other, WriterOptions{Name: "other"}),
		WithInput(4))
	in := NewInlet(h)

	// Enough to fill the writer's channel, the chunk it holds, the one
	// the hub is trying to send and the input: the next send must wait.
	sent := make(chan int, 1)
	go func() {
		n := 0
		for ; n < 50; n++ {
			if in.Append([]byte("l")) != nil {
				break
			}
		}
		sent <- n
	}()
	waitFor(t, "the hub to be held", func() bool { return h.Stuffed() == "disk" })
	st := h.Stats()
	if !st[0].Waiting || st[0].Waits == 0 || st[1].Waiting {
		t.Fatalf("stats %+v", st)
	}
	// The other writer is held back too: it has only what went out
	// before the hub stopped.
	if n := len(other.all()); n >= 50 {
		t.Fatalf("the other writer got all %d while the hub was held", n)
	}
	select {
	case n := <-sent:
		t.Fatalf("the producer was not held: it sent %d", n)
	case <-time.After(20 * time.Millisecond):
	}

	close(gate)
	if n := <-sent; n != 50 {
		t.Fatalf("sent %d", n)
	}
	h.Close()
	if len(disk.all()) != 50 || len(other.all()) != 50 {
		t.Fatalf("disk %d other %d", len(disk.all()), len(other.all()))
	}
	if h.Stuffed() != "" {
		t.Fatal("still held after closing")
	}
	if st := h.Stats()[0]; st.Skipped != 0 || st.Written != 50 {
		t.Fatalf("stats %+v", st)
	}
}

func TestAWriterThatGivesUpDoesNotHoldTheHub(t *testing.T) {
	boom := errors.New("disk on fire")
	bad := &memWriter{quitAt: 2, quit: boom}
	good := &memWriter{}
	h, _ := NewQHub("t", WithWriterOptions(bad, WriterOptions{Name: "bad", Buffer: 1}), WithWriter(good))
	for i := 0; i < 40; i++ {
		h.In() <- Entry{Data: []byte("l")}
	}
	err := h.Close()
	if !errors.Is(err, boom) {
		t.Fatalf("close returned %v", err)
	}
	if len(good.all()) != 40 {
		t.Fatalf("the good writer took %d", len(good.all()))
	}
	st := h.Stats()[0]
	if st.Failed == 0 || !errors.Is(st.LastErr, boom) || st.Written+st.Failed != 40 {
		t.Fatalf("stats %+v", st)
	}
}

func TestFailedBatchesShowWhileItRuns(t *testing.T) {
	boom := errors.New("no space left")
	h, _ := NewQHub("t", WithWriter(Batches(failing{boom}, 1)))
	h.In() <- Entry{Data: []byte("a")}
	h.In() <- Entry{Data: []byte("b")}
	waitFor(t, "the failures to show", func() bool {
		st := h.Stats()[0]
		return st.Failed == 2 && errors.Is(st.LastErr, boom)
	})
	if err := h.Close(); !errors.Is(err, boom) {
		t.Fatalf("close returned %v", err)
	}
}

type failing struct{ err error }

func (f failing) Write([]Chunk) error { return f.err }
func (f failing) Close() error        { return nil }

func TestWhatWasAlreadySentGoesOutOnClose(t *testing.T) {
	gate := make(chan struct{})
	w := &memWriter{gate: gate}
	h, _ := NewQHub("t", WithWriterOptions(w, WriterOptions{Buffer: 1}), WithInput(64))
	for i := 0; i < 30; i++ {
		h.In() <- Entry{Data: []byte("l")}
	}
	done := make(chan error, 1)
	go func() { done <- h.Close() }()
	select {
	case <-done:
		t.Fatal("closed with chunks still to deliver")
	case <-time.After(20 * time.Millisecond):
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := len(w.all()); n != 30 {
		t.Fatalf("took %d", n)
	}
}
