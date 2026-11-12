package pkg

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
)

// TLSOptions are for an https server whose certificate the system does not
// trust — a self-signed or internal CA, as on most self-managed clusters.
type TLSOptions struct {
	// CAFile is a PEM file of CA certificates to trust in addition to the
	// system roots (-ca-cert). The server's certificate must still be valid
	// and issued for the host connected to.
	CAFile string
	// Insecure skips certificate verification entirely (-insecure): the
	// connection is encrypted but the server is not authenticated.
	Insecure bool
}

// Config is the tls.Config these options describe; nil when neither is set,
// so the default transport and its system roots stay in use.
func (o TLSOptions) Config() (*tls.Config, error) {
	if o.CAFile == "" && !o.Insecure {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if o.Insecure {
		cfg.InsecureSkipVerify = true //nolint:gosec // asked for with -insecure, warned about at start
		return cfg, nil
	}
	pemBytes, err := os.ReadFile(o.CAFile)
	if err != nil {
		return nil, fmt.Errorf("-ca-cert: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("-ca-cert: no PEM certificate in %s", o.CAFile)
	}
	cfg.RootCAs = pool
	return cfg, nil
}

// SetTLS applies the options to every request the client makes, the
// one-connection probes included. Empty options change nothing.
func (c *ClickHouseClient) SetTLS(o TLSOptions) error {
	cfg, err := o.Config()
	if err != nil || cfg == nil {
		return err
	}
	c.tlsConfig = cfg
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = cfg.Clone()
	c.httpClient.Transport = tr
	return nil
}
