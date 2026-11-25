---
mkskill:
  pos: 125
  in: "*"
---

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
