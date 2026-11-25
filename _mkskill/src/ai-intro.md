---
mkskill:
  pos: 5
  in: ai*
---

# quevu - agent notes

Go module `github.com/pablo-botella/quevu`. The module has no package
at its root: the kernel is package `quevu/qkernel`, and next to it are
`quevu/qpanel`, `quevu/qtxt` and `quevu/qui`. One tag versions them all.
In `qkernel` live the hub (`QHub`), the contracts of the quevu control panel and
the coordination of an application's lifecycle. No dependencies. Every other `quevu-*` module imports this one and none is
imported by it.

Design principle to respect in any change: quevu hands problems out, it
does not solve them. The application decides what stopping means, a
writer decides what it does with what it receives, a provider decides
what it gives back, a console only looks. Do not move a decision to a
piece it does not belong to, and do not let a piece reach into another:
they meet through the interfaces here and nowhere else.
