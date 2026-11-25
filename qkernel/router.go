package qkernel

import (
	"io"
	"log"
	"sync"
	"time"
)

// LogRouter sends what an application writes with the standard logger to
// a log of its own for each piece, without changing a single call.
//
// An application that grew with log.Printf everywhere has one stream
// where everything mixes. Its lines often say where they come from by
// how they begin: "mailer: sent 3", "payments: declined". A LogRouter
// reads that beginning and hands each message to the writer of its
// channel; a message that begins with nothing known goes to the general
// one.
//
// The writers are io.Writers. An Inlet over a hub is one, so every
// channel can be a log of its own, stored and shown apart.
//
// It is an io.Writer itself: the output of a logger.
type LogRouter struct {
	mu      sync.RWMutex
	general io.Writer
	routes  map[string]io.Writer
	tee     io.Writer
	layout  string
	teeMu   sync.Mutex
	now     func() time.Time
}

// NewLogRouter returns a router whose messages with no known beginning go to
// general. A nil general drops them.
func NewLogRouter(general io.Writer) *LogRouter {
	return &LogRouter{general: general, routes: map[string]io.Writer{}, now: time.Now}
}

// Route sends to w the messages that begin with one of the words given,
// followed by a colon or a blank: Route(w, "mailer") takes "mailer: sent"
// and "mailer queue: 3 left", not "mailers: ..." nor "Error: mailer".
//
// A word is made of lower case letters, digits and underscores, the way
// a package is named. Any other is refused: it could never match.
func (r *LogRouter) Route(w io.Writer, words ...string) error {
	for _, word := range words {
		if word == "" || len(word) > MaxRouteWord || leadingWord([]byte(word+":")) != word {
			return &BadWordError{Word: word}
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, word := range words {
		r.routes[word] = w
	}
	return nil
}

// MaxRouteWord is the longest word a message can begin with to be routed.
const MaxRouteWord = 32

// BadWordError says a word cannot begin a routed message.
type BadWordError struct{ Word string }

func (e *BadWordError) Error() string {
	return "quevu: " + quote(e.Word) + " cannot be routed: lower case letters, digits and underscores only"
}

func quote(s string) string { return `"` + s + `"` }

// Tee also copies every message to w, with the time in front when layout
// is not empty (a layout of the time package). It is how the console of
// the process keeps showing everything, as it did before. A nil w stops
// the copy.
func (r *LogRouter) Tee(w io.Writer, layout string) {
	r.mu.Lock()
	r.tee, r.layout = w, layout
	r.mu.Unlock()
}

// StdLayout is the time the stdLogWriter logger writes by default.
const StdLayout = "2006/01/02 15:04:05"

// Writer returns where a message that begins like p would go.
func (r *LogRouter) writer(p []byte) io.Writer {
	if word := leadingWord(p); word != "" {
		if w := r.routes[word]; w != nil {
			return w
		}
	}
	return r.general
}

// Write receives one message, whole, as the stdLogWriter logger hands it:
// with several lines when the message has them. All of it goes to one
// channel, the one its beginning names.
//
// A channel that fails does not stop the application from logging: the
// error is returned to the logger, which ignores it, and the copy of Tee
// has already been written.
func (r *LogRouter) Write(p []byte) (int, error) {
	r.mu.RLock()
	w, tee, layout := r.writer(p), r.tee, r.layout
	r.mu.RUnlock()
	if tee != nil {
		r.teeMu.Lock()
		if layout != "" {
			buf := r.now().AppendFormat(make([]byte, 0, len(layout)+1+len(p)), layout)
			buf = append(append(buf, ' '), p...)
			tee.Write(buf)
		} else {
			tee.Write(p)
		}
		r.teeMu.Unlock()
	}
	if w == nil {
		return len(p), nil
	}
	if _, err := w.Write(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// leadingWord returns the word a message begins with when a colon or a
// blank follows it, "" otherwise.
func leadingWord(p []byte) string {
	for i, c := range p {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_':
			if i >= MaxRouteWord {
				return ""
			}
		case (c == ':' || c == ' ') && i > 0:
			return string(p[:i])
		default:
			return ""
		}
	}
	return ""
}

// Install makes the router the output of the stdLogWriter logger and takes
// the date and time off its lines: each channel stamps its own. It
// returns the function that puts everything back as it was.
func (r *LogRouter) Install() (restore func()) {
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(r)
	var once sync.Once
	return func() {
		once.Do(func() {
			log.SetOutput(prevOut)
			log.SetFlags(prevFlags)
		})
	}
}

// Logger returns a logger for a package whose lines do not say where
// they come from: every line gets "name: " in front, and so reaches its
// channel once a router is installed. It writes wherever the stdLogWriter
// logger writes at that moment, so it can be made before the router is.
//
// A package uses it in place of importing "log":
//
//	var log = qkernel.Logger("billing")
//
// and its calls stay as they are, since a *log.Logger has the functions
// of the package.
func Logger(name string) *log.Logger {
	return log.New(stdLogWriter{}, name+": ", 0)
}

type stdLogWriter struct{}

func (stdLogWriter) Write(p []byte) (int, error) { return log.Writer().Write(p) }
