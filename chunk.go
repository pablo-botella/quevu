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
//   - A hub collects what the application writes and distributes it to
//     its consumers. That is all it knows: it never answers a query.
//     What flows through one hub is a log, the output of one piece.
//   - A Consumer receives chunks and does with them what it sees fit. It
//     may be persistent (a file, a database) or volatile (the panel).
//   - A Provider gives chunks back, by position. That something consumes
//     the output does not mean anything returns it: a provider is a piece
//     of its own, attached on purpose, and it decides what it hands over.
//   - A console keeps what it shows, and asks providers for the rest: an
//     immediate one for what is recent, a historical one for what is
//     stored.
//
// quevu does not solve the application's problems, it hands them out: the
// application decides what stopping means and when it is safe, each
// consumer decides what it does with what it receives, each provider
// decides what it gives, and a console only looks.
//
// This package has no dependencies. The hub itself, the consumers, the
// wire protocol and the visual part live in modules of their own and meet
// here, through these interfaces.
package quevu

import (
	"io"
	"time"
)

var _ io.Writer = Intake(nil)

// Chunk is the unit of output: what is queued, positioned and asked for.
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

// Intake is the input end of a hub: where whatever reads data from
// somewhere (a pipe, a queue) puts it, so that it follows the normal way
// to the consumers. It is all an input piece knows of a hub.
type Intake interface {
	// Write takes a stream of text: every line becomes a chunk.
	Write(p []byte) (int, error)
	// Append takes data as one chunk, whatever it contains, and returns
	// its position. When it returns without error the hub has accepted
	// the data, which is the moment an input may confirm it to wherever
	// it came from.
	Append(data []byte) (uint64, error)
}

// Consumer is a destination of a hub. The hub queues chunks for it and
// hands them over in batches from a goroutine of its own, so a consumer
// is never called concurrently and never slows down another one.
//
// What a consumer does with a batch is its own business: store it safely
// and slowly, store it fast and at some risk, keep the last few for a
// screen, or throw it away. Persistent or volatile is a property of the
// consumer, not something the hub knows.
type Consumer interface {
	// Write takes a batch of consecutive chunks.
	Write(batch []Chunk) error
	// Close flushes and releases the consumer. It is called once, after
	// the last Write.
	Close() error
}

// SeqConsumer is implemented by consumers that know the last position
// they hold, which in practice means the persistent ones. A hub starts
// counting after the highest, so positions stay unique across restarts
// of the application.
type SeqConsumer interface {
	LastSeq() (uint64, error)
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

// ConsumerStats is the view of the queue between a hub and one consumer.
// A queue is there to absorb bursts: one that keeps growing means the
// consumer is too slow for the output it receives and should be replaced.
type ConsumerStats struct {
	Queued   int    // chunks waiting to be handed over
	Capacity int    // capacity of the queue
	Written  uint64 // chunks the consumer took
	Dropped  uint64 // chunks discarded because the queue was full
	Failed   uint64 // chunks lost in a failed Write
	LastErr  error  // error of the last failed Write
}

// StatSource is implemented by whatever can show the queues of a log.
type StatSource interface {
	Stats() []ConsumerStats
}
