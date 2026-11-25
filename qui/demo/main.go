// The demo is a program with the quevu window inside it: build it and
// the one file that comes out is all there is to copy.
//
//	go build -o demo.exe ./demo
//
// It shows a log that keeps talking and a table with two controls, one
// of which can be started and stopped.
package main

import (
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"time"

	"github.com/pablo-botella/quevu/qkernel"
	"github.com/pablo-botella/quevu/qpanel"

	"github.com/pablo-botella/quevu/qui"
)

func main() {
	// A log kept in memory, and where to write to it.
	screen := qpanel.New("log")
	hub, err := qkernel.NewQHub("log", qkernel.WithWriterOptions(screen, qkernel.WriterOptions{Name: "screen"}))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer hub.Close()
	screen.ShowStats(hub)
	var out io.Writer = qkernel.NewInlet(hub)

	win := qui.NewWindow("quevu, built in")
	win.OpenSource(qkernel.Join(screen, qkernel.Roles{}), qui.Weight(10)).ShowTime(true)

	services := win.OpenControls("Services", qui.Weight(100))
	clock := services.Add("Clock")
	clock.Set(qui.State{Color: qui.Green, Text: "says the time every second"})

	// The ticker is a control with two states and a verb for each.
	var running atomic.Bool
	ticker := services.Add("Ticker")
	var show func()
	show = func() {
		if running.Load() {
			ticker.Set(qui.State{Color: qui.Blue, Blink: true, Text: "ticking", Verbs: []qui.Verb{
				{Label: "Stop", Do: func() { running.Store(false); show() }},
			}})
		} else {
			ticker.Set(qui.State{Color: qui.Gray, Hollow: true, Text: "stopped", Verbs: []qui.Verb{
				{Label: "Start", Do: func() { running.Store(true); show() }},
			}})
		}
	}
	show()

	win.Menu("&Quit").Add("&Now", win.Quit)
	win.OnClose(win.Quit)

	go func() {
		for n := 1; ; n++ {
			time.Sleep(time.Second)
			fmt.Fprintf(out, "it is %s\n", time.Now().Format("15:04:05"))
			if running.Load() {
				fmt.Fprintf(out, "\x1b[33mtick %d\x1b[0m\n", n)
			}
		}
	}()

	if err := win.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
