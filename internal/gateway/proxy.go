package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/fiston-user/payments/internal/platform/httpx"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// newProxy returns a reverse proxy to the service at target, like
// http://localhost:8082.
func newProxy(target *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		// Rewrite drops X-Forwarded headers a client made up.
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
			pr.Out.Header.Set(httpx.RequestIDHeader, httpx.RequestIDFrom(pr.In.Context()))
		},
		// A span for each call upstream, and the trace context in its headers.
		Transport: otelhttp.NewTransport(&http.Transport{
			DialContext: (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			// The default keeps only two idle connections per host.
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   100,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
		}),
		// Our RequestID middleware already set it: don't send it twice.
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Del(httpx.RequestIDHeader)
			return nil
		},
		ErrorHandler: proxyError,
		BufferPool:   buffers,
	}
}

// buffers lends every proxy a 32 KB buffer to copy a response with, and takes
// it back afterwards. Without one, each response gets a buffer of its own.
var buffers = &bufferPool{sync.Pool{New: func() any { b := make([]byte, 32<<10); return &b }}}

// bufferPool is a sync.Pool, as an httputil.BufferPool. It keeps pointers to
// slices: putting a slice itself in a sync.Pool allocates, every time.
type bufferPool struct{ pool sync.Pool }

func (p *bufferPool) Get() []byte  { return *p.pool.Get().(*[]byte) }
func (p *bufferPool) Put(b []byte) { p.pool.Put(&b) }

// proxyError answers in our JSON format when the service can't.
func proxyError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		httpx.Error(w, r, errUpstreamTimeout)
	case errors.Is(err, context.Canceled):
		// The client hung up: no one to answer.
	default:
		// The details, like an internal address, are for our logs only.
		slog.WarnContext(r.Context(), "upstream unavailable", "err", err)
		httpx.Error(w, r, errUpstreamDown)
	}
}
