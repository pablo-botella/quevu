// Package ui shows a quevu panel in a window: the switches of the
// application (a global one and one per unit) and its screens (a tab per
// log, each a pseudo-console).
//
// The window itself is not written in Go. It lives in the quevu-ui
// dynamic library (quevu_ui.dll, libquevu_ui.so, libquevu_ui.dylib), and
// this package only loads it and speaks to it through its C interface,
// described in quevu_ui.h. Nothing here needs cgo: a program that imports
// this package still builds with a plain "go build", for any system.
//
// The visual part is optional by construction. If the library is not
// there, Load says so and the application carries on without a window:
// the same binary serves a desktop and a server with no display.
//
// The window talks to a quevu.Controller and nothing else, so it does not
// know, or care, whether the panel is in its own process or at the other
// end of a connection.
//
// # The main thread
//
// The window must run on the main thread of the process (macOS allows no
// other, and it costs nothing elsewhere). Importing this package ties the
// main goroutine to that thread; call Run from main.
package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ebitengine/purego"
)

func init() {
	// An init function runs on the main thread. Locking here keeps the
	// main goroutine on it for good, which is where Run has to be.
	runtime.LockOSThread()
}

// abi is the version of the C interface this package was written for.
const abi = 4

// Flags, as numbered in quevu_ui.h.
const (
	flagFull    = 1
	flagHistory = 2
)

// ErrNoLibrary is returned by Load when the library cannot be found or
// loaded: this machine has no visual part.
var ErrNoLibrary = errors.New("quevu/ui: quevu-ui library not available")

// cChunk mirrors quevu_ui_chunk.
type cChunk struct {
	seq    uint64
	timeNs int64
	data   *byte
	len    uint32
}

// cEvent mirrors quevu_ui_event.
type cEvent struct {
	kind uint32
	id   uint32
	op   uint32
	n    uint32
	seq  uint64
}

// Kinds of event, as numbered in quevu_ui.h.
const (
	evReady = iota + 1
	evUnitStart
	evUnitStop
	evStartAll
	evStopAll
	evConsoleRequest
	evMenu
	evClose
	evConsoleFind
)

// Library is the loaded quevu-ui library.
type Library struct {
	path string

	fnABI            func() uint32
	fnRun            func(title string) int32
	fnQuit           func()
	fnUnitAdd        func(name string) uint32
	fnUnitState      func(unit, state uint32, err string)
	fnConsoleAdd     func(title string) uint32
	fnConsoleDeliver func(tab, op, flags uint32, chunks *cChunk, n uint32)
	fnConsoleNote    func(tab uint32, note string)
	fnTabRemove      func(tab uint32)
	fnMenuAdd        func(label string) uint32
	fnTray           func(enabled uint32)
	fnShow           func(visible uint32)
	fnNextEvent      func(ev *cEvent, timeoutMs int32) int32
	fnFindText       func(tab uint32, buf *byte, size uint32) uint32
	fnFindRange      func(tab uint32, sinceNs, untilNs *int64)
	fnConsoleGoto    func(tab uint32, chunks *cChunk, n uint32, mark uint64, flags uint32)
	fnFindStatus     func(tab uint32, status string, resume uint64, flags uint32)
}

// Path returns where the library was loaded from.
func (l *Library) Path() string { return l.path }

// candidates lists where to look for the library, best first.
func candidates(path string) []string {
	if path != "" {
		return []string{path}
	}
	var out []string
	if p := os.Getenv("QUEVU_UI_LIB"); p != "" {
		out = append(out, p)
	}
	if exe, err := os.Executable(); err == nil {
		out = append(out, filepath.Join(filepath.Dir(exe), libName))
	}
	// Last, by name alone: the system's own search.
	return append(out, libName)
}

// Load loads the quevu-ui library. With an empty path it looks, in this
// order, at the QUEVU_UI_LIB environment variable, next to the
// executable, and wherever the system looks for libraries.
//
// An error that wraps ErrNoLibrary means there is no visual part here,
// which is a normal situation and not a failure of the application.
func Load(path string) (*Library, error) {
	var firstErr error
	for _, p := range candidates(path) {
		h, err := openLibrary(p)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		l := &Library{path: p}
		if err := l.bind(h); err != nil {
			return nil, err
		}
		if v := l.fnABI(); v != abi {
			return nil, fmt.Errorf("quevu/ui: %s speaks interface version %d, this package speaks %d", p, v, abi)
		}
		return l, nil
	}
	return nil, fmt.Errorf("%w: %v", ErrNoLibrary, firstErr)
}

// bind ties every function of the interface to its Go variable.
func (l *Library) bind(h uintptr) (err error) {
	defer func() {
		// A missing symbol means this is not the library, or not this
		// version of it.
		if r := recover(); r != nil {
			err = fmt.Errorf("quevu/ui: %s is not a usable quevu-ui library: %v", l.path, r)
		}
	}()
	for name, fn := range map[string]any{
		"quevu_ui_abi":                 &l.fnABI,
		"quevu_ui_run":                 &l.fnRun,
		"quevu_ui_quit":                &l.fnQuit,
		"quevu_ui_unit_add":            &l.fnUnitAdd,
		"quevu_ui_unit_state":          &l.fnUnitState,
		"quevu_ui_console_add":         &l.fnConsoleAdd,
		"quevu_ui_console_deliver":     &l.fnConsoleDeliver,
		"quevu_ui_console_note":        &l.fnConsoleNote,
		"quevu_ui_tab_remove":          &l.fnTabRemove,
		"quevu_ui_menu_add":            &l.fnMenuAdd,
		"quevu_ui_tray":                &l.fnTray,
		"quevu_ui_show":                &l.fnShow,
		"quevu_ui_next_event":          &l.fnNextEvent,
		"quevu_ui_console_find_text":   &l.fnFindText,
		"quevu_ui_console_find_range":  &l.fnFindRange,
		"quevu_ui_console_goto":        &l.fnConsoleGoto,
		"quevu_ui_console_find_status": &l.fnFindStatus,
	} {
		purego.RegisterLibFunc(fn, h, name)
	}
	return nil
}
