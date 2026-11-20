package quevu

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeInput is the input end of a hub that is not there: a channel and a
// way to say it is over.
type fakeInput struct {
	in   chan Entry
	done chan struct{}
}

func newFakeInput(buffer int) *fakeInput {
	return &fakeInput{in: make(chan Entry, buffer), done: make(chan struct{})}
}

func (f *fakeInput) In() chan<- Entry      { return f.in }
func (f *fakeInput) Done() <-chan struct{} { return f.done }

func (f *fakeInput) texts() []string {
	var out []string
	for {
		select {
		case e := <-f.in:
			out = append(out, string(e.Data))
		default:
			return out
		}
	}
}

func same(a, b []string) bool {
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

func TestInletSplitsLines(t *testing.T) {
	f := newFakeInput(16)
	in := NewInlet(f)
	fmt.Fprint(in, "one\ntwo\r\nthr")
	fmt.Fprint(in, "ee\n\nlast")
	if got, want := f.texts(), []string{"one", "two", "three", ""}; !same(got, want) {
		t.Fatalf("got %q", got)
	}
	// The unfinished line waits for Flush.
	if err := in.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := f.texts(); !same(got, []string{"last"}) {
		t.Fatalf("got %q", got)
	}
	if err := in.Flush(); err != nil || len(f.texts()) != 0 {
		t.Fatal("a second flush has nothing to send")
	}
}

func TestInletAppendKeepsDataWholeAndItsTime(t *testing.T) {
	f := newFakeInput(4)
	in := NewInlet(f)
	data := []byte("two\nlines")
	if err := in.Append(data); err != nil {
		t.Fatal(err)
	}
	data[0] = 'X' // the caller's buffer is its own again
	then := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := in.AppendAt([]byte("earlier"), then); err != nil {
		t.Fatal(err)
	}
	a, b := <-f.in, <-f.in
	if string(a.Data) != "two\nlines" || !a.Time.IsZero() {
		t.Fatalf("first %q %v", a.Data, a.Time)
	}
	if string(b.Data) != "earlier" || !b.Time.Equal(then) {
		t.Fatalf("second %q %v", b.Data, b.Time)
	}
}

func TestInletRefusesAClosedHub(t *testing.T) {
	f := newFakeInput(1)
	in := NewInlet(f)
	close(f.done)
	if _, err := in.Write([]byte("x\n")); !errors.Is(err, ErrClosed) {
		t.Fatalf("write: %v", err)
	}
	if err := in.Append([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("append: %v", err)
	}
	if len(f.texts()) != 0 {
		t.Fatal("something was sent to a closed hub")
	}
}

func TestInletWaitingIsReleasedWhenTheHubCloses(t *testing.T) {
	f := newFakeInput(0) // nobody receives: a send waits
	in := NewInlet(f)
	got := make(chan error, 1)
	go func() { got <- in.Append([]byte("x")) }()
	select {
	case err := <-got:
		t.Fatalf("returned before the hub closed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(f.done)
	select {
	case err := <-got:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("still waiting after the hub closed")
	}
}

func TestInletFromSeveralGoroutinesKeepsLinesWhole(t *testing.T) {
	f := newFakeInput(4000)
	in := NewInlet(f)
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 250; i++ {
				fmt.Fprintf(in, "g%d-%d\n", g, i)
			}
		}()
	}
	wg.Wait()
	next := [4]int{}
	got := f.texts()
	if len(got) != 1000 {
		t.Fatalf("got %d lines", len(got))
	}
	for _, s := range got {
		var g, i int
		if _, err := fmt.Sscanf(s, "g%d-%d", &g, &i); err != nil || i != next[g] {
			t.Fatalf("line %q out of place (expected %d)", s, next[g])
		}
		next[g]++
	}
}

// memBatcher records the batches it is given.
type memBatcher struct {
	mu      sync.Mutex
	batches [][]Chunk
	closed  int
	fail    error
	failAt  int
	last    uint64
}

func (m *memBatcher) Write(b []Chunk) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil && len(m.batches) == m.failAt {
		m.batches = append(m.batches, nil)
		return m.fail
	}
	m.batches = append(m.batches, b)
	return nil
}

func (m *memBatcher) Close() error             { m.closed++; return nil }
func (m *memBatcher) LastSeq() (uint64, error) { return m.last, nil }

func TestDrainHandsEverythingOverInOrderAndCloses(t *testing.T) {
	in := make(chan Chunk, 100)
	for i := 1; i <= 100; i++ {
		in <- Chunk{Seq: uint64(i)}
	}
	close(in)
	m := &memBatcher{}
	var f Failures
	if err := f.Drain(in, 16, m); err != nil {
		t.Fatal(err)
	}
	seq := uint64(0)
	for _, b := range m.batches {
		if len(b) == 0 || len(b) > 16 {
			t.Fatalf("batch of %d", len(b))
		}
		for _, c := range b {
			seq++
			if c.Seq != seq {
				t.Fatalf("position %d holds %d", seq, c.Seq)
			}
		}
	}
	if seq != 100 || m.closed != 1 {
		t.Fatalf("took %d, closed %d", seq, m.closed)
	}
	if n, err := f.Failed(); n != 0 || err != nil {
		t.Fatalf("failed %d %v", n, err)
	}
}

func TestDrainDoesNotWaitToFillABatch(t *testing.T) {
	in := make(chan Chunk)
	m := &memBatcher{}
	done := make(chan error, 1)
	go func() {
		var f Failures
		done <- f.Drain(in, 100, m)
	}()
	in <- Chunk{Seq: 1}
	deadline := time.Now().Add(2 * time.Second)
	for {
		m.mu.Lock()
		n := len(m.batches)
		m.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a lone chunk was held back waiting for more")
		}
		time.Sleep(time.Millisecond)
	}
	close(in)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDrainCountsAFailedBatchAndGoesOn(t *testing.T) {
	boom := errors.New("disk on fire")
	in := make(chan Chunk, 3)
	m := &memBatcher{fail: boom, failAt: 0}
	w := Batches(m, 1)
	done := make(chan error, 1)
	go func() { done <- w.Run(in) }()
	in <- Chunk{Seq: 1}
	in <- Chunk{Seq: 2}
	in <- Chunk{Seq: 3}
	close(in)
	if err := <-done; !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if n, err := w.(FailCounter).Failed(); n != 1 || !errors.Is(err, boom) {
		t.Fatalf("failed %d %v", n, err)
	}
	if len(m.batches) != 3 || m.closed != 1 {
		t.Fatalf("batches %d closed %d", len(m.batches), m.closed)
	}
}

func TestBatchesPassesOnTheLastPosition(t *testing.T) {
	w := Batches(&memBatcher{last: 41}, 0)
	if n, err := w.(SeqWriter).LastSeq(); n != 41 || err != nil {
		t.Fatalf("got %d %v", n, err)
	}
}
