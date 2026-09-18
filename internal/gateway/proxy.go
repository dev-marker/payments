package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/fiston-user/payments/internal/platform/httpx"
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
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			// The default keeps only two idle connections per host.
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   100,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
		},
		// Our RequestID middleware already set it: don't send it twice.
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Del(httpx.RequestIDHeader)
			return nil
		},
		ErrorHandler: proxyError,
	}
}

// proxyError answers in our JSON format when the service can't.
func proxyError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		httpx.Error(w, r, errUpstreamTimeout)
	case errors.Is(err, context.Canceled):
		// The client hung up: no one to answer.
	default:
		httpx.Error(w, r, fmt.Errorf("%w: %v", errUpstreamDown, err))
	}
}
