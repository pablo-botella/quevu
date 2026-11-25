---
mkskill:
  pos: 130
  in: "*"
---

## The window, from Go: `quevu/qui`

```
import "github.com/pablo-botella/quevu/qui"
```

Package `qui` is the quevu window. It is compiled into the program that
uses it: Go on top and C underneath, against the native toolkit, joined
by cgo. An application ships as one executable, with no library next to
it and none to load.

It is a package of its own, and not part of package `qkernel`, for two
reasons: building it needs a C compiler, and importing it ties the main
goroutine to the main thread of the process. A program with no window
imports `qkernel` alone and pays for none of this: only who imports
`quevu/qui` needs cgo.

The window is a row of tabs that the application fills, and a menu that
is the application's. It has no language and decides nothing: no tab is
fixed, every word on it comes from the application, and what the user
does (a button pressed, a menu entry chosen, the request to close) runs a
function of the application and changes nothing by itself.

```go
func main() {
	w := qui.NewWindow("my application")

	// A tab that shows a log.
	w.OpenSource(qkernel.Join(screen, qkernel.Roles{}), qui.Weight(10))

	// A tab with a table of controls.
	services := w.OpenControls("Services", qui.Weight(100))
	web := services.Add("Web server")
	web.Set(qui.State{Color: qui.Green, Text: "listening on :8443"})
	mail := services.Add("Mail")
	mail.Set(qui.State{
		Color: qui.Gray, Hollow: true, Text: "stopped",
		Verbs: []qui.Verb{{Label: "Start", Do: startMail}},
	})

	// The menu, and what closing means.
	quit := w.Menu("Quit")
	quit.Add("Normal", func() { stopEverything(); w.Quit() })
	quit.Add("Forced", w.Quit)
	w.OnClose(func() { stopEverything(); w.Quit() })

	err := w.Run() // blocks until Quit
	if qui.IsNoWindow(err) {
		// A build with no window: the same source runs headless.
	}
}
```

### Building

`CGO_ENABLED=1`, the default when a C compiler is found, and:

| System | To build | Where the program runs |
|---|---|---|
| Windows | gcc from MinGW-w64, 64-bit (with MSYS2, the `ucrt64` or `mingw64` toolchain) | Nothing: the window uses what is part of Windows |
| Linux | gcc, `pkg-config` and the headers of GTK 3 (`libgtk-3-dev` on Debian and Ubuntu, `gtk3-devel` on Fedora) | GTK 3, to show the window; without it the program still runs, with no window |
| macOS | The command line tools of Xcode (`xcode-select --install`) | Nothing: the window uses Cocoa, which is part of macOS |

On Linux GTK is not linked in: it is loaded when the window is asked
for. The executable depends on the C library and nothing else, so the
same file starts on a desktop and on a server that has no GTK and no
display, where it says there is no window.

To build on Linux with no GTK headers at all, give the `quevu_nogui` tag
(`go build -tags quevu_nogui`): the program is built with no window, as
it would be without cgo.

On Windows, if the build fails with nothing but `cgo.exe: exit status 2`,
gcc is dying before it can say anything, which happens when the `bin`
folder of a different MinGW comes earlier in `PATH` than its own (a shell
that ships its own, for instance). Put the folder of the gcc in use first
in `PATH`, or build from a shell that has only one.

`quevu/qui/demo` is a small program to try it with: a log that keeps
talking and a table with two controls.

### A program with no window

The window exists for Windows, for Linux and for macOS, and only in a
build with cgo. A program has no window when it was built for another
system, without cgo or with the `quevu_nogui` tag, and also when it runs
where there is no desktop to open it on: on Linux, no GTK 3 or no
display; on macOS, a remote shell with nobody logged in at the screen. In every one of those cases
the package still works: `qui.Available()` says false, every call does
nothing, and `Run` returns an error for which `qui.IsNoWindow` is true
(`qui.ErrNoWindow`). That is a normal situation, not a failure: the
visual part is optional by construction, and it is how the same program
serves a desktop and a server with no display.

### How far each system has been tried

| System | State |
|---|---|
| Windows | Tested: used every day, and driven by the tests of the package |
| Linux | Tried by hand on one desktop (Xfce on X11). Not tried on Wayland |
| macOS | **Pending to test on a Mac.** So far it has only been built and used by the automated run of the repository, on a machine with an Apple processor: nobody has sat in front of it |

### What differs between systems

The window is the native one of each system, so it looks like the rest
of the desktop it is on. What it does is the same, but for this:

- **The icon in the notification area exists on Windows only.** The
  desktops of Linux do not agree on one, and on macOS it is not written
  yet. There `Tray` does nothing, and `WithTray` is left out: closing the
  window means exiting, since there would be nowhere to bring it back
  from.
- **The range of time of a search** is picked in two date and time
  controls on Windows; on Linux and macOS it is typed in two boxes, as
  `2025-03-14 16:05:00` or the day alone.
- **On macOS the menus are on the bar across the top of the screen**,
  after the menu the system names after the program. That bar holds only
  menus: an entry put straight on the bar opens a menu with that one
  entry. Marks of keys in labels (`&`) and the names of keys after a tab
  are left out.
- **On macOS the keys of the console go with Command**, as everywhere
  there: Command+C copies, Command+A selects everything and Command+F
  goes to the search box. F3 and Shift+F3 are the same on the three.

### The window

`qui.NewWindow(title)` returns the window of the process, with nothing on
it. Everything below may be called before `Run` or while the window is
open, from any goroutine. What is there before `Run` is on the window
when it first shows.

| Method | Effect |
|---|---|
| `Run()` | Puts the window on the screen and blocks until it is gone. From `main` |
| `Quit()` | Closes the window. The only way it goes away |
| `OnReady(fn)` | Runs `fn` once the window is on the screen. Never runs if it cannot be created |
| `OnClose(fn)` | Runs `fn` every time the user asks to close. The window stays until `Quit`. With none set, asking closes |
| `Show(visible)`, `Tray(on)` | Hides or shows the window; an icon in the notification area to hide in |
| `SetText(id, text)` | Changes one of the texts of the window |
| `SetStats(every, format)` | How often, and how, the queues of a log are shown in the strip of its tab |
| `SetFindBudget(chunks)` | How many chunks one step of a search may examine (default 100,000) |

Closing is an event like any other. Nothing stops in order unless the
application does it in `OnClose` before calling `Quit`, and nothing stops
it from calling `Quit` at once: orderly or abrupt is its choice.

### Tabs

No tab is fixed. A window with none is a valid window.

```go
tab := w.OpenSource(source, qui.Weight(30))             // shows a log
tab := w.OpenSource(source, qui.Named("Orders"))        // under another name
tab := w.OpenControls("Services", qui.Weight(100))      // a table of controls
tab.Select()        // to the front
tab.ShowTime(true)  // a source tab: the time of each line in a margin
tab.Rename("Jobs")
tab.Close()
```

`Weight` says where a tab goes in the row: the heavier, the nearer the
start. Of two tabs that weigh the same, the one opened first goes first.
A tab that says nothing weighs zero, so a negative weight sends a tab
after all of those. A tab opened later takes its place among the ones
already there.

A source tab shows a `qkernel.Source` in a pseudo-console and asks it for
everything. Closing the tab only takes it off the window: whatever feeds
the source goes on, and the same source can be opened again.

### Controls

A control tab is a table with a row per control. A control has the
states and the verbs the application gives it; the window understands
none of them.

```go
c := tab.Add("Mail")
c.Set(qui.State{
	Color: qui.Amber,               // any color: qui.RGB(r, g, b)
	Blink: true,                    // the light goes on and off
	Text:  "sending 3 of 40",       // any text
	Verbs: []qui.Verb{
		{Label: "Stop", Do: stopMail},
		{Label: "Kick", Do: kickMail},
	},
})
c.Remove()
```

- `Set` says everything at once, every time the control changes: the
  light, the text and the whole list of verbs it offers from then on.
- A state with no verbs is one the user can only look at.
- `Hollow` draws the light as a ring: it is off. `Disabled` shows a verb
  that cannot be pressed.
- `Do` runs on a goroutine of its own. The button does nothing else: the
  application calls `Set` with what the control shows next.
- A control whose verbs do not change keeps its buttons as they are, so
  a text that changes often does not make them flicker. Send the verbs in
  a steady order for the same reason.
- Only the verbs of the current state answer. A button of the state
  before, pressed just as it changed, asks for nothing.
- `qui.Green`, `Amber`, `Red`, `Blue` and `Gray` read well as lights. They
  are a convenience, not a list to choose from.

A control tab has verbs of its own, for what acts on the whole table,
and a line of text in its bar:

```go
tab.SetVerbs(qui.Verb{Label: "Stop all", Do: stopAll})
tab.SetNote("3 running, 1 stopped")
```

With no verbs and no note the tab has no bar.

### Menus

The window has no menu until the application gives it one.

```go
m := w.Menu("Quit")              // a menu on the bar
normal := m.Add("Normal", fn)    // an entry
m.Separator()
sub := m.Menu("More")            // an entry that opens a menu
normal.SetChecked(true)          // the mark is the application's to move
normal.SetDisabled(true)
normal.SetLabel("Normal (running)")
normal.Remove()

w.MenuBar().Add("Help", fn)      // an entry straight on the bar
w.TrayMenu().Add("Open", func() { w.Show(true) })
```

Choosing an entry runs its function and changes nothing: an entry that
carries a mark gets it, and loses it, from the application.

### Texts

A source tab does a few things by itself (it follows the output or holds
the view, it searches, it offers the history) and has to call them
something. The window holds no word of any language: the Go side hands
it English, and `SetText` changes any of them.

```go
w.SetText(qui.TextFollowing, "Siguiendo la salida")
w.SetText(qui.TextMenuCopy, "&Copiar\tCtrl+C")
w.SetText(qui.TextFound, "Encontrado %[1]q a las %[2]s")
```

The identifiers are the `Text...` constants. The ones about the outcome
of a search are formats of package `fmt` with numbered values, so a
translation can put them in another order. An entry of the menu of a
source tab whose text is set to empty is left out of the menu.

### The window of a panel

```go
err := qui.Run(title, controller, options...)
```

`Run` is the window of an application that is happy with the usual one,
built with what `Window` offers. It takes a `qkernel.Controller`, so it
shows a `*qkernel.Panel` of its own process and a `quevu-wire` client
attached to another one in exactly the same way:

- a tab called *Units*, if the controller has any, with a row for each
  unit, a button to start or stop it, and *Start all* and *Stop all*;
- a tab for each of its sources;
- a *Panel* menu with *Exit*.

Its words are English, and units and sources are read once, when the
window opens. An application that wants other tabs, states, verbs or
words builds its own with `Window`.

| Option | Effect |
|---|---|
| `WithTray()` | An icon in the notification area; closing or minimizing hides there. Windows only: elsewhere it is left out |
| `OnReady(fn)` | Runs `fn` once the window is on the screen |
| `OnClose(fn)` | What "exit" means. The window stays until `fn` returns |
| `WithMenu(label, fn)` | An entry in an *Application* menu |
| `WithStats(every)` | How often the queues of its log are shown next to each console (default 1s, 0 off) |
| `WithFindBudget(chunks)` | How many chunks one step of a search may examine |

By default exiting stops every unit in order. A controller attached to
somebody else's service passes `OnClose(func() {})`: closing that window
must not stop the service. `qui.Quit()` closes the window from the host's
side.

### Searching

When a console asks to look for a text, this package runs one step with
`qkernel.Search` on the source of that console and shows the outcome: the
lines around the match, or how far the step got. It does not search
anything itself, and it does not decide how much is searched: the budget
is a ceiling, and the stored-data provider behind the source may allow
less, or nothing.

quevu has no dialogs. An application that needs one serves a page and
opens it from a menu entry or a verb.

### Rules and gotchas of the window

- **Call `Run` from `main`.** The window has to run on the main thread of
  the process; importing this package locks the main goroutine to it in
  an `init`. Do not call `Run` from another goroutine.
- **One window at a time per process.**
- **Nothing here stops the application.** `Quit` closes the window and
  that is all. Stopping what runs, and in what order, is done before it.
- **Providers are not an option here.** A source tab shows a
  `qkernel.Source` and asks it for everything. To let it go further back
  than the screen of its log remembers, open the source already joined
  with its providers: `qkernel.Join(screen, qkernel.Roles{...})`.
- **This package never opens the history.** It passes the source's offer
  to the window and the user's answer back; the flag that lets a request
  into the history comes only from the window.
- **`QUEVU_UI_TRACE=1`** prints every search request and its outcome to
  standard error.
- **Tests:** the parts of the C that need no window are checked in C
  (`qv_selftest.c`) and run from a Go test; the window is also
  opened for real and driven from outside the way a user would. With
  `QUEVU_UI_SHOTS=<folder>` the test saves pictures of it.
- **Internals for changes:** `door_cgo.go` is the only Go that
  touches C, one function per function of `quevu_ui.h`, and
  `door_none.go` has the same functions for a program with no window.
  Commands go down and events come up; the C never calls back. The C
  comes in two kinds. `qv_core` (strings, the text of a line, the order
  of the tabs, the model of a console) and `qv_shared` (the two one-way
  lanes) know no system: the little they need of one (locks, waiting, a
  clock, waking the window) is `qv_port`, declared in `qv.h`. The files
  that end in `_windows` are the Windows side, against Win32, and the
  ones that end in `_linux` the Linux side, against GTK 3: each has its
  `qv_port`, the window (`qv_win`, `qv_gtk`), the pseudo-console
  (`qv_view`) and the table of controls (`qv_table`). The macOS side is
  `qv_port_darwin.c` and one file of Objective-C, `qv_cocoa_darwin.m`,
  with the three in it; it keeps memory by hand, with no automatic
  reference counting. On Linux no
  function of GTK is called by its name: each goes through a table that
  is filled when GTK is loaded, written `G(function)(arguments)`, and
  `qv_gtk_syms.sh` writes the list of the functions used
  (`qv_gtk_syms_linux.h`) from the sources. Run it after adding a call. Chunk data and verb labels are copied
  into C memory for the length of a call. Each verb label gets a number
  of its own for the life of the window: that is how the window sees that
  the buttons of a control did not change.
- **Not here yet:** 32-bit builds, and the icon in the notification
  area outside Windows. On Linux the window is tried by hand; on macOS a
  workflow of the repository (`.github/workflows/macos.yml`) builds it,
  uses the demo with clicks and keys and keeps pictures of each step.
  Only the Windows one has a test that drives it.
