package pkg

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// selfSigned starts an https server with a certificate no system trusts
// (httptest's own, issued for 127.0.0.1) and returns a client for it and
// the certificate as a PEM file.
func selfSigned(t *testing.T) (*ClickHouseClient, string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("1\n"))
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	host, port, _ := net.SplitHostPort(u.Host)
	pemFile := filepath.Join(t.TempDir(), "ca.pem")
	block := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(pemFile, block, 0o600); err != nil {
		t.Fatal(err)
	}
	return NewClickHouseClient("https", host, port, "u", "p"), pemFile
}

func TestTLS_SelfSignedIsRefusedByDefault(t *testing.T) {
	c, _ := selfSigned(t)
	if _, err := c.ExecuteQueryReal("SELECT 1"); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("a self-signed certificate must be refused without -ca-cert / -insecure, got %v", err)
	}
}

func TestTLS_CACertIsTrusted(t *testing.T) {
	c, pemFile := selfSigned(t)
	if err := c.SetTLS(TLSOptions{CAFile: pemFile}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExecuteQueryReal("SELECT 1"); err != nil {
		t.Fatalf("with -ca-cert: %v", err)
	}
	// The one-connection-per-request probe client must honour it too.
	if _, err := c.ExecuteQueryFreshConnection("SELECT 1"); err != nil {
		t.Fatalf("fresh-connection probe with -ca-cert: %v", err)
	}
}

func TestTLS_InsecureSkipsVerification(t *testing.T) {
	c, _ := selfSigned(t)
	if err := c.SetTLS(TLSOptions{Insecure: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExecuteQueryReal("SELECT 1"); err != nil {
		t.Fatalf("with -insecure: %v", err)
	}
	if _, err := c.ExecuteQueryFreshConnection("SELECT 1"); err != nil {
		t.Fatalf("fresh-connection probe with -insecure: %v", err)
	}
}

// A CA file for a different certificate does not open the door.
func TestTLS_WrongCAIsRefused(t *testing.T) {
	c, _ := selfSigned(t)
	// Every httptest server shares one certificate: generate a different one.
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "other-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other.pem")
	_ = os.WriteFile(other, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	if err := c.SetTLS(TLSOptions{CAFile: other}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExecuteQueryReal("SELECT 1"); err == nil {
		t.Fatal("a CA that did not sign the server's certificate must not be trusted")
	}
}

func TestTLS_BadCAFile(t *testing.T) {
	c, _ := selfSigned(t)
	if err := c.SetTLS(TLSOptions{CAFile: filepath.Join(t.TempDir(), "missing.pem")}); err == nil {
		t.Error("a missing CA file must be an error")
	}
	notPEM := filepath.Join(t.TempDir(), "not.pem")
	_ = os.WriteFile(notPEM, []byte("hello"), 0o600)
	if err := c.SetTLS(TLSOptions{CAFile: notPEM}); err == nil || !strings.Contains(err.Error(), "no PEM certificate") {
		t.Errorf("a file without certificates must be an error, got %v", err)
	}
}

// Without options nothing changes: no custom transport is installed.
func TestTLS_NoOptionsKeepsDefaults(t *testing.T) {
	c, _ := selfSigned(t)
	if err := c.SetTLS(TLSOptions{}); err != nil {
		t.Fatal(err)
	}
	if c.httpClient.Transport != nil || c.tlsConfig != nil {
		t.Error("empty TLS options must leave the default transport")
	}
}
