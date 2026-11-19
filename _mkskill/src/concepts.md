---
mkskill:
  pos: 100
  in: "*"
---

## Lifecycle

A **unit** is one piece of the application. It is the only thing pieces of
very different natures have in common:

```go
type Unit interface {
	Start(ctx context.Context) error // launch and return; do not block
	Stop(ctx context.Context) error  // return when it is safe: work in hand finished
}
```

The application implements it, and with it decides what stopping means and
when it is safe. Three ways to get one:

```go
panel.Add("web", srv)                       // a type of your own
panel.Add("cache", quevu.Func(open, close)) // two functions
panel.Add("mailer", quevu.Go(func(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil // nothing in hand: stopped
		case msg := <-queue:
			send(msg) // in hand: finished whatever ctx says
		}
	}
}))
```

`quevu.Go` wraps a function that runs for the life of the piece: `Stop`
cancels its context and waits for it to return. How to wind down is written
inside the function, which is where the knowledge is.

A **Panel** holds the units. It starts them in the order they were added
and stops them in reverse. `StopAll` asks every unit even if one fails, and
joins the errors. A unit that ends by itself (see `Exiter`) is noticed and
shown as stopped or failed.

- `Start`, `Stop`: one unit. `StartAll`, `StopAll`: the global switch.
- `Units`, `Watch`: the state of every unit, and every change of it.
- `Shutdown`: `StopAll` within `StopTimeout`, then `Close`.
- `Run`: start everything, wait for `ctx` or for the system to ask the
  process to end, then `Shutdown`. The whole life of an application with no
  window.

The context given to `Stop` is how long the caller is willing to wait. When
it expires `Stop` returns its error and the unit is left as `Failed`, not
as stopped: quevu never pretends something stopped. The operating system
has a limit of its own when it stops a service; a long orderly stop has to
fit in it or raise it.

## Logs: the contracts

The log itself lives in `quevu-log`. Here are the interfaces it and its
neighbours meet through.

A **Chunk** is the unit of output: what is queued, positioned and asked
for. Usually a line, not necessarily.

```go
type Chunk struct {
	Seq  uint64    // position in its log: from 1, plus one per chunk
	Time time.Time // when it entered the log
	Data []byte
}
```

`Seq` is the same for every source of the same log, which is what lets a
reader move from live output to stored output without a seam.

A **Device** is a persistent destination. The log queues chunks for it and
writes them in batches from a goroutine per device, so a device is never
called concurrently and never slows another destination down:

```go
type Device interface {
	Write(batch []Chunk) error
	Close() error
}
```

A device that also implements `SeqDevice` tells the log the last position
it holds, so positions stay unique across restarts.

A **Provider** serves chunks by position. It is all a console needs to move
forwards and backwards:

```go
type Provider interface {
	Tail(n int) ([]Chunk, error)
	Before(seq uint64, n int) ([]Chunk, error)
	After(seq uint64, n int) ([]Chunk, error)
}
```

A **Source** is a log as a console sees it: a `Provider` with a name that
can open a live **Feed**.

## Feeds: two one-way lanes

A feed connects one console to one source. `Data` brings deliveries down,
`Req` takes requests up; the two never mix.

```go
f := src.Subscribe(0)
f.Req <- quevu.Request{Op: quevu.OpFollow, N: 200} // last 200, then live
for d := range f.Data {
	switch d.Op {
	case quevu.OpFollow: // replaces what the console shows
	case quevu.OpLive:   // new chunks
	case quevu.OpPause:  // the "do not disturb" sign; d.Full if the source hung it
	}
}
```

- A feed is born paused. `OpFollow` starts it, and resumes it after a
  pause: it does not catch up with what it missed, it jumps to the end.
- `OpPause` hangs the *do not disturb* sign. While it hangs, the source
  sends nothing live to this feed.
- If the console falls behind and its buffer fills, the source hangs the
  sign by itself and says so with `Delivery{Op: OpPause, Full: true}`. The
  source never waits for a console.
- `OpBefore` and `OpAfter` move by position. An empty answer to `OpBefore`
  means there is nothing further back.

A console is a view, not a store: it keeps what the user can see and asks
again for the rest.

## History: changing provider without noticing

A log remembers only its last few hundred chunks. Going further back means
changing source, and the provider behind it: a reader over the persistent
store, with a file handle or a connection of its own, so that reading
history never competes with the component that guarantees persistence.

```go
src := quevu.WithHistory(theLog, qtxt.NewReader(path))
```

The result is one `Source`. Where the short memory ends the reader takes
over, in both directions, and positions make the join seamless.

## Controller

`Controller` is the panel as a visual part sees it: units, switches, a way
to watch changes, and the sources. `*Panel` implements it in the
application's process and the `quevu-wire` client implements it at the
other end of a connection. A window talks to the interface and does not
know which of the two is behind.

## Rules and gotchas

- **`Unit.Start` must not block** for the life of the piece. Launch and
  return; use `quevu.Go` for a function that runs until told.
- **`Watch` callbacks must not block** nor call back into the panel: they
  run on the goroutine that caused the change.
- **Add units before starting.** Order of `Add` is order of start.
- **Close the logs last.** `Shutdown` and `Run` do it; if you call
  `StopAll` yourself, call `Close` after it, so the last words of every
  unit are stored.
- **`Chunk.Data` is read-only** once handed over: it is shared by the
  short memory, the queues and the feeds.
- **A failed `Stop` leaves the unit `Failed` and still started**, so it
  can be asked again.
