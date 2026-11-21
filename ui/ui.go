package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/pablo-botella/quevu"
)

// Option configures a window.
type Option func(*config)

type config struct {
	tray    bool
	onClose func()
	menu    []menuEntry
	stats   time.Duration
	budget  int
}

type menuEntry struct {
	label string
	fn    func()
}

// WithTray puts an icon in the notification area. While it is there,
// closing or minimizing the window hides it instead; the way out is the
// Exit entry of the menu. Where the system has no such area the window
// simply minimizes.
func WithTray() Option {
	return func(c *config) { c.tray = true }
}

// OnClose sets what happens when the user asks to close the application.
// The window stays open, showing the units change state, until fn
// returns; then it goes away and Run returns.
//
// The default stops every unit in order, which is what the window of an
// application should do. A controller attached to somebody else's
// service wants an fn that does nothing: closing the window must not
// stop the service.
func OnClose(fn func()) Option {
	return func(c *config) { c.onClose = fn }
}

// WithMenu adds an entry to the application menu. fn runs on a goroutine
// of its own. quevu has no dialogs: an application that needs one serves
// a page and opens it from an entry like this.
func WithMenu(label string, fn func()) Option {
	return func(c *config) { c.menu = append(c.menu, menuEntry{label, fn}) }
}

// WithStats sets how often the queues of each log are read and shown in
// the status strip of its console. Zero turns it off. Default one second.
func WithStats(every time.Duration) Option {
	return func(c *config) { c.stats = every }
}

// WithFindBudget sets how many chunks one step of a search may examine.
// A step that finds nothing within it says how far it got and stops;
// going further is the user's decision. The stored-data provider of a
// log may allow less. Default quevu.DefaultSearchBudget.
func WithFindBudget(chunks int) Option {
	return func(c *config) { c.budget = chunks }
}

// console is one tab: a feed and the goroutines on its two lanes.
type console struct {
	tab   uint32
	src   quevu.Source
	feed  *quevu.Feed
	reqs  chan quevu.Request
	finds chan find
}

// find is one step of a search asked for from a console.
type find struct {
	text  string
	from  uint64
	newer bool
	// history is set only when the user allowed the history in the
	// window: this package never sets it by itself.
	history bool
	// since and until are the time range the user set, if any.
	since, until time.Time
}

// Run opens the window over a panel and returns when it is gone. It must
// be called from the main goroutine, and at most once per process.
//
// The window is the face of c and decides nothing: a switch asks c to
// start or stop a unit, and a console asks its source for chunks.
func (l *Library) Run(title string, c quevu.Controller, opts ...Option) error {
	cfg := config{stats: time.Second}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.onClose == nil {
		cfg.onClose = func() { c.StopAll(context.Background()) }
	}

	// Everything below is queued by the library until its window exists.
	units := make(map[uint32]string)
	ids := make(map[string]uint32)
	for _, u := range c.Units() {
		id := l.fnUnitAdd(u.Name)
		units[id], ids[u.Name] = u.Name, id
		l.fnUnitState(id, uint32(u.State), u.Err)
	}
	unwatch := c.Watch(func(u quevu.UnitInfo) {
		if id, ok := ids[u.Name]; ok {
			l.fnUnitState(id, uint32(u.State), u.Err)
		}
	})
	defer unwatch()

	var wg sync.WaitGroup
	done := make(chan struct{})
	consoles := make(map[uint32]*console)
	for _, src := range c.Sources() {
		k := &console{
			tab:   l.fnConsoleAdd(src.Name()),
			src:   src,
			feed:  src.Subscribe(0),
			reqs:  make(chan quevu.Request, 64),
			finds: make(chan find, 1),
		}
		consoles[k.tab] = k
		wg.Add(3)
		go func() {
			defer wg.Done()
			l.finder(k, cfg.budget, done)
		}()
		go func() {
			defer wg.Done()
			l.down(k)
		}()
		go func() {
			defer wg.Done()
			l.up(k, done)
		}()
	}

	menu := make(map[uint32]func())
	for _, m := range cfg.menu {
		menu[l.fnMenuAdd(m.label)] = m.fn
	}
	if cfg.tray {
		l.fnTray(1)
	}

	if cfg.stats > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.stats(consoles, cfg.stats, done)
		}()
	}

	// The user's lane: what happens in the window is read here, never
	// called back from the window's own thread.
	wg.Add(1)
	go func() {
		defer wg.Done()
		l.events(c, units, consoles, menu, cfg.onClose)
	}()

	code := l.fnRun(title)

	close(done)
	for _, k := range consoles {
		k.feed.Close()
	}
	wg.Wait()
	switch code {
	case 0:
		return nil
	case -1:
		return fmt.Errorf("%w: it has no window for %s", ErrNoLibrary, runtime.GOOS)
	}
	return fmt.Errorf("quevu/ui: the window could not be created (code %d)", code)
}

// Quit closes the window from the host's side: Run returns. It may be
// called from any goroutine, and does nothing if no window is open. It is
// for the host that has to leave for reasons of its own, such as a
// controller whose application went away.
func (l *Library) Quit() { l.fnQuit() }

// events reads what the user does until the window is gone.
func (l *Library) events(c quevu.Controller, units map[uint32]string, consoles map[uint32]*console, menu map[uint32]func(), onClose func()) {
	var closing sync.Once
	for {
		var ev cEvent
		switch l.fnNextEvent(&ev, 500) {
		case -1:
			return
		case 0:
			continue
		}
		switch ev.kind {
		case evUnitStart:
			if name, ok := units[ev.id]; ok {
				// An error shows up as the state of the unit.
				go c.Start(context.Background(), name)
			}
		case evUnitStop:
			if name, ok := units[ev.id]; ok {
				go c.Stop(context.Background(), name)
			}
		case evStartAll:
			go c.StartAll(context.Background())
		case evStopAll:
			go c.StopAll(context.Background())
		case evConsoleRequest:
			if k, ok := consoles[ev.id]; ok {
				r := quevu.Request{Op: quevu.Op(ev.op), Seq: ev.seq, N: int(ev.n)}
				select {
				case k.reqs <- r:
				default:
					// A console that asks faster than it is answered
					// will ask again when the user moves.
				}
			}
		case evConsoleFind:
			if k, ok := consoles[ev.id]; ok {
				f := find{text: l.findText(ev.id), from: ev.seq, newer: ev.op&1 != 0, history: ev.op&2 != 0}
				f.since, f.until = l.findRange(ev.id)
				// Only the last thing asked for matters: a request still
				// waiting its turn is replaced, not queued behind.
				select {
				case <-k.finds:
				default:
				}
				select {
				case k.finds <- f:
				default:
				}
			}
		case evMenu:
			if fn, ok := menu[ev.id]; ok {
				go fn()
			}
		case evClose:
			closing.Do(func() {
				go func() {
					onClose()
					l.fnQuit()
				}()
			})
		}
	}
}

// findText reads the text a console is looking for.
func (l *Library) findText(tab uint32) string {
	buf := make([]byte, 1024)
	n := l.fnFindText(tab, &buf[0], uint32(len(buf)))
	return string(buf[:min(int(n), len(buf)-1)])
}

// findRange reads the time range a console is searching within. A zero
// time is no bound on that side.
func (l *Library) findRange(tab uint32) (since, until time.Time) {
	var s, u int64
	l.fnFindRange(tab, &s, &u)
	if s != 0 {
		since = time.Unix(0, s)
	}
	if u != 0 {
		until = time.Unix(0, u)
	}
	return since, until
}

// finder runs the searches of one console, one step at a time. The step
// is the source's to run: here it is only asked for, and its outcome
// shown.
func (l *Library) finder(k *console, budget int, done <-chan struct{}) {
	for {
		var f find
		select {
		case <-done:
			return
		case f = <-k.finds:
		}
		res, err := quevu.Search(k.src, quevu.Query{Text: f.text, From: f.from, Forward: f.newer, Budget: budget, History: f.history, Since: f.since, Until: f.until})
		if os.Getenv("QUEVU_UI_TRACE") != "" {
			fmt.Fprintf(os.Stderr, "find %q from=%d newer=%v history=%v range=[%s, %s] -> %d matches, reached=%d examined=%d done=%v out-of-range=%v offer=%v err=%v\n",
				f.text, f.from, f.newer, f.history, stamp(f.since), stamp(f.until),
				len(res.Matches), res.Reached, res.Examined, res.Done, res.OutOfRange, res.History, err)
		}
		switch {
		case err != nil:
			l.fnFindStatus(k.tab, "The search failed: "+err.Error(), 0, 0)
		case len(res.Matches) > 0:
			l.showFound(k, f, res.Matches[0])
		case res.History:
			// Recent output has no more of it. There is a history, and
			// looking into it is the user's decision, not this one's.
			l.fnFindStatus(k.tab, fmt.Sprintf("No %q in recent output (%d lines examined). The history has not been searched: press History to look there",
				f.text, res.Examined), res.Reached, flagHistory)
		default:
			l.fnFindStatus(k.tab, notFound(f, res), resumeFrom(res), 0)
		}
	}
}

// showFound moves the console to a match. Only the line found is handed
// over: the console asks its source for what surrounds it, the same way
// it does when the user scrolls, so this package never has to know which
// provider holds what.
func (l *Library) showFound(k *console, f find, m quevu.Chunk) {
	flags := uint32(0)
	if f.history {
		flags = flagHistory
	}
	one := []quevu.Chunk{m}
	cs := toC(one)
	l.fnConsoleGoto(k.tab, &cs[0], 1, m.Seq, flags)
	runtime.KeepAlive(one)
	runtime.KeepAlive(cs)
	l.fnFindStatus(k.tab, fmt.Sprintf("Found %q at %s. F3 looks for the next older one, Shift+F3 newer; End goes back to the live output",
		f.text, m.Time.Local().Format("2006-01-02 15:04:05")), 0, 0)
}

// stamp writes a bound of a time range for the trace.
func stamp(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// resumeFrom is where the next step starts if the user insists: nowhere
// when the step ran out of log.
func resumeFrom(res quevu.Result) uint64 {
	if res.Done {
		return 0
	}
	return res.Reached
}

// notFound says how far a step that found nothing got.
func notFound(f find, res quevu.Result) string {
	way, end, again := "back to", "the beginning of what is kept", "F3"
	if f.newer {
		way, end, again = "up to", "the end of the log", "Shift+F3"
	}
	if res.OutOfRange {
		return fmt.Sprintf("No %q within the time range (%d lines examined)", f.text, res.Examined)
	}
	if res.Done {
		return fmt.Sprintf("No %q: reached %s (%d lines examined)", f.text, end, res.Examined)
	}
	when := ""
	if res.ReachedTime != 0 {
		when = fmt.Sprintf(" %s %s", way, time.Unix(0, res.ReachedTime).Format("2006-01-02 15:04:05"))
	}
	return fmt.Sprintf("No %q in %d lines%s. %s keeps looking from there", f.text, res.Examined, when, again)
}

// up carries the requests of a console to its source.
func (l *Library) up(k *console, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case r := <-k.reqs:
			select {
			case k.feed.Req <- r:
			case <-done:
				return
			}
		}
	}
}

// down carries what the source delivers to the console.
func (l *Library) down(k *console) {
	for d := range k.feed.Data {
		if d.Err != nil {
			// The console must hear the answer either way, or it would
			// wait for it forever.
			d.Chunks = nil
		}
		l.deliver(k.tab, d)
	}
}

func (l *Library) deliver(tab uint32, d quevu.Delivery) {
	flags := uint32(0)
	if d.Full {
		flags |= flagFull
	}
	if d.History {
		// The source's offer: the window shows it, the user decides.
		flags |= flagHistory
	}
	if len(d.Chunks) == 0 {
		l.fnConsoleDeliver(tab, uint32(d.Op), flags, nil, 0)
		return
	}
	cs := toC(d.Chunks)
	l.fnConsoleDeliver(tab, uint32(d.Op), flags, &cs[0], uint32(len(cs)))
	// The library copies before returning; until then the data must
	// stay where it is.
	runtime.KeepAlive(d.Chunks)
	runtime.KeepAlive(cs)
}

// toC lays chunks out the way the library reads them. The data is not
// copied: the chunks must stay alive until the call that uses the result
// returns.
func toC(chunks []quevu.Chunk) []cChunk {
	cs := make([]cChunk, len(chunks))
	for i, c := range chunks {
		cs[i] = cChunk{seq: c.Seq, timeNs: c.Time.UnixNano(), len: uint32(len(c.Data))}
		if len(c.Data) > 0 {
			cs[i].data = &c.Data[0]
		}
	}
	return cs
}

// stats shows the queues of each log next to its console. A queue is
// there to absorb bursts; one that keeps growing says the writer
// behind it is too slow for this output.
func (l *Library) stats(consoles map[uint32]*console, every time.Duration, done <-chan struct{}) {
	t := time.NewTicker(every)
	defer t.Stop()
	last := make(map[uint32]string)
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		for tab, k := range consoles {
			ss, ok := k.src.(quevu.StatSource)
			if !ok {
				continue
			}
			note := formatStats(ss.Stats())
			if note != last[tab] {
				last[tab] = note
				l.fnConsoleNote(tab, note)
			}
		}
	}
}

func formatStats(stats []quevu.WriterStats) string {
	parts := make([]string, 0, len(stats))
	for i, s := range stats {
		name := s.Name
		if name == "" {
			name = fmt.Sprintf("writer %d", i+1)
		}
		p := fmt.Sprintf("%s: %d/%d, taken %d", name, s.Queued, s.Capacity, s.Written)
		if s.Waiting {
			p += ", HOLDING THE HUB"
		}
		if s.Waits > 0 {
			p += fmt.Sprintf(", waited for %d times", s.Waits)
		}
		if s.Skipped > 0 {
			p += fmt.Sprintf(", skipped %d", s.Skipped)
		}
		if s.Failed > 0 {
			p += fmt.Sprintf(", failed %d", s.Failed)
		}
		if s.LastErr != nil {
			p += " (" + s.LastErr.Error() + ")"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "; ")
}

// IsNoLibrary reports whether err means that this machine has no visual
// part, as opposed to a visual part that failed.
func IsNoLibrary(err error) bool { return errors.Is(err, ErrNoLibrary) }
