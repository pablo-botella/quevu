// Package qui is the quevu window: a row of tabs the application fills as
// it sees fit, a menu that is the application's, and nothing else.
//
// A tab is one of two kinds. A source tab shows a log in a pseudo-console.
// A control tab shows a table of controls: each row has a light, a name,
// the text of its state and the buttons of its verbs. The application
// opens and closes tabs whenever it wants, names them and says where each
// goes by its weight.
//
// The window has no language and decides nothing. States, verbs, colors,
// menu entries and every word on the screen come from the application; a
// button pressed or a menu entry chosen runs a function of the
// application's, and so does the request to close the window.
//
//	win := qui.NewWindow("My application")
//	services := win.OpenControls("Services", qui.Weight(100))
//	web := services.Add("Web server")
//	web.Set(qui.State{Color: qui.Green, Text: "listening"})
//	win.OnClose(win.Quit)
//	err := win.Run()
//
// # One executable
//
// The window is compiled into the program that uses it. It is written in
// C against the native toolkit and built in by cgo: there is no library
// to ship next to the executable, and none to load.
//
// What that costs is what cgo costs: a C compiler where the program is
// built and CGO_ENABLED=1. On Windows that is gcc from MinGW-w64, and
// nothing is needed where the program runs. On macOS it is the compiler
// that comes with the command line tools of Xcode, and nothing either.
// On Linux it is gcc and the headers of GTK 3 (libgtk-3-dev); where the
// program runs, GTK 3 is loaded when the window is asked for, so the
// same executable starts on a machine with no desktop and says there is
// no window.
//
// The window exists for Windows, for Linux and for macOS; the macOS one
// is pending to test on a Mac. Elsewhere, in
// a build without cgo, on Linux with the quevu_nogui tag (which needs no
// GTK to build) and where there is no desktop to open a window on, the
// package still works and Available says there is no window: Run returns an error for which
// IsNoWindow is true, which is a normal situation and not a failure of
// the application. The same source serves a desktop and a server with no
// display.
//
// # The main thread
//
// The window must run on the main thread of the process (some systems
// allow no other, and it costs nothing elsewhere). Importing this package
// ties the main goroutine to that thread; call Run from main.
package qui

import (
	"errors"
	"runtime"
)

func init() {
	// An init function runs on the main thread. Locking here keeps the
	// main goroutine on it for good, which is where Run has to be.
	runtime.LockOSThread()
}

// Flags, as numbered in quevu_ui.h.
const (
	flagFull    = 1
	flagHistory = 2
)

// Attributes, as numbered in quevu_ui.h.
const (
	lightBlink  = 1
	lightHollow = 2

	verbDisabled = 1

	menuSeparator = 1
	menuChecked   = 2
	menuDisabled  = 4
	menuSubmenu   = 8
)

// The two places a menu entry hangs from that are not another entry.
const (
	menuBar  = 0
	menuTray = 0xffffffff
)

// Kinds of event, as numbered in quevu_ui.h.
const (
	evReady          = 1
	evConsoleRequest = 6
	evMenu           = 7
	evClose          = 8
	evConsoleFind    = 9
	evControlVerb    = 10
	evTabVerb        = 11
)

// uiVerb is a verb on its way to the window.
type uiVerb struct {
	id    uint32
	attrs uint32
	label string
}

// uiChunk is a chunk on its way to the window.
type uiChunk struct {
	seq    uint64
	timeNs int64
	data   []byte
}

// uiEvent is something the user did, as the window tells it.
type uiEvent struct {
	kind uint32
	id   uint32
	op   uint32
	n    uint32
	seq  uint64
}

// ErrNoWindow is what Run returns when this program has no window: it
// was built for a system the window does not exist for, or without cgo,
// or it runs where there is no display to open one on.
var ErrNoWindow = errors.New("qui: no window in this program")

// IsNoWindow reports whether err means that this program has no window,
// as opposed to a window that failed.
func IsNoWindow(err error) bool { return errors.Is(err, ErrNoWindow) }

// Available reports whether this program has a window to open where it
// is running: it was built with one and there is a desktop to open it on
// (on Linux, GTK 3 and a display). When it
// says no, everything else in the package still works and does nothing,
// and Run returns an error for which IsNoWindow is true.
func Available() bool { return uiAvailable() }
