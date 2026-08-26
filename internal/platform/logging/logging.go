// Package logging sets up structured logging with log/slog.
package logging

import (
	"io"
	"log/slog"
)

// Options configures a logger.
type Options struct {
	Service string
	Version string
	Level   slog.Level
	Format  string // "text" for humans, "json" for log collectors
}

// New returns a logger that writes to w. Every line carries the service name
// and version, plus any attributes stored in the context with With.
func New(w io.Writer, o Options) *slog.Logger {
	opts := &slog.HandlerOptions{Level: o.Level}
	var h slog.Handler
	if o.Format == "json" {
		h = slog.NewJSONHandler(w, opts)
	} else {
		opts.ReplaceAttr = shortTime
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(contextHandler{h}).With(
		slog.String("service", o.Service),
		slog.String("version", o.Version),
	)
}

// shortTime prints only the time of day. Someone reading a terminal doesn't
// need the date and time zone on every line; a log collector does.
func shortTime(groups []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey && len(groups) == 0 {
		a.Value = slog.StringValue(a.Value.Time().Format("15:04:05.000"))
	}
	return a
}

// ParseLevel parses debug, info, warn or error, in upper or lower case.
func ParseLevel(s string) (slog.Level, error) {
	var l slog.Level
	err := l.UnmarshalText([]byte(s))
	return l, err
}
