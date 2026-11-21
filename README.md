# quevu

The kernel of **quevu**, a control panel for Go applications (the name
comes from *queue view*): the hub and the contracts every other `quevu-*`
module is built on. Whatever brings a visual part or a transport is a
module of its own.

```go
import "github.com/pablo-botella/quevu"
```

An application made of several pieces (a web server, a mailer, a periodic
task, a group of workers) has two problems that a console window does not
solve:

- **It cannot simply be killed.** Each piece has to be told to stop, finish
  what it has in hand, and only then may the process exit.
- **Its output comes out mixed.** Everything is written to the same console
  and nobody can follow one piece on its own.

quevu gives the application an orderly start and stop, a log per piece that
is sent to every writer at that writer's own speed, and a small window
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
application decides what stopping means and when it is safe, each writer
decides what it does with what it receives, each provider decides what it
gives back, and a console only looks.

## Install

```
go get github.com/pablo-botella/quevu
```

This module has no dependencies. It holds the contracts, the hub and the
lifecycle; everything else is a module of its own, taken only by who needs
it:

| Module | What it is |
|---|---|
| `quevu` | Contracts, the hub (`quevu.QHub`), the router of the standard logger (`quevu.LogRouter`) and lifecycle coordination (this module) |
| `quevu-panel` | The panel as a volatile writer: the screen of a log |
| `quevu-txt` | Persistent writer over a text file, and its stored-data provider |
| `quevu-sql` | Persistent writers over SQL databases (SQLite, MySQL), and their stored-data providers |
| `quevu-syslog` | The system log as a queue: a writer that writes to it and an input that reads from it (Windows Event Log) |
| `quevu-pipe` | Input: a program's standard output, through a pipe, into a log |
| `quevu-sqs` | Amazon SQS, as a writer and as an input |
| `quevu-pubsub` | Google Cloud Pub/Sub, as a writer and as an input |
| `quevu-forward` | A log sent to another machine: a forwarder with a durable spool, and its receiver |
| `quevu-wire` | A panel served over a local connection, for a separate controller |
| `quevu-ui` | The window, as a dynamic library with a C interface (Rust) |
| `quevu-ui-go` | Go binding to that library, without cgo |
| `quevu-ctl` | `quevuctl`, the panel for an application running elsewhere |
| `quevu-examples` | A small application that shows all of the above at work |

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
when it is safe. Four ways to get one:

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

Not every piece is a loop. Some are **work launched on demand**: the
report a user asked for, the payment a request started, each in a
goroutine of its own. There is nothing to tell to stop; there is work
that began and must be allowed to end. That is `quevu.Work`:

```go
payments := quevu.NewWork()
panel.Add("payments", payments)

// in the handler
if !payments.Accepting() {
	http.Error(w, "stopping: try again later", http.StatusServiceUnavailable)
	return
}
payments.Go(func() { charge(order) }) // instead of: go charge(order)
```

Stopping it means no new jobs, and waiting for the ones running. A job
is never cut from there: if the time given runs out, `Stop` returns the
error and the job goes on. What the user is told when work is refused is
the application's to decide.

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
| A hub (`quevu.QHub`) | One input channel, one output channel per writer: takes what arrives, gives it a position, sends it on. Never answers a query | Anything else: only channels. Not what a writer does, not that screens exist |
| A **Writer** | Receives chunks from its channel and does its thing with them | Who else receives them, or whether the hub waits for it |
| A **Provider** | Gives chunks back by position, and decides what it gives | Who stored them |
| A console | Keeps what it shows; asks providers for the rest | Where anything is kept |

What flows through one hub is a **log**: the output of one piece, in the
order it was written. The hub is the element; the log is what it carries.

A **Chunk** is the unit of output: what is positioned, sent and asked
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

### Producers

A producer is whatever writes into the input channel of a hub: the
application itself, or a piece that reads what was written somewhere
else, like the output of another program through a pipe (`quevu-pipe`),
the event log of the system (`quevu-syslog`) or a cloud queue
(`quevu-sqs`, `quevu-pubsub`). Such a piece is a unit like any other,
started and stopped with the application. All a producer knows of a hub
is its input end:

```go
type Entry struct {
	Data []byte    // one entry is one chunk
	Time time.Time // when it was written, if the producer knows; zero is now
}

type Input interface {
	In() chan<- Entry      // any number of goroutines may send
	Done() <-chan struct{} // closed when the hub takes no more
}
```

Nobody closes the input channel: with several senders, closing is the
one thing that cannot be shared. A hub that stops closes `Done` instead.

An **Inlet** is the same input for code that does not want a channel.
`NewInlet(hub)` is an `io.Writer`, so the standard logger, `log/slog` or
`fmt.Fprintf` can write to it (every line is an entry), and it has the
two methods a reading piece needs:

```go
type Intake interface {
	Write(p []byte) (int, error) // a stream of text: lines become chunks
	Append(data []byte) error    // one chunk, whole
}
```

When `Append` returns without error the data is in the input of the hub.
That is the moment a producer may confirm it to wherever it came from,
and not before: a piece that reads a queue deletes or acknowledges a
message only then, so a message can arrive twice but is never lost on the
way in. Sending waits while the hub is held by a writer at its limit:
that is what slows a producer down.

### Writers

```go
type Writer interface {
	Run(in <-chan Chunk) error
}
```

A writer is where a hub sends things. It receives from its channel until
the channel is closed, and writes, sends or shows what arrives, or
whatever it is it does: a text file (`quevu-txt`), a database
(`quevu-sql`), the screen of the panel (`quevu-panel`), another machine
(`quevu-forward`), something that prints, something that counts. It
knows how to do nothing else. The hub gives every writer a channel and a
goroutine of its own, so a writer is never called concurrently.

**Persistent or volatile is decided when the writer is connected**, not
by the writer. The capacity of its channel is its limit, and at that
limit the hub does one of two things:

- it waits (persistent): nothing is lost, and everything else is held
  back until the writer makes room. The hub says which writer it is
  waiting for.
- it skips the chunk for that writer (volatile): the writer misses a few
  and nobody waits.

Most stores work in batches. `Batcher` is that shape (`Write(batch)`,
`Close()`), `Failures.Drain` is the loop that feeds one from a channel,
and `Batches(b, max)` makes a writer of any batcher.

A writer that also implements `SeqWriter` tells the hub the last position
it holds, so positions stay unique across restarts.

### Providers

That something consumes the output does not mean anything gives it back. A
database that stores a log is a writer; reading it is the work of a
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

- **Keep the borders.** A hub does not answer, a writer does not know its
  neighbours, a provider is not reached into: `quevu.Limit` and `Searcher`
  exist so terms are stated through interfaces. Nothing in this package
  looks at a concrete type from another module.
- **`Unit.Start` must not block** for the life of the piece. Launch and
  return; use `quevu.Go` for a function that runs until told.
- **`Watch` callbacks must not block** nor call back into the panel: they
  run on the goroutine that caused the change.
- **Add units before starting.** Order of `Add` is order of start.
- **Register the hubs with `AddCloser`.** They close last, so the final
  words of every unit reach their writers; the sources close after them.
- **`Chunk.Data` is read-only** once handed over: every writer of a log
  receives the same slice.
- **A failed `Stop` leaves the unit `Failed` and still started**, so it
  can be asked again.

## The hub: `quevu.QHub`

### One job

A hub takes what arrives on its input channel, gives every chunk a
position, and sends it on the channel of each of its writers. That is all
it knows.

What flows through one hub is a **log**: the output of one piece, in the
order it arrived. The hub is the element; the log is what it carries.

It never answers a question: there is no way to ask a hub for data. It
does not know what any writer does with what it receives, or that there
is such a thing as a screen. Remembering recent output, showing it, giving
it back by position and searching it are the work of others: writers and
providers. Keeping the hub this small is what
protects the one thing that must not fail, which is that output reaches
whoever must keep it.

### One hub, one log

A hub has **one input and one or more outputs**. The unit of separation is
the log, not the piece: what you would send to a different log file is
what should have a `QHub` of its own. Usually that is one per piece; a
piece may well have two (access and errors), or several may share one.

### The input

```go
h.In() <- quevu.Entry{Data: line}           // now
h.In() <- quevu.Entry{Data: line, Time: at} // written earlier, elsewhere
```

`In()` is a channel of `quevu.Entry`. One entry becomes one chunk. Any
number of goroutines may send on it, and what they send comes out in the
order it arrived.

Nobody closes that channel. A hub that stops says so by closing `Done()`;
a producer that sends on the bare channel should watch it:

```go
select {
case h.In() <- e:
case <-h.Done():
	// the hub is gone
}
```

`quevu.NewInlet(h)` does that for you, and turns the channel into an
`io.Writer` (every line is an entry) with `Append` and `AppendAt` for data
that is not line oriented. An unfinished line stays in the inlet until the
rest arrives or you call `Flush`: the hub never sees half a line.

### The outputs: writers

```go
h, err := quevu.NewQHub("payments",
	quevu.WithWriter(txtFile), // waited for, with the defaults
	quevu.WithWriterOptions(database, quevu.WriterOptions{
		Name:   "db",      // how the hub calls it when it speaks of it
		Buffer: 8192,      // chunks that may wait for it: the limit
		OnFull: quevu.Wait, // persistent
	}),
	quevu.WithWriterOptions(screen, quevu.WriterOptions{
		Name:   "screen",
		OnFull: quevu.Skip, // volatile
	}),
	quevu.WithInput(1024), // entries that may wait for the hub
)
```

Any number of writers, each anything that implements `quevu.Writer`:

```go
type Writer interface {
	Run(in <-chan Chunk) error
}
```

The hub gives each one a channel of its own and runs it in a goroutine of
its own. A writer is a loop that receives from its channel until it is
closed and does its thing with what arrives. It can listen to more than
its channel in the same `select`: a timer to flush, for instance.

Something that only knows how to take batches (`Write(batch)`, `Close()`)
becomes a writer with `quevu.Batches(it, max)`.

On a server with no desktop the screen is simply not in the list. A hub
with no writers at all is legal and pointless.

### Persistent or volatile is decided when connecting

The capacity of a writer's channel is its limit. A channel absorbs bursts;
what happens when it is full is the one decision left to you, and it is
what makes a writer persistent or volatile. The writer itself is the same
in both cases.

- `Wait` (default): the hub stops until the writer makes room. Nothing is
  lost. Everything else is held back meanwhile: the other writers receive
  nothing, and the producers wait in turn once the input fills.
- `Skip`: the hub leaves that chunk out for that writer, counts it, and
  goes on. The writer sees a gap in the positions.

While it is held, the hub says for whom:

```go
if name := h.Stuffed(); name != "" {
	// nothing moves until this writer catches up
}
```

and `Stats()` shows every writer:

```go
for _, s := range h.Stats() {
	fmt.Println(s.Name, s.Queued, s.Capacity, s.Written, s.Skipped,
		s.Waiting, s.Waits, s.Failed, s.LastErr)
}
```

A channel that stays full is the signal. If the application produces more
output than a writer can take, the writer is the wrong one, and the answer
is to replace it, not to tune its buffer. quevu is not a log for
gigabytes per second.

### Positions

Positions start after the highest one any writer already holds
(`quevu.SeqWriter`), so they stay unique across restarts of the
application. `Seq()` is the position of the last chunk given out.

### Closing

`Close` stops taking input, sends on what had already arrived, closes the
channel of every writer and waits for each of them to finish. It returns
what the writers returned. In an orderly stop the hubs go last, after
every unit has stopped: register them with `panel.AddCloser`.

### Rules and gotchas

- **Sending is asynchronous.** A send returns when the entry is in the
  input channel, not when the writers have it. `Close` is what guarantees
  that everything sent before it has been delivered.
- **What is sent after `Close` began may be lost.** Stop the producers
  first. An inlet returns `quevu.ErrClosed` once the hub is done.
- **`Entry.Data` belongs to the hub once sent.** Do not modify it. An
  inlet copies what it is given.
- **A writer that gives up does not hold the hub.** If `Run` returns
  before its channel is closed, the hub stops sending to it and counts
  what it would have received in `Failed`.
- **A line written in several `Write` calls** from several goroutines
  through the same inlet comes out interleaved, as with any shared writer.
  Write whole lines.
- **A hub cannot be read.** It has no `Tail` and no feed. To show its
  log attach a screen (`quevu-panel`); to read what was stored attach a
  stored-data provider to that screen.
- **Do not add behaviour here.** If something needs to remember, show,
  return or search output, it is a writer or a provider.
- **Internals for changes:** one goroutine receives from the input, gives
  the position and sends to each output in turn, so every writer sees the
  same order. A `Wait` output that is full blocks that goroutine: that is
  exactly how everything else is held.

## The standard logger, split into logs: `quevu.LogRouter`

An application that grew with `log.Printf` everywhere has one stream
where everything mixes. Its lines often say where they come from by how
they begin: `mailer: sent 3`, `payments: declined`. A `LogRouter` reads
that beginning and hands each message to the writer of its channel,
**without changing a single call**.

```go
r := quevu.NewLogRouter(quevu.NewInlet(general)) // what begins with nothing known
r.Route(quevu.NewInlet(mailHub), "mailer")       // "mailer: ..." and "mailer queue: ..."
r.Route(quevu.NewInlet(payHub), "payments", "pay_worker")
r.Tee(os.Stderr, quevu.StdLayout) // the console keeps showing everything
restore := r.Install()            // becomes the output of the standard logger
defer restore()
```

The writers are `io.Writer`s: an `Inlet` over a hub is one, so every
channel can be a log of its own, stored and shown apart.

### How a message finds its channel

A message is routed by **the word it begins with, when a colon or a
blank follows it**:

| Message | With `Route(w, "mailer")` goes to |
|---|---|
| `mailer: sent 3` | `w` |
| `mailer queue: 2 left` | `w` |
| `mailers: ...` | the general writer: another word |
| `Mailer: ...` | the general writer: words are lower case |
| `Error: mailer: ...` | the general writer: it does not begin with it |

A word is made of lower case letters, digits and underscores, the way a
package is named, 32 characters at most; `Route` refuses any other.
Several words can lead to one writer.

A message is handed over **whole**, as the logger writes it: one with
several lines goes to one channel, lines together.

### The console stays as it was

`Install` takes the date and time off the lines of the standard logger,
because each channel stamps its own. `Tee(w, layout)` copies every
message to `w` with the time in front again, so what the process printed
before it keeps printing, in the same shape (`StdLayout` is the default
format of the standard logger).

### Packages whose lines say nothing

A package that logs `Error saving: ...` with no name in front cannot be
routed. Instead of editing every call, give the package a logger with
the name of the standard package:

```go
package billing

import "github.com/pablo-botella/quevu"

var log = quevu.Logger("billing") // in place of importing "log"
```

A `*log.Logger` has the functions of the package (`Printf`, `Println`,
`Fatalf`...), so the calls stay as they are and every line now begins
with `billing: `. The logger writes wherever the standard logger writes
at that moment: it can be made before the router exists.

### Rules and gotchas

- **A channel that fails does not silence the application.** The copy of
  `Tee` is written first; the error goes back to the logger, which
  ignores it.
- **Only the standard logger is routed.** What is printed with `fmt`
  straight to the console does not pass through here.
- **`Install` is global**, as the standard logger is. Call what it
  returns to put the previous output and flags back.
- **A nil general writer drops** what has no channel.
- Give each channel an `Inlet` over a hub of its own: the inlet cuts what
  it receives into lines and the hub gives each a position.

## License

MIT - see [LICENSE](LICENSE).
