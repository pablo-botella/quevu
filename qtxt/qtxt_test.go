package qtxt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pablo-botella/quevu/qkernel"
)

func mk(from, to uint64, pad int) []qkernel.Chunk {
	var out []qkernel.Chunk
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for s := from; s <= to; s++ {
		out = append(out, qkernel.Chunk{
			Seq:  s,
			Time: t0.Add(time.Duration(s) * time.Millisecond),
			Data: []byte(fmt.Sprintf("line %d %s", s, strings.Repeat("x", pad))),
		})
	}
	return out
}

func seqs(cs []qkernel.Chunk) string {
	if len(cs) == 0 {
		return ""
	}
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = fmt.Sprint(c.Seq)
	}
	return strings.Join(parts, ",")
}

func TestRoundTripKeepsEverything(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.log")
	d, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	in := []qkernel.Chunk{
		{Seq: 1, Time: time.Date(2026, 5, 6, 7, 8, 9, 123456789, time.UTC), Data: []byte("plain")},
		{Seq: 2, Time: time.Now(), Data: []byte("tab\there, back\\slash, \r\n two lines\n")},
		{Seq: 3, Time: time.Now(), Data: nil},
		{Seq: 4, Time: time.Now(), Data: []byte("áéí ñ ü \x00\xff binary")},
	}
	if err := d.Write(in); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := NewReader(path).Tail(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("read %d chunks", len(out))
	}
	for i := range in {
		if out[i].Seq != in[i].Seq || string(out[i].Data) != string(in[i].Data) || !out[i].Time.Equal(in[i].Time) {
			t.Fatalf("chunk %d: got %+v want %+v", i, out[i], in[i])
		}
	}
	// One chunk, one line: the file stays readable with any tool.
	raw, _ := os.ReadFile(path)
	if n := strings.Count(string(raw), "\n"); n != len(in) {
		t.Fatalf("%d lines in the file", n)
	}
}

func TestMissingFileIsAnEmptyLog(t *testing.T) {
	r := NewReader(filepath.Join(t.TempDir(), "nope.log"))
	for _, call := range []func() ([]qkernel.Chunk, error){
		func() ([]qkernel.Chunk, error) { return r.Tail(5) },
		func() ([]qkernel.Chunk, error) { return r.Before(10, 5) },
		func() ([]qkernel.Chunk, error) { return r.After(0, 5) },
	} {
		got, err := call()
		if err != nil || len(got) != 0 {
			t.Fatalf("got %v, %v", got, err)
		}
	}
}

// A file far larger than the reading windows, so bisection and the
// growing backward window are really exercised.
func TestMovingByPositionInALargeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.log")
	d, _ := Open(path, Options{})
	const total = 20000
	for from := uint64(1); from <= total; from += 500 {
		if err := d.Write(mk(from, from+499, 200)); err != nil {
			t.Fatal(err)
		}
	}
	d.Close()
	if st, _ := os.Stat(path); st.Size() < 4<<20 {
		t.Fatalf("file too small to mean anything: %d", st.Size())
	}
	r := NewReader(path)

	cases := []struct {
		name string
		got  func() ([]qkernel.Chunk, error)
		want string
	}{
		{"tail", func() ([]qkernel.Chunk, error) { return r.Tail(3) }, "19998,19999,20000"},
		{"before middle", func() ([]qkernel.Chunk, error) { return r.Before(10000, 3) }, "9997,9998,9999"},
		{"before first", func() ([]qkernel.Chunk, error) { return r.Before(1, 3) }, ""},
		{"before second", func() ([]qkernel.Chunk, error) { return r.Before(2, 3) }, "1"},
		{"before past the end", func() ([]qkernel.Chunk, error) { return r.Before(999999, 2) }, "19999,20000"},
		{"after middle", func() ([]qkernel.Chunk, error) { return r.After(10000, 3) }, "10001,10002,10003"},
		{"after zero", func() ([]qkernel.Chunk, error) { return r.After(0, 2) }, "1,2"},
		{"after last", func() ([]qkernel.Chunk, error) { return r.After(20000, 3) }, ""},
		{"after near the end", func() ([]qkernel.Chunk, error) { return r.After(19998, 10) }, "19999,20000"},
	}
	for _, c := range cases {
		got, err := c.got()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if seqs(got) != c.want {
			t.Fatalf("%s: got %q want %q", c.name, seqs(got), c.want)
		}
	}

	// A request larger than one backward window.
	got, err := r.Before(15000, 2000)
	if err != nil || len(got) != 2000 || got[0].Seq != 13000 || got[1999].Seq != 14999 {
		t.Fatalf("wide before: %d chunks, err %v", len(got), err)
	}
	// Every position is reachable.
	for _, s := range []uint64{1, 2, 499, 500, 501, 7777, 19999, 20000} {
		got, err := r.After(s-1, 1)
		if err != nil || len(got) != 1 || got[0].Seq != s {
			t.Fatalf("seek %d: %q %v", s, seqs(got), err)
		}
	}
}

func TestPositionsContinueAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.log")
	d, _ := Open(path, Options{})
	if last, _ := d.LastSeq(); last != 0 {
		t.Fatalf("new file: last %d", last)
	}
	d.Write(mk(1, 7, 0))
	d.Close()
	d, _ = Open(path, Options{Sync: true})
	if last, _ := d.LastSeq(); last != 7 {
		t.Fatalf("reopened: last %d", last)
	}
	d.Write(mk(8, 9, 0))
	d.Close()
	got, _ := NewReader(path).Tail(100)
	if seqs(got) != "1,2,3,4,5,6,7,8,9" {
		t.Fatalf("got %q", seqs(got))
	}
}

func TestTornLastLineIsHealedAndSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.log")
	d, _ := Open(path, Options{})
	d.Write(mk(1, 3, 0))
	d.Close()
	// A process dies in the middle of a line.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("4\t2026-01-02T03:04:05Z\tcut he")
	f.Close()

	r := NewReader(path)
	// The unfinished tail is not served.
	if got, _ := r.Tail(10); seqs(got) != "1,2,3" {
		t.Fatalf("tail before healing: %q", seqs(got))
	}
	if got, _ := r.After(2, 10); seqs(got) != "3" {
		t.Fatalf("after before healing: %q", seqs(got))
	}

	d, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// The cut line happens to parse, so it counts as position 4: what
	// matters is that the next chunk starts on a line of its own.
	last, _ := d.LastSeq()
	d.Write(mk(last+1, last+2, 0))
	d.Close()
	got, _ := r.Tail(10)
	if seqs(got) != "1,2,3,4,5,6" {
		t.Fatalf("after healing: %q", seqs(got))
	}
	if string(got[3].Data) != "cut he" || string(got[4].Data) != "line 5 " {
		t.Fatalf("data: %q / %q", got[3].Data, got[4].Data)
	}
}

func TestDamagedLinesAreSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.log")
	d, _ := Open(path, Options{})
	d.Write(mk(1, 3, 0))
	d.Close()
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("this is not a log line\n\n")
	f.Close()
	d, _ = Open(path, Options{})
	if last, _ := d.LastSeq(); last != 3 {
		t.Fatalf("last %d", last)
	}
	d.Write(mk(4, 6, 0))
	d.Close()
	r := NewReader(path)
	if got, _ := r.Tail(4); seqs(got) != "3,4,5,6" {
		t.Fatalf("tail: %q", seqs(got))
	}
	if got, _ := r.After(2, 3); seqs(got) != "3,4,5" {
		t.Fatalf("after: %q", seqs(got))
	}
	if got, _ := r.Before(5, 3); seqs(got) != "2,3,4" {
		t.Fatalf("before: %q", seqs(got))
	}
}

func TestReaderSeesWhatADeviceIsWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.log")
	d, _ := Open(path, Options{})
	defer d.Close()
	r := NewReader(path)
	for s := uint64(1); s <= 50; s++ {
		d.Write(mk(s, s, 0))
		got, err := r.Tail(1)
		if err != nil || len(got) != 1 || got[0].Seq != s {
			t.Fatalf("at %d: %q %v", s, seqs(got), err)
		}
	}
}
