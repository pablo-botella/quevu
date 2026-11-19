# quevu

A control panel for Go applications. The name comes from *queue view*.

An application made of several pieces (a web server, a mailer, a periodic
task, a group of workers) has two problems that a console window does not
solve:

- **It cannot simply be killed.** Each piece has to be told to stop, finish
  what it has in hand, and only then may the process exit.
- **Its output comes out mixed.** Everything is written to the same console
  and nobody can follow one piece on its own.

quevu gives the application an orderly start and stop, a log per piece that
is sent to every consumer at that consumer's own speed, and a small window
with the switches and the screens.

```go
panel := quevu.New()
panel.Add("web", webServer)                 // anything with Start and Stop
panel.Add("mailer", quevu.Go(runMailer))    // or a function that runs until told

// Start everything, wait for Ctrl+C or the stop order of the system,
// stop everything in reverse order, close the logs last.
if err := panel.Run(context.Background()); err != nil {
	log.Fatal(err)
}
```

quevu does not solve the application's problems, it hands them out: the
application decides what stopping means and when it is safe, each consumer
decides what it does with what it receives, each provider decides what it
gives back, and a console only looks.

## Install

```
go get github.com/pablo-botella/quevu
```

This module has no dependencies. It holds the contracts and the lifecycle;
everything else is a module of its own, taken only by who needs it:

| Module | What it is |
|---|---|
| `quevu` | Contracts and lifecycle coordination (this module) |
| `quevu-log` | The non-visual log: positions, and a queue per consumer |
| `quevu-panel` | The panel as a volatile consumer: the screen of a log |
| `quevu-txt` | Persistent consumer over a text file, and its stored-data provider |
| `quevu-sqlite` | Persistent consumer over SQLite, and its stored-data provider |
| `quevu-syslog` | Persistent consumer over the system log (Windows Event Log) |
| `quevu-pipe` | Input: a program's standard output, through a pipe, into a log |
| `quevu-sqs` | Amazon SQS, as a consumer and as an input |
| `quevu-pubsub` | Google Cloud Pub/Sub, as a consumer and as an input |
| `quevu-wire` | A panel served over a local connection, for a separate controller |
| `quevu-ui` | The window, as a dynamic library with a C interface (Rust) |
| `quevu-ui-go` | Go binding to that library, without cgo |
| `quevu-ctl` | `quevuctl`, the panel for an application running elsewhere |
| `quevu-demo` | A small application that shows all of the above at work |

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
- `AddCloser`: the logs, closed once every unit has stopped.
- `AddSource`: what a console shows in a tab.
- `Shutdown`: `StopAll` within `StopTimeout`, then `Close`.
- `Run`: start everything, wait for `ctx` or for the system to ask the
  process to end, then `Shutdown`. The whole life of an application with no
  window.

The context given to `Stop` is how long the caller is willing to wait. When
it expires `Stop` returns its error and the unit is left as `Failed`, not
as stopped: quevu never pretends something stopped. The operating system
has a limit of its own when it stops a service; a long orderly stop has to
fit in it or raise it.

## Output: who does what

The division of work is strict. Each interface marks one of its borders.

| Piece | Its work | What it does not know |
|---|---|---|
| A log (`quevu-log`) | Consumes what the application writes and sends it to its consumers | Anything else: not which consumers persist, not that screens exist |
| A **Consumer** | Receives chunks and does with them what it sees fit | Who else receives them |
| A **Provider** | Gives chunks back by position, and decides what it gives | Who stored them |
| A console | Keeps what it shows; asks providers for the rest | Where anything is kept |

A **Chunk** is the unit of output: what is queued, positioned and asked
for. Usually a line, not necessarily.

```go
type Chunk struct {
	Seq  uint64    // position in its log: from 1, plus one per chunk
	Time time.Time // when it entered the log
	Data []byte
}
```

`Seq` is the same for whoever holds the chunk, which is what lets a reader
move from recent output to stored output without a seam.

### Inputs

A log is written to by the application itself, and it can also be fed from
outside: the output of another program through a pipe (`quevu-pipe`), a
cloud queue (`quevu-sqs`, `quevu-pubsub`). An input is a unit like any
other, started and stopped with the application, and all it knows of a
log is its input end:

```go
type Intake interface {
	Write(p []byte) (int, error)        // a stream of text: lines become chunks
	Append(data []byte) (uint64, error) // one chunk, whole
}
```

When `Append` returns without error the log has accepted the data. That
is the moment an input may confirm it to wherever it came from, and not
before: an input that reads a queue deletes or acknowledges a message only
then, so a message can arrive twice but is never lost on the way in.

### Consumers

```go
type Consumer interface {
	Write(batch []Chunk) error
	Close() error
}
```

A consumer may be **persistent** (`quevu-txt`, `quevu-sqlite`) or
**volatile** (the screen of the panel, `quevu-panel`; something that
prints; something that counts). That is a property of the consumer, not
something the log is told. A log queues chunks for each consumer and hands
them over in batches from a goroutine per consumer, so a consumer is never
called concurrently and never slows another one down.

A consumer that also implements `SeqConsumer` tells the log the last
position it holds, so positions stay unique across restarts.

### Providers

That something consumes the output does not mean anything gives it back. A
database that stores a log is a consumer; reading it is the work of a
**stored-data provider**, a piece of its own, attached on purpose.

```go
type Provider interface {
	Tail(n int) ([]Chunk, error)
	Before(seq uint64, n int) ([]Chunk, error)
	After(seq uint64, n int) ([]Chunk, error)
}
```

Every method returns at most `n` chunks. Fewer, or none, is the provider's
right, and is not questioned: an empty answer means there is nothing
further that way as far as that provider is concerned.

`quevu.Limit` is how a provider states its terms without each
implementation growing the same options:

```go
history := quevu.Limit(reader, quevu.Limits{
	Page:         500,            // at most this many per request
	MaxAge:       24 * time.Hour, // nothing older than this
	SearchBudget: 20_000,         // a search step examines no more
	NoSearch:     false,          // or refuse to be searched at all
})
```

### A console's providers, and their roles

A console escalates in three steps, and only the first two happen by
themselves:

1. **Its own memory**: what its screen remembers.
2. **The immediate provider**, automatically, as soon as it needs
   something the screen no longer has.
3. **The historical provider**, only on its user's request. A console
   that runs out of recent output says so and offers the history, if one
   is configured. It never goes there on its own.

A role is given by configuration, not by class. Nothing in a provider
says which one it plays, and the same kind can be set up twice to play
both:

```go
src := quevu.Join(screen, quevu.Roles{
	Immediate:  liveReader,    // a reader over the live table
	Historical: archiveReader, // the same kind of reader, over the archives
})
panel.AddSource(src)
```

Either role may be left empty. With neither, the screen is all there is.

There is **one** historical provider as far as a console is concerned. If
the history lives in several places it is an aggregator that knows them;
the console never does:

```go
history := quevu.Aggregate(year2024, year2025, liveTable) // oldest first
```

`Aggregate` answers a request by asking as many parts as it takes, in the
order the direction calls for. Parts may overlap; a chunk held by two is
given once. A part out of reach does not hide the others.
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
- `OpBefore` and `OpAfter` move by position through recent output. An
  answer to `OpBefore` that comes back short means recent output ends
  there; if it carries `History`, a history exists and may be offered.
- `OpHistoryBefore` and `OpHistoryAfter` are the same through the
  history. A console sends them only after its user asked. Walking
  forwards out of the history into recent output needs no permission.

## Searching

A log may hold millions of chunks, so a search is never "the whole log".
It is a **step**: it starts at a position, walks one way, examines a
bounded number of chunks and stops, saying how far it got. Whoever asked
decides whether to take another step from there.

```go
res, err := quevu.Search(src, quevu.Query{
	Text:   "timeout",
	From:   0,       // going backwards, zero is the end of the log
	Budget: 100_000, // chunks examined, found or not
	Since:  monday,  // optional: only what entered the log in this range
	Until:  tuesday,
})
if len(res.Matches) == 0 && !res.Done {
	// Nothing in this stretch. res.Reached and res.ReachedTime say how
	// far back it looked; the next step starts at res.Reached.
}
```

- The match ignores case and ignores ANSI color sequences: it is made
  against the text as a console shows it (`quevu.PlainText`), so a phrase
  is found even when a change of color falls in the middle of it.
- `Search` asks the provider if it knows how to search itself
  (`Searcher`); otherwise it reads it page by page (`Scan`), which works
  for any provider and costs what the budget says.
- A joined source searches each piece through that piece's own interface
  and on its terms: the screen, then the immediate provider. The history
  is searched only when `Query.History` is set, which means the user
  asked; otherwise a step that runs out of recent output returns with
  `Result.History` set, as an offer, and stops.
- `Since` and `Until` bound the search in time. A step that walks past
  the far bound stops there with `OutOfRange`: chunks are in the order
  they were written, so nothing beyond it can be in range, in this
  provider or in any older one. The history is then not offered.
- `Done` means the step ran out of log, not out of budget.

## Controller

`Controller` is the panel as a visual part sees it: units, switches, a way
to watch changes, and the sources. `*Panel` implements it in the
application's process and the `quevu-wire` client implements it at the
other end of a connection. A window talks to the interface and does not
know which of the two is behind.

## Rules and gotchas

- **Keep the borders.** A log does not read, a consumer does not know its
  neighbours, a provider is not reached into: `quevu.Limit` and `Searcher`
  exist so terms are stated through interfaces. Nothing in this package
  looks at a concrete type from another module.
- **`Unit.Start` must not block** for the life of the piece. Launch and
  return; use `quevu.Go` for a function that runs until told.
- **`Watch` callbacks must not block** nor call back into the panel: they
  run on the goroutine that caused the change.
- **Add units before starting.** Order of `Add` is order of start.
- **Register the logs with `AddCloser`.** They close last, so the final
  words of every unit reach their consumers; the sources close after them.
- **`Chunk.Data` is read-only** once handed over: every consumer of a log
  receives the same slice.
- **A failed `Stop` leaves the unit `Failed` and still started**, so it
  can be asked again.

## License

MIT - see [LICENSE](LICENSE).
