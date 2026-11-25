// Package qtxt stores a quevu log in a plain text file and reads it back
// by position.
//
// It has two halves that do not know each other at run time:
//
//   - Device is a persistent writer of a log (qkernel.Writer). It only
//     appends, and gives nothing back.
//   - Reader is the stored-data provider a console reads history from
//     (qkernel.Provider). It opens the file on its own for every request,
//     so reading history never competes with the writer, and works with
//     the application stopped.
//
// # File format
//
// One chunk per line, three fields separated by tabs, lines ended by LF:
//
//	<seq> TAB <time, RFC 3339 with nanoseconds, UTC> TAB <data>
//
// In the data, backslash, LF and CR are written as \\, \n and \r, so a
// chunk never spans lines and the file stays readable with any tool.
// Positions grow along the file, which is what lets the reader find one
// by bisection instead of reading the file from the start.
package qtxt

import (
	"bytes"
	"strconv"
	"time"

	"github.com/pablo-botella/quevu/qkernel"
)

// appendLine appends the line of one chunk to buf.
func appendLine(buf []byte, c qkernel.Chunk) []byte {
	buf = strconv.AppendUint(buf, c.Seq, 10)
	buf = append(buf, '\t')
	buf = c.Time.UTC().AppendFormat(buf, time.RFC3339Nano)
	buf = append(buf, '\t')
	for _, b := range c.Data {
		switch b {
		case '\\':
			buf = append(buf, '\\', '\\')
		case '\n':
			buf = append(buf, '\\', 'n')
		case '\r':
			buf = append(buf, '\\', 'r')
		default:
			buf = append(buf, b)
		}
	}
	return append(buf, '\n')
}

// parseSeq reads only the position of a line.
func parseSeq(line []byte) (uint64, bool) {
	i := bytes.IndexByte(line, '\t')
	if i <= 0 {
		return 0, false
	}
	seq, err := strconv.ParseUint(string(line[:i]), 10, 64)
	return seq, err == nil
}

// parseLine turns a line, without its line end, back into a chunk. The
// data of the chunk is a copy: it does not alias line.
func parseLine(line []byte) (qkernel.Chunk, bool) {
	i := bytes.IndexByte(line, '\t')
	if i <= 0 {
		return qkernel.Chunk{}, false
	}
	j := bytes.IndexByte(line[i+1:], '\t')
	if j < 0 {
		return qkernel.Chunk{}, false
	}
	j += i + 1
	seq, err := strconv.ParseUint(string(line[:i]), 10, 64)
	if err != nil {
		return qkernel.Chunk{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, string(line[i+1:j]))
	if err != nil {
		return qkernel.Chunk{}, false
	}
	raw := line[j+1:]
	data := make([]byte, 0, len(raw))
	for k := 0; k < len(raw); k++ {
		b := raw[k]
		if b == '\\' && k+1 < len(raw) {
			k++
			switch raw[k] {
			case 'n':
				b = '\n'
			case 'r':
				b = '\r'
			default:
				b = raw[k]
			}
		}
		data = append(data, b)
	}
	return qkernel.Chunk{Seq: seq, Time: t, Data: data}, true
}
