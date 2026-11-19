---
mkskill:
  pos: 5
  in: ai*
---

# quevu - agent notes

Go package `github.com/pablo-botella/quevu`: the contracts of the quevu
control panel and the coordination of an application's lifecycle. No
dependencies. Every other `quevu-*` module imports this one and none is
imported by it.

Design principle to respect in any change: quevu hands problems out, it
does not solve them. The application decides what stopping means, a device
decides speed against safety, a console only looks. Do not move a decision
into the panel that belongs to one of those.
