// Package health serves a service's probes. Liveness, on /healthz: the process
// is up, and answering. Readiness, on /readyz: it can do its job right now, so
// it should be sent traffic.
package health

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"slices"
	"sync/atomic"
	"time"

	"github.com/fiston-user/payments/internal/platform/httpx"
)

// A Check reports whether something the service needs is reachable.
type Check func(ctx context.Context) error

// Probes answers /healthz and /readyz.
type Probes struct {
	logger   *slog.Logger
	checks   map[string]Check
	draining atomic.Bool
}

// New returns probes whose readiness runs checks, each under its name.
func New(logger *slog.Logger, checks map[string]Check) *Probes {
	return &Probes{logger: logger, checks: checks}
}

// Register serves both probes on mux.
func (p *Probes) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", p.live)
	mux.HandleFunc("GET /readyz", p.ready)
}

// live checks nothing else, on purpose. If it checked the database, an outage
// would get every instance restarted, which fixes nothing, and makes it worse.
func (p *Probes) live(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
}

// ready runs every check, with one deadline for all of them.
func (p *Probes) ready(w http.ResponseWriter, r *http.Request) {
	if p.draining.Load() {
		httpx.WriteJSON(w, r, http.StatusServiceUnavailable, map[string]string{"status": "draining"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	var failed []string
	for _, name := range slices.Sorted(maps.Keys(p.checks)) {
		if err := p.checks[name](ctx); err != nil {
			p.logger.WarnContext(ctx, "not ready", "check", name, "err", err)
			failed = append(failed, name)
		}
	}
	if len(failed) > 0 {
		body := map[string]any{"status": "unavailable", "failed": failed}
		httpx.WriteJSON(w, r, http.StatusServiceUnavailable, body)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]string{"status": "ready"})
}

// Drain makes readiness fail, and then waits for delay: long enough for load
// balancers to notice, and stop sending new requests, before the server stops
// accepting them.
func (p *Probes) Drain(delay time.Duration) {
	p.draining.Store(true)
	if delay > 0 {
		p.logger.Info("draining: readiness fails from now on", "delay", delay)
		time.Sleep(delay)
	}
}

// Probe asks the service listening on addr, like :8081, whether it's ready.
// It's a container's health check: our images have no curl, and no shell to
// run one, so the binary checks itself.
func Probe(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/readyz")
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: %s", resp.Status)
	}
	return nil
}
