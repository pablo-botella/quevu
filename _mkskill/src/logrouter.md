---
mkskill:
  pos: 120
  in: "*"
---

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
