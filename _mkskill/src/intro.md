---
mkskill:
  pos: 10
  in: readme
---

# quevu-kernel

The kernel of **quevu**, a control panel for Go applications (the name
comes from *queue view*): the contracts every other `quevu-*` module is
built on, and nothing else. The package is named `quevu`:

```go
import quevu "github.com/pablo-botella/quevu-kernel"
```

An application made of several pieces (a web server, a mailer, a periodic
task, a group of workers) has two problems that a console window does not
solve:

- **It cannot simply be killed.** Each piece has to be told to stop, finish
  what it has in hand, and only then may the process exit.
- **Its output comes out mixed.** Everything is written to the same console
  and nobody can follow one piece on its own.

quevu gives the application an orderly start and stop, a log per piece that
is sent to every writer at that writer's own speed, and a small window
with the switches and the screens.

```go
panel := quevu.New()
panel.Add("web", webServer)                 // anything with Start and Stop
panel.Add("mailer", quevu.Go(runMailer))    // or a function that runs until told

// Start everything, wait for Ctrl+C or the stop order of the system,
// stop everything in reverse order, close the logs last.
if err := panel.Run(context.Background()); err != nil {
	log.Fatal(err)
}
```

quevu does not solve the application's problems, it hands them out: the
application decides what stopping means and when it is safe, each writer
decides what it does with what it receives, each provider decides what it
gives back, and a console only looks.
