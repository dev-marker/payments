package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// BenchmarkProxy sends requests through a proxy to a service that answers
// with a small JSON body, like most of ours do.
func BenchmarkProxy(b *testing.B) {
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"0b5c7a8e-4f5e-4d6c-9a62-2f7a1c3e9d10","state":"pending"}`)
	}))
	b.Cleanup(svc.Close)
	target, _ := url.Parse(svc.URL)
	proxy := newProxy(target)
	for b.Loop() {
		proxy.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/transfers/42", nil))
	}
}
