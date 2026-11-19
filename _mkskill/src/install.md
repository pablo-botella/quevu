---
mkskill:
  pos: 50
  in: readme
---

## Install

```
go get github.com/pablo-botella/quevu
```

This module has no dependencies. It holds the contracts and the lifecycle;
everything else is a module of its own, taken only by who needs it:

| Module | What it is |
|---|---|
| `quevu` | Contracts and lifecycle coordination (this module) |
| `quevu-log` | The non-visual log: positions, and a queue per consumer |
| `quevu-panel` | The panel as a volatile consumer: the screen of a log |
| `quevu-txt` | Persistent consumer over a text file, and its stored-data provider |
| `quevu-sqlite` | Persistent consumer over SQLite, and its stored-data provider |
| `quevu-wire` | A panel served over a local connection, for a separate controller |
| `quevu-ui` | The window, as a dynamic library with a C interface (Rust) |
| `quevu-ui-go` | Go binding to that library, without cgo |
| `quevu-ctl` | `quevuctl`, the panel for an application running elsewhere |
| `quevu-demo` | A small application that shows all of the above at work |
