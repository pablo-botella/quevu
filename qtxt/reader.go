package qtxt

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"

	"github.com/pablo-botella/linereader"
	"github.com/pablo-botella/quevu/qkernel"
)

// maxLine bounds a single line when reading forwards: a guard against a
// file that is not a log at all.
const maxLine = 16 << 20

// Reader serves the chunks stored in a text file by position. It keeps
// nothing open and nothing in memory between requests: every call opens
// the file, looks at its current size and reads only what it needs. It
// can be used while a Device is appending to the same file.
type Reader struct {
	path string
}

// NewReader returns a provider over the file at path. The file does not
// need to exist yet: a missing file is an empty log.
func NewReader(path string) *Reader { return &Reader{path: path} }

func (r *Reader) open() (*os.File, int64, error) {
	f, err := os.Open(r.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, st.Size(), nil
}

// Tail returns the last n chunks of the file.
func (r *Reader) Tail(n int) ([]qkernel.Chunk, error) {
	f, size, err := r.open()
	if f == nil || n <= 0 {
		if f != nil {
			f.Close()
		}
		return nil, err
	}
	defer f.Close()
	return backward(f, size, n)
}

// Before returns the n chunks that precede seq.
func (r *Reader) Before(seq uint64, n int) ([]qkernel.Chunk, error) {
	f, size, err := r.open()
	if f == nil || n <= 0 {
		if f != nil {
			f.Close()
		}
		return nil, err
	}
	defer f.Close()
	end, err := find(f, size, seq)
	if err != nil {
		return nil, err
	}
	return backward(f, end, n)
}

// After returns the n chunks that follow seq.
func (r *Reader) After(seq uint64, n int) ([]qkernel.Chunk, error) {
	f, size, err := r.open()
	if f == nil || n <= 0 {
		if f != nil {
			f.Close()
		}
		return nil, err
	}
	defer f.Close()
	if seq == ^uint64(0) {
		return nil, nil
	}
	off, err := find(f, size, seq+1)
	if err != nil {
		return nil, err
	}
	return forward(f, off, size, n)
}

// lineStart returns the offset of the first line that starts at or after
// off; size when there is none.
func lineStart(f io.ReaderAt, off, size int64) (int64, error) {
	if off <= 0 {
		return 0, nil
	}
	var buf [4096]byte
	for pos := off - 1; pos < size; {
		want := min(int64(len(buf)), size-pos)
		n, err := f.ReadAt(buf[:want], pos)
		if i := bytes.IndexByte(buf[:n], '\n'); i >= 0 {
			return pos + int64(i) + 1, nil
		}
		if n == 0 {
			if err != nil && err != io.EOF {
				return 0, err
			}
			break
		}
		pos += int64(n)
	}
	return size, nil
}

// seqFrom returns the position of the first well formed, finished line
// that starts at or after the line start off.
func seqFrom(f io.ReaderAt, off, size int64) (seq uint64, ok bool, err error) {
	var line []byte
	var buf [4096]byte
	for pos := off; pos < size; {
		want := min(int64(len(buf)), size-pos)
		n, rerr := f.ReadAt(buf[:want], pos)
		if n == 0 {
			if rerr != nil && rerr != io.EOF {
				return 0, false, rerr
			}
			break
		}
		pos += int64(n)
		rest := buf[:n]
		for {
			i := bytes.IndexByte(rest, '\n')
			if i < 0 {
				// Only the head of a line matters here: the position.
				if len(line) < 64 {
					line = append(line, rest...)
				}
				break
			}
			if len(line) < 64 {
				line = append(line, rest[:i]...)
			}
			if s, good := parseSeq(line); good {
				return s, true, nil
			}
			line = line[:0]
			rest = rest[i+1:]
		}
	}
	return 0, false, nil
}

// find returns the offset of a line start such that every chunk before it
// has a position below target and the first chunk from it on, if any, has
// target or more. Positions grow along the file, so it bisects.
func find(f io.ReaderAt, size int64, target uint64) (int64, error) {
	lo, hi := int64(0), size
	for lo < hi {
		mid := lo + (hi-lo)/2
		ls, err := lineStart(f, mid, size)
		if err != nil {
			return 0, err
		}
		seq, ok, err := seqFrom(f, ls, size)
		if err != nil {
			return 0, err
		}
		if !ok || seq >= target {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lineStart(f, lo, size)
}

// backward returns the last n chunks that end before the line start end.
func backward(f io.ReaderAt, end int64, n int) ([]qkernel.Chunk, error) {
	for win := int64(64 << 10); ; win *= 4 {
		start := max(end-win, 0)
		buf := make([]byte, end-start)
		if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
			return nil, err
		}
		lines := bytes.Split(buf, []byte{'\n'})
		// What follows the last line end is empty, or the unfinished
		// tail of the file.
		lines = lines[:len(lines)-1]
		if start > 0 && len(lines) > 0 {
			// The window opens in the middle of a line.
			lines = lines[1:]
		}
		chunks := make([]qkernel.Chunk, 0, min(len(lines), n))
		// Damaged lines do not count: the window grows if they leave
		// the answer short.
		for i := len(lines) - 1; i >= 0 && len(chunks) < n; i-- {
			if c, ok := parseLine(lines[i]); ok {
				chunks = append(chunks, c)
			}
		}
		if len(chunks) >= n || start == 0 {
			for i, j := 0, len(chunks)-1; i < j; i, j = i+1, j-1 {
				chunks[i], chunks[j] = chunks[j], chunks[i]
			}
			return chunks, nil
		}
	}
}

// forward returns the first n chunks from the line start off.
func forward(f io.ReaderAt, off, size int64, n int) ([]qkernel.Chunk, error) {
	if off >= size {
		return nil, nil
	}
	lr := linereader.NewLineReader(io.NewSectionReader(f, off, size-off), 64<<10, maxLine)
	var chunks []qkernel.Chunk
	for len(chunks) < n {
		line, err := lr.ReadLine()
		if err != nil {
			if err == io.EOF {
				break
			}
			return chunks, err
		}
		if lr.LastEolType == linereader.EolEof {
			// The unfinished tail of the file: a line still being
			// written, or one left by a process that died.
			break
		}
		if c, ok := parseLine(line); ok {
			chunks = append(chunks, c)
		}
	}
	return chunks, nil
}

var _ qkernel.Provider = (*Reader)(nil)
