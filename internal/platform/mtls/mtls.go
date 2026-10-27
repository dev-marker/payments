// Package mtls is mutual TLS between our services. Each one proves who it is
// with a certificate from our CA, with its name in it, and checks who's
// calling the same way: being inside our network proves nothing.
package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
)

// Config is a service's credentials: its certificate and private key, and
// the CA that every certificate it accepts must come from.
type Config struct {
	cert tls.Certificate
	cas  *x509.CertPool
}

// Load reads the files in dir: ca.pem, cert.pem and key.pem.
func Load(dir string) (*Config, error) {
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"))
	if err != nil {
		return nil, fmt.Errorf("mtls: %w", err)
	}
	ca, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil {
		return nil, fmt.Errorf("mtls: %w", err)
	}
	cas := x509.NewCertPool()
	if !cas.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("mtls: no certificate in %s", filepath.Join(dir, "ca.pem"))
	}
	return &Config{cert: cert, cas: cas}, nil
}

// Server is the TLS config of an internal API: every client must show a
// certificate from our CA. TLS 1.3 only: we control both ends.
func (c *Config) Server() *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{c.cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    c.cas,
	}
}

// Client is the TLS config for calling the service called server, wherever it
// runs: its certificate must come from our CA, and have its name in it.
func (c *Config) Client(server string) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{c.cert},
		RootCAs:      c.cas,
		ServerName:   server,
	}
}
