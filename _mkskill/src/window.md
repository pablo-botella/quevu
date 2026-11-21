---
mkskill:
  pos: 130
  in: "*"
---

## The window, from Go: `quevu/ui`

```
import "github.com/pablo-botella/quevu/ui"
```

Package `ui` shows a panel in a window. The window itself is the
`quevu-ui` dynamic library; this package loads it at run time and speaks
to it through its C interface, without cgo, so a program that imports it
still builds with a plain `go build`.

It is a package of its own, and not part of package `quevu`, for one
reason: importing it ties the main goroutine to the main thread of the
process. A program with no window imports `quevu` alone and pays for none
of this.

```go
func main() {
	panel := quevu.New()
	// ... add units and logs ...

	lib, err := ui.Load("")
	if ui.IsNoLibrary(err) {
		// No window on this machine: the same binary runs headless.
		log.Fatal(panel.Run(context.Background()))
	}

	panel.StartAll(context.Background())
	err = lib.Run("my application", panel, ui.WithTray())
	// The window is gone and every unit has stopped. The logs go last.
	panel.Shutdown()
}
```

### Finding the library

`ui.Load(path)` loads `quevu_ui.dll`, `libquevu_ui.so` or
`libquevu_ui.dylib`. With an empty path it looks, in this order:

1. at the `QUEVU_UI_LIB` environment variable,
2. next to the executable,
3. wherever the system looks for libraries.

An error for which `ui.IsNoLibrary` is true means this machine has no
visual part. That is a normal situation, not a failure: the visual part is
optional by construction, and it is how one binary serves both a desktop
and a server with no display.

### Running the window

```go
err := lib.Run(title, controller, options...)
```

`Run` blocks until the window is gone. It takes a `quevu.Controller`, so
it shows a `*quevu.Panel` of its own process and a `quevu-wire` client
attached to another one in exactly the same way.

For each unit of the controller it adds a switch and keeps its state up to
date. For each source it adds a tab with a pseudo-console and opens a feed
on it. The window decides nothing: a switch asks the controller to start
or stop a unit, and a console asks its source for chunks.

| Option | Effect |
|---|---|
| `WithTray()` | An icon in the notification area; closing or minimizing hides there |
| `OnClose(fn)` | What "close the application" means. The window stays until `fn` returns |
| `WithMenu(label, fn)` | An entry in the application menu |
| `WithStats(every)` | How often the queues of its log are shown next to each console (default 1s, 0 off) |
| `WithFindBudget(chunks)` | How many chunks one step of a search may examine (default 100,000) |

By default closing stops every unit in order, which is what the window of
an application should do. A controller attached to somebody else's service
passes `OnClose(func() {})`: closing that window must not stop the
service.

`lib.Quit()` closes the window from the host's side.

### Searching

When a console asks to look for a text, this package runs one step with
`quevu.Search` on the source of that console and shows the outcome: the
lines around the match, or how far the step got. It does not search
anything itself, and it does not decide how much is searched: the budget
above is a ceiling, and the stored-data provider behind the source may
allow less, or nothing.

quevu has no dialogs. An application that needs one serves a page and
opens it from a `WithMenu` entry.

### Rules and gotchas of the window

- **Call `Run` from `main`.** The window has to run on the main thread of
  the process; importing this package locks the main goroutine to it in
  an `init`. Do not call `Run` from another goroutine.
- **At most one `Run` per process.**
- **Providers are not an option here.** A console shows a `quevu.Source`
  and asks it for everything. To let it go further back than the screen
  of its log remembers, register the source already joined with its
  providers: `panel.AddSource(quevu.Join(screen, quevu.Roles{...}))`.
- **This package never opens the history.** It passes the source's offer
  to the window and the user's answer back; the flag that lets a request
  into the history comes only from the window.
- **`QUEVU_UI_TRACE=1`** prints every search request and its outcome to
  standard error.
- **Ship the library next to the executable**, or set `QUEVU_UI_LIB`.
- **The interface version is checked on load**: a library that speaks
  another version of the C interface is refused with a clear error.
- **Internals for changes:** the structs `cChunk` and `cEvent` mirror the
  C ones field by field; chunk data is passed by pointer and must stay
  alive until the call returns (the library copies it), hence the
  `runtime.KeepAlive`.
