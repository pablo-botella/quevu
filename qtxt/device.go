package qtxt

import (
	"os"

	"github.com/pablo-botella/quevu/qkernel"
)

// Options tunes a Device.
type Options struct {
	// Sync forces every batch to the disk before Write returns. It is the
	// safe and slow lane: without it a batch is in the hands of the
	// operating system when Write returns, which survives the death of
	// the process but not a power cut.
	Sync bool
}

// Device is a persistent writer: it appends the chunks of a log to a
// text file.
type Device struct {
	fails qkernel.Failures // the body of Run, and what it could not write

	f    *os.File
	sync bool
	last uint64
	buf  []byte
}

// Open opens the file for appending, creating it if needed. A last line
// left unfinished by a process that died is closed off, so what comes
// next starts on a line of its own; readers skip the damaged line.
func Open(path string, opt Options) (*Device, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if size := st.Size(); size > 0 {
		var b [1]byte
		if _, err := f.ReadAt(b[:], size-1); err != nil {
			f.Close()
			return nil, err
		}
		if b[0] != '\n' {
			if _, err := f.Write([]byte{'\n'}); err != nil {
				f.Close()
				return nil, err
			}
			size++
		}
		tail, err := backward(f, size, 1)
		if err != nil {
			f.Close()
			return nil, err
		}
		d := &Device{f: f, sync: opt.Sync}
		if len(tail) > 0 {
			d.last = tail[0].Seq
		}
		return d, nil
	}
	return &Device{f: f, sync: opt.Sync}, nil
}

// Write appends a batch with a single write to the file.
func (d *Device) Write(batch []qkernel.Chunk) error {
	d.buf = d.buf[:0]
	for _, c := range batch {
		d.buf = appendLine(d.buf, c)
	}
	if _, err := d.f.Write(d.buf); err != nil {
		return err
	}
	if n := len(batch); n > 0 {
		d.last = batch[n-1].Seq
	}
	if d.sync {
		return d.f.Sync()
	}
	return nil
}

// Close syncs and closes the file.
func (d *Device) Close() error {
	err := d.f.Sync()
	if cerr := d.f.Close(); err == nil {
		err = cerr
	}
	return err
}

// LastSeq returns the last position found in the file when it was opened,
// so the log continues counting from there.
func (d *Device) LastSeq() (uint64, error) { return d.last, nil }

var (
	_ qkernel.Writer    = (*Device)(nil)
	_ qkernel.SeqWriter = (*Device)(nil)
)

// Run receives from the channel the hub gave this writer until it is
// closed, writing what arrives in batches, and closes the writer at the
// end. It is what makes it a qkernel.Writer.
func (d *Device) Run(in <-chan qkernel.Chunk) error { return d.fails.Drain(in, 0, d) }

// Failed returns the chunks lost so far in failed batches, and the last
// error.
func (d *Device) Failed() (uint64, error) { return d.fails.Failed() }
