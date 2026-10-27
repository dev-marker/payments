package mtls_test

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fiston-user/payments/internal/platform/mtls"
	"github.com/fiston-user/payments/internal/platform/mtls/devca"
)

// issue makes a new CA, and credentials from it for each name.
func issue(t *testing.T, names ...string) map[string]*mtls.Config {
	t.Helper()
	ca, err := devca.New(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	dir, creds := t.TempDir(), map[string]*mtls.Config{}
	for _, name := range names {
		if err := ca.Write(dir, name); err != nil {
			t.Fatal(err)
		}
		if creds[name], err = mtls.Load(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	return creds
}

// TestAllow calls an internal API that only ops may call, as the transfer
// service's review API is.
func TestAllow(t *testing.T) {
	t.Parallel()
	creds := issue(t, "transfer", "gateway", "ops")
	srv := httptest.NewUnstartedServer(mtls.Allow("ops")(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, mtls.PeerFrom(r.Context())) })))
	srv.TLS = creds["transfer"].Server()
	srv.StartTLS()
	t.Cleanup(srv.Close)

	client := func(name string) *tls.Config { return creds[name].Client("transfer") }
	anonymous := client("ops")
	anonymous.Certificates = nil
	forged := issue(t, "ops")["ops"].Client("transfer") // the right name, from someone else's CA
	forged.RootCAs = anonymous.RootCAs
	tests := []struct {
		name   string
		config *tls.Config
		want   string // the status and body, or the error
	}{
		{"no certificate", anonymous, "certificate required"},
		{"another CA", forged, "unknown certificate authority"},
		{"another service", client("gateway"), "403"},
		{"ops", client("ops"), "200 ops"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &http.Client{Transport: &http.Transport{TLSClientConfig: tt.config}}
			resp, err := c.Get(srv.URL)
			got := fmt.Sprint(err)
			if err == nil {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				got = fmt.Sprintf("%d %s", resp.StatusCode, body)
			}
			if !strings.Contains(got, tt.want) {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}
