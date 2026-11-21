package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pablo-botella/quevu"
)

func TestMissingLibraryIsNotAFailure(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nothing-here"))
	if err == nil || !IsNoLibrary(err) {
		t.Fatalf("got %v", err)
	}
}

func TestAFileThatIsNotTheLibrary(t *testing.T) {
	path := filepath.Join(t.TempDir(), libName)
	os.WriteFile(path, []byte("not a library"), 0o644)
	if _, err := Load(path); err == nil {
		t.Fatal("loaded garbage")
	}
}

func TestFormatStats(t *testing.T) {
	got := formatStats([]quevu.WriterStats{
		{Queued: 3, Capacity: 4096, Written: 120},
		{Name: "disk", Queued: 4096, Capacity: 4096, Written: 7, Waiting: true, Waits: 3, Skipped: 2, Failed: 5, LastErr: errors.New("disk full")},
	})
	want := "writer 1: 3/4096, taken 120; disk: 4096/4096, taken 7, HOLDING THE HUB, waited for 3 times, skipped 2, failed 5 (disk full)"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if formatStats(nil) != "" {
		t.Fatal("no writers should be no note")
	}
}

// fakeSource records the requests a console sends up its feed and answers
// the first follow.
type fakeSource struct {
	mu   sync.Mutex
	reqs []quevu.Request
	got  chan quevu.Request
}

func (s *fakeSource) Name() string                              { return "out" }
func (s *fakeSource) Tail(int) ([]quevu.Chunk, error)           { return nil, nil }
func (s *fakeSource) Before(uint64, int) ([]quevu.Chunk, error) { return nil, nil }
func (s *fakeSource) After(uint64, int) ([]quevu.Chunk, error)  { return nil, nil }
func (s *fakeSource) Stats() []quevu.WriterStats                { return nil }
func (s *fakeSource) Subscribe(int) *quevu.Feed {
	data := make(chan quevu.Delivery)
	req := make(chan quevu.Request, 16)
	done := make(chan struct{})
	go func() {
		defer close(data)
		for {
			select {
			case <-done:
				return
			case r := <-req:
				s.got <- r
				d := quevu.Delivery{Op: r.Op}
				if r.Op == quevu.OpFollow {
					d.Chunks = []quevu.Chunk{
						{Seq: 1, Time: time.Now(), Data: []byte("first line")},
						{Seq: 2, Time: time.Now(), Data: nil},
						{Seq: 3, Time: time.Now(), Data: []byte("tab\there \x1b[31mred\x1b[0m \xff")},
					}
				}
				select {
				case data <- d:
				case <-done:
					return
				}
			}
		}
	}()
	return quevu.NewFeed(data, req, func() { close(done) })
}

// library finds a built quevu-ui library, or skips the test.
func library(t *testing.T) *Library {
	t.Helper()
	paths := []string{os.Getenv("QUEVU_UI_LIB"), filepath.Join("..", "..", "quevu-ui", "target", "release", libName)}
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			abs, _ := filepath.Abs(p)
			lib, err := Load(abs)
			if err != nil {
				t.Fatal(err)
			}
			return lib
		}
	}
	t.Skip("no quevu-ui library built; set QUEVU_UI_LIB to run the window tests")
	return nil
}

// The real library, a real window, for a moment: the console asks for its
// first page through the event lane, and the host closes the window.
func TestWindowRoundTrip(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only the Win32 backend can run off the main thread")
	}
	lib := library(t)
	if !strings.HasSuffix(lib.Path(), libName) {
		t.Fatalf("path %q", lib.Path())
	}

	src := &fakeSource{got: make(chan quevu.Request, 16)}
	panel := quevu.New()
	panel.AddSource(src)
	started := make(chan struct{})
	panel.Add("a", quevu.Func(func(context.Context) error { close(started); return nil }, nil))
	panel.Add("b", quevu.Func(func(context.Context) error { return errors.New("broken") }, nil))

	closed := make(chan struct{})
	ran := make(chan error, 1)
	go func() {
		// A window lives and dies on one thread.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		ran <- lib.Run("ui test", panel, OnClose(func() { close(closed) }), WithStats(0))
	}()

	select {
	case r := <-src.got:
		if r.Op != quevu.OpFollow || r.N <= 0 {
			t.Fatalf("the console opened with %+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the console never asked for its first page")
	}
	// State changes reach the window without blocking anybody.
	if err := panel.Start(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	<-started
	panel.Start(context.Background(), "b")

	lib.Quit()
	select {
	case err := <-ran:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the window did not close")
	}
	select {
	case <-closed:
		t.Fatal("a quit from the host must not run the user's close handler")
	default:
	}
}
