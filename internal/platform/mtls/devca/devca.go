// Package devca is a certificate authority for development and tests. It
// gives each of our services a certificate with the service's name in it.
// Its private key only ever lives in memory: once it's gone, nobody can
// issue another certificate that our services trust.
package devca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// A CA issues certificates that expire when it does.
type CA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

// New returns a CA that's valid from now, for validity.
func New(validity time.Duration) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		Subject:               pkix.Name{CommonName: "payments development CA"},
		NotBefore:             now.Add(-5 * time.Minute), // clocks drift
		NotAfter:              now.Add(validity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true, // it signs services' certificates, never another CA's
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	return &CA{cert: cert, key: key}, err
}

// Write gives the service called name a directory in dir, with the CA's
// certificate, ca.pem, and its own: cert.pem, and key.pem, which only its
// owner can read. Its name is its only name: the one servers prove, and
// clients are known by.
func (ca *CA) Write(dir, name string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		Subject:   pkix.Name{CommonName: name},
		DNSNames:  []string{name},
		NotBefore: ca.cert.NotBefore,
		NotAfter:  ca.cert.NotAfter,
		KeyUsage:  x509.KeyUsageDigitalSignature,
		// Every service is a server to some, and a client of others.
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, key.Public(), ca.key)
	if err != nil {
		return err
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	dir = filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return errors.Join(
		write(filepath.Join(dir, "ca.pem"), "CERTIFICATE", ca.cert.Raw, 0o644),
		write(filepath.Join(dir, "cert.pem"), "CERTIFICATE", der, 0o644),
		write(filepath.Join(dir, "key.pem"), "PRIVATE KEY", pkcs8, 0o600),
	)
}

// write writes der to path, as a PEM block of type typ.
func write(path, typ string, der []byte, perm os.FileMode) error {
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), perm)
}
