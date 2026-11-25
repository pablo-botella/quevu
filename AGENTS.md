# quevu - agent notes

Go module `github.com/pablo-botella/quevu`. The module has no package
at its root: the kernel is package `quevu/qkernel`, and next to it are
`quevu/qpanel`, `quevu/qtxt` and `quevu/qui`. One tag versions them all.
In `qkernel` live the hub (`QHub`), the contracts of the quevu control panel and
the coordination of an application's lifecycle. No dependencies. Every other `quevu-*` module imports this one and none is
imported by it.

Design principle to respect in any change: quevu hands problems out, it
does not solve them. The application decides what stopping means, a
writer decides what it does with what it receives, a provider decides
what it gives back, a console only looks. Do not move a decision to a
piece it does not belong to, and do not let a piece reach into another:
they meet through the interfaces here and nowhere else.

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
panel.Add("cache", qkernel.Func(open, close)) // two functions
panel.Add("mailer", qkernel.Go(func(ctx context.Context) error {
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

`qkernel.Go` wraps a function that runs for the life of the piece: `Stop`
cancels its context and waits for it to return. How to wind down is written
inside the function, which is where the knowledge is.

Not every piece is a loop. Some are **work launched on demand**: the
report a user asked for, the payment a request started, each in a
goroutine of its own. There is nothing to tell to stop; there is work
that began and must be allowed to end. That is `qkernel.Work`:

```go
payments := qkernel.NewWork()
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
| A hub (`qkernel.QHub`) | One input channel, one output channel per writer: takes what arrives, gives it a position, sends it on. Never answers a query | Anything else: only channels. Not what a writer does, not that screens exist |
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
whatever it is it does: a text file (`quevu/qtxt`), a database
(`quevu-sql`), the screen of the panel (`quevu/qpanel`), another machine
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

`qkernel.Limit` is how a provider states its terms without each
implementation growing the same options:

```go
history := qkernel.Limit(reader, qkernel.Limits{
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
src := qkernel.Join(screen, qkernel.Roles{
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
history := qkernel.Aggregate(year2024, year2025, liveTable) // oldest first
```

`Aggregate` answers a request by asking as many parts as it takes, in the
order the direction calls for. Parts may overlap; a chunk held by two is
given once. A part out of reach does not hide the others.
## Feeds: two one-way lanes

A feed connects one console to one source. `Data` brings deliveries down,
`Req` takes requests up; the two never mix.

```go
f := src.Subscribe(0)
f.Req <- qkernel.Request{Op: qkernel.OpFollow, N: 200} // last 200, then live
for d := range f.Data {
	switch d.Op {
	case qkernel.OpFollow: // replaces what the console shows
	case qkernel.OpLive:   // new chunks
	case qkernel.OpPause:  // the "do not disturb" sign; d.Full if the source hung it
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
res, err := qkernel.Search(src, qkernel.Query{
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
  against the text as a console shows it (`qkernel.PlainText`), so a phrase
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
  neighbours, a provider is not reached into: `qkernel.Limit` and `Searcher`
  exist so terms are stated through interfaces. Nothing in this package
  looks at a concrete type from another module.
- **`Unit.Start` must not block** for the life of the piece. Launch and
  return; use `qkernel.Go` for a function that runs until told.
- **`Watch` callbacks must not block** nor call back into the panel: they
  run on the goroutine that caused the change.
- **Add units before starting.** Order of `Add` is order of start.
- **Register the hubs with `AddCloser`.** They close last, so the final
  words of every unit reach their writers; the sources close after them.
- **`Chunk.Data` is read-only** once handed over: every writer of a log
  receives the same slice.
- **A failed `Stop` leaves the unit `Failed` and still started**, so it
  can be asked again.

## The hub: `qkernel.QHub`

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
h.In() <- qkernel.Entry{Data: line}           // now
h.In() <- qkernel.Entry{Data: line, Time: at} // written earlier, elsewhere
```

`In()` is a channel of `qkernel.Entry`. One entry becomes one chunk. Any
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

`qkernel.NewInlet(h)` does that for you, and turns the channel into an
`io.Writer` (every line is an entry) with `Append` and `AppendAt` for data
that is not line oriented. An unfinished line stays in the inlet until the
rest arrives or you call `Flush`: the hub never sees half a line.

### The outputs: writers

```go
h, err := qkernel.NewQHub("payments",
	qkernel.WithWriter(txtFile), // waited for, with the defaults
	qkernel.WithWriterOptions(database, qkernel.WriterOptions{
		Name:   "db",      // how the hub calls it when it speaks of it
		Buffer: 8192,      // chunks that may wait for it: the limit
		OnFull: qkernel.Wait, // persistent
	}),
	qkernel.WithWriterOptions(screen, qkernel.WriterOptions{
		Name:   "screen",
		OnFull: qkernel.Skip, // volatile
	}),
	qkernel.WithInput(1024), // entries that may wait for the hub
)
```

Any number of writers, each anything that implements `qkernel.Writer`:

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
becomes a writer with `qkernel.Batches(it, max)`.

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
(`qkernel.SeqWriter`), so they stay unique across restarts of the
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
  first. An inlet returns `qkernel.ErrClosed` once the hub is done.
- **`Entry.Data` belongs to the hub once sent.** Do not modify it. An
  inlet copies what it is given.
- **A writer that gives up does not hold the hub.** If `Run` returns
  before its channel is closed, the hub stops sending to it and counts
  what it would have received in `Failed`.
- **A line written in several `Write` calls** from several goroutines
  through the same inlet comes out interleaved, as with any shared writer.
  Write whole lines.
- **A hub cannot be read.** It has no `Tail` and no feed. To show its
  log attach a screen (`quevu/qpanel`); to read what was stored attach a
  stored-data provider to that screen.
- **Do not add behaviour here.** If something needs to remember, show,
  return or search output, it is a writer or a provider.
- **Internals for changes:** one goroutine receives from the input, gives
  the position and sends to each output in turn, so every writer sees the
  same order. A `Wait` output that is full blocks that goroutine: that is
  exactly how everything else is held.

## The screen of a log: `quevu/qpanel`

```
import "github.com/pablo-botella/quevu/qpanel"
```

A `Screen` is attached to a hub like any other writer, next to the file
or the database. It is the volatile one: it keeps the last few hundred
chunks in memory, hands new ones to whoever is watching, and stores
nothing.

```go
screen := qpanel.New("web")
web, _ := qkernel.NewQHub("web",
	qkernel.WithWriter(file),   // persistent
	qkernel.WithWriterOptions(screen, qkernel.WriterOptions{OnFull: qkernel.Skip}), // volatile: skipped at its limit
)
screen.ShowStats(web) // so the console can show the queues of its log

panel.AddCloser(web)
panel.AddSource(qkernel.Join(screen, qkernel.Roles{Immediate: qtxt.NewReader("web.log")}))
```

The hub knows none of this. To the hub a screen is one more writer,
connected so that it is skipped at its limit instead of waited for.

### What a screen is

To the log, a writer: it takes batches and never makes the log wait.

To the console that shows it, two things at once:

- **The live output**, through a feed that is never waited for. A console
  that falls behind is paused; nothing upstream notices.
- **The immediate provider**: what was written a moment ago, given back by
  position without touching any store.

What lies further back is not the screen's business. The providers the
application attaches with `qkernel.Join` take over where the short memory
ends: the immediate one by itself, the historical one only when the user
asks for it.

```go
screen := qpanel.New("web", qpanel.WithMemory(500)) // 500 is the default
```

`WithMemory(0)` keeps nothing: the console then gets live output only.

### On a server with no desktop

The screen is the part of quevu that only makes sense when somebody can be
watching. Where nobody can, do not create it: the log has one writer
less and the application is otherwise the same. Create it when there is a
window, or an endpoint a controller may connect to.

### The queues of the log

A console shows, next to its state, the queues of its log: one that keeps
growing is how one sees that a writer is too slow. Those numbers belong
to the log, so the screen is only told where to read them from and passes
them on:

```go
screen.ShowStats(theLog)
```

### Closing a screen

`Close` ends the feeds. What the screen remembers stays readable, so a
window still open shows the last words of a log that has closed. It is
safe to call more than once, and it is: the log closes its writers, and
the panel closes its sources.

### Rules and gotchas of the screen

- **One screen per log, one tab per screen.** Its name is the title of the
  tab.
- **A screen stores nothing.** When the application ends, what it held is
  gone. For output that must be kept, the log needs a persistent writer
  as well; for it to be read back, that writer needs a provider.
- **`Before` returning nothing means the short memory ends there**, not
  that the log does.
- **Do not hold a feed without reading it** unless you mean to: its buffer
  fills and the screen pauses it. That is by design, and harmless.
- **Internals for changes:** one mutex guards the memory and the set of
  feeds, which is what makes `OpFollow` exact: the last chunks are taken
  and the feed switched to live output in the same step, so nothing falls
  between the two.

## The standard logger, split into logs: `qkernel.LogRouter`

An application that grew with `log.Printf` everywhere has one stream
where everything mixes. Its lines often say where they come from by how
they begin: `mailer: sent 3`, `payments: declined`. A `LogRouter` reads
that beginning and hands each message to the writer of its channel,
**without changing a single call**.

```go
r := qkernel.NewLogRouter(qkernel.NewInlet(general)) // what begins with nothing known
r.Route(qkernel.NewInlet(mailHub), "mailer")       // "mailer: ..." and "mailer queue: ..."
r.Route(qkernel.NewInlet(payHub), "payments", "pay_worker")
r.Tee(os.Stderr, qkernel.StdLayout) // the console keeps showing everything
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

import "github.com/pablo-botella/quevu/qkernel"

var log = qkernel.Logger("billing") // in place of importing "log"
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

## A log in a text file: `quevu/qtxt`

```
import "github.com/pablo-botella/quevu/qtxt"
```

A log in a plain text file: the persistent writer that writes it and, apart from it, the
stored-data provider that gives it back by position.

```go
file, _ := qtxt.Open("web.log", qtxt.Options{}) // consumes
web, _ := qkernel.NewQHub("web", qkernel.WithWriter(file), qkernel.WithWriterOptions(screen, qkernel.WriterOptions{OnFull: qkernel.Skip}))

// Writing a file does not mean reading it back: that takes a provider,
// with a file handle of its own. Here it plays the immediate role: the
// console walks from the live output back into the file by itself.
src := qkernel.Join(screen, qkernel.Roles{Immediate: qtxt.NewReader("web.log")})
```

The file stays readable with any tool: one chunk per line.

```
1	2026-01-02T03:04:05.123456789Z	listening on :8080
2	2026-01-02T03:04:05.2Z	GET / 200 12ms
```

### File format

One chunk per line, three fields separated by tabs, lines ended by LF:

```
<seq> TAB <time, RFC 3339 with nanoseconds, UTC> TAB <data>
```

In the data, backslash, LF and CR are written as `\\`, `\n` and `\r`, so a
chunk never spans lines. Positions grow along the file, which is what lets
the reader find one by bisection instead of reading from the start.

### Device: the writer

```go
file, err := qtxt.Open(path, qtxt.Options{Sync: false})
```

- Appends only, and gives nothing back. A batch is one write to the file.
- `Sync: true` forces every batch to the disk before `Write` returns: the
  safe and slow lane. Without it a batch is in the hands of the operating
  system when `Write` returns, which survives the death of the process
  but not a power cut.
- `LastSeq` reports the last position found when the file was opened, so
  the log continues counting from there.
- A last line left unfinished by a process that died is closed off on
  `Open`, so what comes next starts on a line of its own.

### Reader: the stored-data provider

```go
r := qtxt.NewReader(path)
last, _ := r.Tail(200)
older, _ := r.Before(last[0].Seq, 200)
```

- Keeps nothing open and nothing in memory between requests. Every call
  opens the file, looks at its current size and reads only what it needs,
  so it can be used while a device appends to the same file and with the
  application stopped.
- `Before` and `After` bisect on byte offsets; `Tail` and `Before` read
  backwards in windows that grow as needed; `After` reads forwards.
- A missing file is an empty log, not an error.
- The unfinished tail of the file is never served, and damaged lines are
  skipped.

### Rules and gotchas of the text file

- **The two halves are independent.** A log can write a file nobody
  reads back, and a reader can serve a file no log is writing. To limit
  what the reader gives (page size, age, searches), wrap it with
  `qkernel.Limit`.
- **Searching reads the file.** There is no index: a search step is a
  bounded scan, which is what its budget is for.
- **Positions must grow along the file.** The device guarantees it when
  the log takes its `LastSeq`. A file edited by hand, or two logs writing
  to one file, break the bisection.
- **One writer per file.** Readers are unlimited.
- **It is not a rotation scheme.** A file grows until something else
  rotates it; a rotated file is a different log to the reader.

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
