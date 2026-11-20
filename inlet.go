package quevu

import (
	"bytes"
	"io"
	"sync"
	"time"
)

// Intake is what a piece that reads data from somewhere (a pipe, a queue)
// needs in order to put it into a hub. An Inlet is the usual one.
type Intake interface {
	// Write takes a stream of text: every line becomes a chunk.
	Write(p []byte) (int, error)
	// Append takes data as one chunk, whatever it contains. When it
	// returns without error the data is in the input of the hub, which is
	// the moment a producer may confirm it to wherever it came from.
	Append(data []byte) error
}

// TimedIntake is an Intake that can be told when the data was written.
// It is for producers that bring data from somewhere that knew: what a
// program wrote on another machine an hour ago should not look as if it
// had been written now.
type TimedIntake interface {
	Intake
	// AppendAt is Append with the time the data was originally written.
	AppendAt(data []byte, at time.Time) error
}

// Inlet is a producer's way into a hub for code that does not want to
// deal with a channel: an io.Writer, so the standard log package,
// log/slog or fmt.Fprintf can write to it, and an Intake. It only sends
// on the input channel of the hub.
//
// An Inlet may be used from several goroutines, and several inlets may
// feed the same hub. Sending waits when the hub is held by a writer that
// has reached its limit: that is what slows a producer down.
type Inlet struct {
	in   chan<- Entry
	done <-chan struct{}

	mu      sync.Mutex
	partial []byte
}

// NewInlet makes a way into a hub.
func NewInlet(to Input) *Inlet {
	return &Inlet{in: to.In(), done: to.Done()}
}

func (i *Inlet) send(e Entry) error {
	select {
	case <-i.done:
		return ErrClosed
	default:
	}
	select {
	case i.in <- e:
		return nil
	case <-i.done:
		return ErrClosed
	}
}

// Write splits p into lines and sends each one. A trailing piece without
// a line end waits for the rest of its line, or for Flush.
//
// Callers that write a line in several calls from several goroutines will
// see them interleaved, as with any shared writer.
func (i *Inlet) Write(p []byte) (int, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	rest := p
	for {
		n := bytes.IndexByte(rest, '\n')
		if n < 0 {
			break
		}
		line := rest[:n]
		rest = rest[n+1:]
		if len(i.partial) > 0 {
			line = append(i.partial, line...)
			i.partial = nil
		} else {
			line = bytes.Clone(line)
		}
		if err := i.send(Entry{Data: bytes.TrimSuffix(line, []byte{'\r'})}); err != nil {
			return 0, err
		}
	}
	if len(rest) > 0 {
		i.partial = append(i.partial, rest...)
	}
	return len(p), nil
}

// Flush sends a line that was left unfinished. It is for the end of the
// producer: a hub cannot do it, because it never sees the half line.
func (i *Inlet) Flush() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if len(i.partial) == 0 {
		return nil
	}
	line := i.partial
	i.partial = nil
	return i.send(Entry{Data: line})
}

// Append sends data as one chunk, whatever it contains. It is the way in
// for output that is not line oriented.
func (i *Inlet) Append(data []byte) error {
	return i.send(Entry{Data: bytes.Clone(data)})
}

// AppendAt is Append for data that was written earlier, somewhere else:
// the chunk keeps the time it is given instead of the time it arrives.
func (i *Inlet) AppendAt(data []byte, at time.Time) error {
	return i.send(Entry{Data: bytes.Clone(data), Time: at})
}

var (
	_ io.Writer   = (*Inlet)(nil)
	_ TimedIntake = (*Inlet)(nil)
)
