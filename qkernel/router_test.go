package qkernel

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

type sink struct {
	mu   sync.Mutex
	msgs []string
	fail error
}

func (s *sink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return 0, s.fail
	}
	s.msgs = append(s.msgs, string(p))
	return len(p), nil
}

func (s *sink) got() string { s.mu.Lock(); defer s.mu.Unlock(); return strings.Join(s.msgs, "") }

func TestEveryMessageReachesItsChannel(t *testing.T) {
	general, mail, pay := &sink{}, &sink{}, &sink{}
	r := NewLogRouter(general)
	if err := r.Route(mail, "mailer"); err != nil {
		t.Fatal(err)
	}
	if err := r.Route(pay, "payments", "pay_worker", "pay2"); err != nil {
		t.Fatal(err)
	}
	l := log.New(r, "", 0)
	l.Printf("mailer: sent %d", 3)
	l.Printf("mailer queue: 2 left")
	l.Printf("payments: declined")
	l.Printf("pay_worker: order 7")
	l.Printf("pay2: x")
	l.Printf("mailers: not the mailer")
	l.Printf("Mailer: capital")
	l.Printf("Error: mailer: in the middle")
	l.Printf("mailer")    // the word alone
	l.Printf(": nothing") // no word
	l.Printf("payments: two\nlines\nof it")
	l.Printf("")

	if got, want := mail.got(), "mailer: sent 3\nmailer queue: 2 left\n"; got != want {
		t.Errorf("mailer got %q", got)
	}
	if got, want := pay.got(), "payments: declined\npay_worker: order 7\npay2: x\npayments: two\nlines\nof it\n"; got != want {
		t.Errorf("payments got %q", got)
	}
	if got, want := general.got(), "mailers: not the mailer\nMailer: capital\nError: mailer: in the middle\nmailer\n: nothing\n\n"; got != want {
		t.Errorf("general got %q", got)
	}
}

func TestWordsAreChecked(t *testing.T) {
	r := NewLogRouter(nil)
	for _, bad := range []string{"", "Mailer", "my-app", "a b", "a:", strings.Repeat("x", MaxRouteWord+1)} {
		var e *BadWordError
		if err := r.Route(&sink{}, "fine", bad); !errors.As(err, &e) || e.Word != bad {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// A refused call routes nothing, not even its good words.
	s := &sink{}
	r.Route(s, "ok", "Not")
	r.Write([]byte("ok: x\n"))
	if s.got() != "" {
		t.Error("a refused call routed part of itself")
	}
	if err := r.Route(s, strings.Repeat("x", MaxRouteWord)); err != nil {
		t.Error("the longest word:", err)
	}
	r.Write([]byte(strings.Repeat("x", MaxRouteWord) + ": long\n"))
	if !strings.HasSuffix(s.got(), ": long\n") {
		t.Error("the longest word does not route")
	}
	// No general writer: what has no channel is dropped, not an error.
	if n, err := r.Write([]byte("nobody: home\n")); n != 13 || err != nil {
		t.Errorf("%d %v", n, err)
	}
}

func TestTeeKeepsTheConsoleAsItWas(t *testing.T) {
	general, mail := &sink{}, &sink{}
	var console bytes.Buffer
	r := NewLogRouter(general)
	r.Route(mail, "mailer")
	r.now = func() time.Time { return time.Date(2025, 3, 9, 14, 5, 6, 0, time.UTC) }
	r.Tee(&console, StdLayout)
	l := log.New(r, "", 0)
	l.Printf("mailer: sent")
	l.Printf("started")
	if got, want := console.String(), "2025/03/09 14:05:06 mailer: sent\n2025/03/09 14:05:06 started\n"; got != want {
		t.Errorf("console %q", got)
	}
	// The channels get the message bare: each stamps its own time.
	if mail.got() != "mailer: sent\n" || general.got() != "started\n" {
		t.Errorf("channels %q %q", mail.got(), general.got())
	}
	console.Reset()
	r.Tee(&console, "")
	l.Printf("plain")
	r.Tee(nil, "")
	l.Printf("nothing")
	if console.String() != "plain\n" {
		t.Errorf("console %q", console.String())
	}
	// A channel that fails does not silence the console, and the logger
	// goes on.
	mail.fail = errors.New("disk full")
	console.Reset()
	r.Tee(&console, "")
	if _, err := r.Write([]byte("mailer: lost\n")); err == nil {
		t.Error("a failing channel was not reported")
	}
	l.Printf("still here")
	if console.String() != "mailer: lost\nstill here\n" {
		t.Errorf("console %q", console.String())
	}
}

func TestInstallAndPackageLoggers(t *testing.T) {
	var before bytes.Buffer
	log.SetOutput(&before)
	log.SetFlags(log.Lshortfile)
	defer func() { log.SetOutput(nil); log.SetFlags(log.LstdFlags) }()

	billing := Logger("billing") // made before any router exists
	general, bill := &sink{}, &sink{}
	r := NewLogRouter(general)
	r.Route(bill, "billing")
	restore := r.Install()
	log.Printf("hello")
	billing.Printf("invoice %d", 9)
	billing.Println("done")
	restore()
	restore() // twice is once
	log.Printf("after")
	billing.Printf("late")

	if general.got() != "hello\n" || bill.got() != "billing: invoice 9\nbilling: done\n" {
		t.Errorf("routed %q %q", general.got(), bill.got())
	}
	if got := before.String(); !strings.Contains(got, "router_test.go") || !strings.Contains(got, "after") || !strings.Contains(got, "billing: late") || strings.Contains(got, "hello") {
		t.Errorf("after restoring: %q", got)
	}
}

func TestManyAtOnce(t *testing.T) {
	a, b, g := &sink{}, &sink{}, &sink{}
	r := NewLogRouter(g)
	r.Route(a, "a")
	var console bytes.Buffer
	r.Tee(&console, "")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				switch j % 3 {
				case 0:
					fmt.Fprintf(r, "a: %d %d\n", i, j)
				case 1:
					fmt.Fprintf(r, "b: %d %d\n", i, j)
				default:
					fmt.Fprintf(r, "other %d %d\n", i, j)
				}
				if j == 100 && i == 0 {
					r.Route(b, "b")
				}
			}
		}(i)
	}
	wg.Wait()
	na, nb, ng := strings.Count(a.got(), "\n"), strings.Count(b.got(), "\n"), strings.Count(g.got(), "\n")
	if na != 8*67 || na+nb+ng != 1600 || strings.Count(console.String(), "\n") != 1600 {
		t.Errorf("a %d, b %d, general %d, console %d", na, nb, ng, strings.Count(console.String(), "\n"))
	}
	// No line of the console is two messages run together.
	for _, line := range strings.Split(strings.TrimSpace(console.String()), "\n") {
		if strings.Count(line, " ") != 2 {
			t.Fatalf("a torn line: %q", line)
		}
	}
}
