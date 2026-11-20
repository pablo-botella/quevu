---
mkskill:
  pos: 50
  in: readme
---

## Install

```
go get github.com/pablo-botella/quevu-kernel
```

This module has no dependencies. It holds the contracts and the lifecycle;
everything else is a module of its own, taken only by who needs it:

| Module | What it is |
|---|---|
| `quevu-kernel` | Contracts and lifecycle coordination (this module) |
| `quevu-hub` | The control element: collects, numbers and distributes, a queue per consumer |
| `quevu-panel` | The panel as a volatile consumer: the screen of a log |
| `quevu-txt` | Persistent consumer over a text file, and its stored-data provider |
| `quevu-sql` | Persistent consumers over SQL databases (SQLite, MySQL), and their stored-data providers |
| `quevu-syslog` | The system log as a queue: a consumer that writes to it and an input that reads from it (Windows Event Log) |
| `quevu-logger` | Input: what a Go application writes with its standard logger, split into channels |
| `quevu-pipe` | Input: a program's standard output, through a pipe, into a log |
| `quevu-sqs` | Amazon SQS, as a consumer and as an input |
| `quevu-pubsub` | Google Cloud Pub/Sub, as a consumer and as an input |
| `quevu-forward` | A log sent to another machine: a forwarder with a durable spool, and its receiver |
| `quevu-wire` | A panel served over a local connection, for a separate controller |
| `quevu-ui` | The window, as a dynamic library with a C interface (Rust) |
| `quevu-ui-go` | Go binding to that library, without cgo |
| `quevu-ctl` | `quevuctl`, the panel for an application running elsewhere |
| `quevu-demo` | A small application that shows all of the above at work |