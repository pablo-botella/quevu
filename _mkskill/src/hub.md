---
mkskill:
  pos: 110
  in: "*"
---

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
