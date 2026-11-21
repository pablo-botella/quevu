---
mkskill:
  pos: 50
  in: readme
---

## Install

```
go get github.com/pablo-botella/quevu
```

This module has no dependencies. It holds the contracts, the hub and the
lifecycle; everything else is a module of its own, taken only by who needs
it:

| Module | What it is |
|---|---|
| `quevu` | Contracts, the hub (`quevu.QHub`), the router of the standard logger (`quevu.LogRouter`) and lifecycle coordination (this module) |
| `quevu-panel` | The panel as a volatile writer: the screen of a log |
| `quevu-txt` | Persistent writer over a text file, and its stored-data provider |
| `quevu-sql` | Persistent writers over SQL databases (SQLite, MySQL), and their stored-data providers |
| `quevu-syslog` | The system log as a queue: a writer that writes to it and an input that reads from it (Windows Event Log) |
| `quevu-pipe` | Input: a program's standard output, through a pipe, into a log |
| `quevu-sqs` | Amazon SQS, as a writer and as an input |
| `quevu-pubsub` | Google Cloud Pub/Sub, as a writer and as an input |
| `quevu-forward` | A log sent to another machine: a forwarder with a durable spool, and its receiver |
| `quevu-wire` | A panel served over a local connection, for a separate controller |
| `quevu-ui` | The window, as a dynamic library with a C interface (Rust) |
| `quevu-ui-go` | Go binding to that library, without cgo |
| `quevu-ctl` | `quevuctl`, the panel for an application running elsewhere |
| `quevu-examples` | A small application that shows all of the above at work |