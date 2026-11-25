---
mkskill:
  pos: 50
  in: readme
---

## Install

```
go get github.com/pablo-botella/quevu
```

The module has no package at its root: it is four packages versioned
together, and a program compiles only the ones it imports. Package
`quevu/qkernel` imports only the standard library. It holds the
contracts, the hub and the lifecycle. The module has one dependency, `linereader`, used by `quevu/qtxt` to
read its files. One package, `quevu/qui`, the window (Windows, Linux and macOS; the
macOS one is pending to test on a Mac), is partly C
and needs cgo to be built; nothing else does, so a program that shows no
window builds with no C compiler. Everything else is a
module of its own, taken only by who needs it:

| Module | What it is |
|---|---|
| `quevu` | `quevu/qkernel`: contracts, the hub (`qkernel.QHub`), the router of the standard logger (`qkernel.LogRouter`) and lifecycle coordination; `quevu/qpanel`, the screen of a log; `quevu/qtxt`, a log in a text file and its stored-data provider; and `quevu/qui`, the window, built into the executable (this module) |
| `quevu-sql` | Persistent writers over SQL databases (SQLite, MySQL), and their stored-data providers |
| `quevu-syslog` | The system log as a queue: a writer that writes to it and an input that reads from it (Windows Event Log) |
| `quevu-pipe` | Input: a program's standard output, through a pipe, into a log |
| `quevu-sqs` | Amazon SQS, as a writer and as an input |
| `quevu-pubsub` | Google Cloud Pub/Sub, as a writer and as an input |
| `quevu-forward` | A log sent to another machine: a forwarder with a durable spool, and its receiver |
| `quevu-wire` | A panel served over a local connection, for a separate controller |
| `quevu-examples` | A small application that shows all of the above at work, and `quevuctl`, the panel for an application running elsewhere |