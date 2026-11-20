// Package quevu holds the contracts of the quevu control panel and the
// coordination of an application's lifecycle.
//
// quevu ("queue view") gives an application an orderly start and stop of
// the pieces it is made of, and a log mechanism that keeps the output of
// each piece apart and hands it out, each destination at its own speed.
//
// The division of work is strict, and every interface here marks one of
// its borders:
//
//   - A hub has one input channel and one or more output channels. It
//     takes what arrives on the first, gives it a position, and sends it
//     on every one of the others. That is all it knows: for a hub there
//     are only channels, and it never answers a query. What flows through
//     one hub is a log, the output of one piece.
//   - A producer writes into the input channel of a hub: the application
//     itself, or a piece that reads what was written somewhere else.
//   - A Writer receives from one output channel and does its thing with
//     what arrives: stores it, shows it, sends it on. It knows nothing
//     else. Whether it is treated as persistent or volatile is decided
//     when it is connected to the hub.
//   - A Provider gives chunks back, by position. That something writes
//     the output does not mean anything returns it: a provider is a piece
//     of its own, attached on purpose, and it decides what it hands over.
//   - A console keeps what it shows, and asks providers for the rest: an
//     immediate one for what is recent, a historical one for what is
//     stored.
//
// quevu does not solve the application's problems, it hands them out: the
// application decides what stopping means and when it is safe, each
// writer decides what it does with what it receives, each provider
// decides what it gives, and a console only looks.
//
// This package has no dependencies. The hub itself, the writers, the
// wire protocol and the visual part live in modules of their own and meet
// here, through these interfaces.
package quevu

import (
	"errors"
	"time"
)

// ErrClosed is returned when something is sent to a hub that no longer
// takes input.
var ErrClosed = errors.New("quevu: hub closed")

// Chunk is the unit of output: what is positioned, sent and asked for.
// A chunk is usually one line of text, but nothing requires it to be.
type Chunk struct {
	// Seq is the position of the chunk inside its log. It starts at 1,
	// grows by one per chunk and is the same for whoever holds the chunk,
	// so a reader can move from recent output to stored output with it.
	Seq uint64
	// Time is when the chunk entered the log.
	Time time.Time
	// Data is the payload. It must not be modified once handed over.
	Data []byte
}

// Entry is what a producer puts into the input channel of a hub: data on
// its way to becoming a chunk. The hub gives it its position.
type Entry struct {
	// Data is the payload, whole: one entry is one chunk. It belongs to
	// the hub once sent and must not be modified afterwards.
	Data []byte
	// Time is when the data was written, for a producer that brings it
	// from somewhere that knew. Zero means now.
	Time time.Time
}

// Input is the input end of a hub, and all a producer knows of it.
//
// Any number of goroutines may send on In. Nobody closes it: a hub that
// stops says so by closing Done, and whoever sends should stop then. An
// Inlet does that for its callers.
type Input interface {
	// In is the channel a producer sends on.
	In() chan<- Entry
	// Done is closed when the hub takes no more input.
	Done() <-chan struct{}
}

// Writer is a destination of a hub: where the hub sends things. It
// receives from its channel and writes, sends or shows what arrives, or
// whatever it is it does. It knows how to do nothing else.
//
// A hub gives every writer a channel of its own and runs it in a
// goroutine of its own, so a writer is never called concurrently. The
// capacity of that channel is the limit of what may wait for the writer,
// and what happens at the limit is the business of the hub, set when the
// writer is connected: the hub waits, and everything else waits with it,
// or the hub skips the chunk for this writer.
type Writer interface {
	// Run receives chunks, in order, until in is closed. It returns when
	// everything received has been dealt with and the writer has released
	// what it held. Returning earlier means the writer has given up: the
	// hub stops sending to it.
	Run(in <-chan Chunk) error
}

// SeqWriter is implemented by writers that know the last position they
// hold, which in practice means the persistent ones. A hub starts
// counting after the highest, so positions stay unique across restarts
// of the application.
type SeqWriter interface {
	LastSeq() (uint64, error)
}

// FailCounter is implemented by writers that can say, while they run,
// how many chunks they could not deal with. A hub shows it next to what
// it knows itself of the writer.
type FailCounter interface {
	// Failed returns the chunks lost so far and the last error.
	Failed() (uint64, error)
}

// Provider gives chunks back, by position. It is all a console needs to
// move forwards and backwards through a log, whoever is behind: the
// short memory of a screen or a reader over a persistent store.
//
// Every method returns chunks in ascending Seq order and at most n of
// them. Fewer, or none, is the provider's right: it decides what it
// hands over, and an empty answer means there is nothing further that
// way as far as it is concerned.
type Provider interface {
	// Tail returns the last n chunks.
	Tail(n int) ([]Chunk, error)
	// Before returns the n chunks that precede seq.
	Before(seq uint64, n int) ([]Chunk, error)
	// After returns the n chunks that follow seq.
	After(seq uint64, n int) ([]Chunk, error)
}

// WriterStats is the view of the channel between a hub and one writer.
// A channel is there to absorb bursts: one that stays full means the
// writer is too slow for the output it receives.
type WriterStats struct {
	Name     string // the name the writer was connected with
	Queued   int    // chunks waiting in the channel
	Capacity int    // capacity of the channel: the limit
	Written  uint64 // chunks handed to the writer
	Skipped  uint64 // chunks skipped for this writer because the channel was full
	Waiting  bool   // the hub is held right now, waiting for this writer
	Waits    uint64 // times the hub has had to wait for this writer
	Failed   uint64 // chunks the writer could not deal with, or that found it gone
	LastErr  error  // the last error of the writer
}

// StatSource is implemented by whatever can show the writers of a log.
type StatSource interface {
	Stats() []WriterStats
}
