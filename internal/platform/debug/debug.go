// Package debug serves Go's profiler, net/http/pprof, to our ops tools: CPU
// and allocation profiles, goroutine dumps and execution traces, from a
// running service. Only ever behind mTLS, on an internal port.
package debug

import (
	"net/http"
	"net/http/pprof"
)

// Handler serves the profiler under /internal/debug/pprof/. Importing
// net/http/pprof also registers it on http.DefaultServeMux: none of our
// servers use that mux, and none ever should.
func Handler() http.Handler {
	mux := http.NewServeMux()
	// A CPU profile, or an execution trace, of the next ?seconds=.
	mux.HandleFunc("GET /internal/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /internal/debug/pprof/trace", pprof.Trace)
	// Every other profile by name, like allocs or goroutine. Index looks for
	// them under /debug/pprof/, so it gets the path without /internal.
	mux.Handle("GET /internal/debug/pprof/", http.StripPrefix("/internal", http.HandlerFunc(pprof.Index)))
	return mux
}
