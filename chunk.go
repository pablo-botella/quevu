// Package quevu holds the contracts of the quevu control panel and the
// coordination of an application's lifecycle.
//
// quevu ("queue view") gives an application an orderly start and stop of
// the pieces it is made of, and a log mechanism that keeps the output of
// each piece apart, queues it towards every destination at that
// destination's own speed and serves it back by position.
//
// quevu does not solve the application's problems, it hands them out: the
// application decides what stopping means and when it is safe, a device
// decides how fast and how safely it persists, and a console only looks.
//
// This package has no dependencies. The log itself, the persistent
// devices, the wire protocol and the visual part live in modules of their
// own and meet here, through these interfaces.
package quevu

import "time"

// Chunk is the unit of output: what is queued, positioned and asked for.
// A chunk is usually one line of text, but nothing requires it to be.
type Chunk struct {
	// Seq is the position of the chunk inside its log. It starts at 1,
	// grows by one per chunk and is shared by every source of the same
	// log, so a reader can move from live output to stored output with it.
	Seq uint64
	// Time is when the chunk entered the log.
	Time time.Time
	// Data is the payload. It must not be modified once handed over.
	Data []byte
}

// Provider serves chunks by position. It is all a console needs to move
// forwards and backwards through a log, whoever is behind: the log itself
// (live output and its short memory) or a reader over a persistent store.
//
// Every method returns chunks in ascending Seq order and at most n of them.
type Provider interface {
	// Tail returns the last n chunks.
	Tail(n int) ([]Chunk, error)
	// Before returns the n chunks that precede seq.
	Before(seq uint64, n int) ([]Chunk, error)
	// After returns the n chunks that follow seq.
	After(seq uint64, n int) ([]Chunk, error)
}

// Device is a persistent destination. A log queues chunks for it and
// writes them in batches from a goroutine of its own, so a device is never
// called concurrently and never slows down another destination.
//
// How fast and how safely a batch is stored is the device's own business.
type Device interface {
	// Write stores a batch of consecutive chunks.
	Write(batch []Chunk) error
	// Close flushes and releases the device. It is called once, after the
	// last Write.
	Close() error
}

// SeqDevice is implemented by devices that know the last position they
// hold. A log starts counting after the highest one, so positions stay
// unique across restarts of the application.
type SeqDevice interface {
	LastSeq() (uint64, error)
}

// DeviceStats is the view of the queue between a log and one device. A
// queue is there to absorb bursts: one that keeps growing means the device
// is too slow for the output it receives and should be replaced.
type DeviceStats struct {
	Queued   int    // chunks waiting to be written
	Capacity int    // capacity of the queue
	Written  uint64 // chunks stored
	Dropped  uint64 // chunks discarded because the queue was full
	Failed   uint64 // chunks lost in a failed Write
	LastErr  error  // error of the last failed Write
}

// StatSource is implemented by sources that can show their device queues.
type StatSource interface {
	Stats() []DeviceStats
}
