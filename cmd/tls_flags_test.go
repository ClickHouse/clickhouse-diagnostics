package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clickhouse-diagnostic/pkg"
)

// -ca-cert / -insecure: ignored over http, -insecure wins over -ca-cert and
// is named in the execution log, a bad CA file stops the run.
func TestApplyTLS(t *testing.T) {
	c := pkg.NewClickHouseClient("https", "h", "8443", "u", "p")
	if note, err := applyTLS(c, "https", pkg.TLSOptions{}); note != "" || err != nil {
		t.Errorf("no options: %q %v", note, err)
	}
	if note, err := applyTLS(c, "http", pkg.TLSOptions{Insecure: true}); note != "" || err != nil {
		t.Errorf("http: ignored, got %q %v", note, err)
	}
	if note, err := applyTLS(c, "https", pkg.TLSOptions{Insecure: true, CAFile: "/missing.pem"}); err != nil || !strings.Contains(note, "-insecure") {
		t.Errorf("insecure wins over a CA file it does not read: %q %v", note, err)
	}
	if _, err := applyTLS(c, "https", pkg.TLSOptions{CAFile: filepath.Join(t.TempDir(), "missing.pem")}); err == nil {
		t.Error("a missing CA file must stop the run")
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	// any well-formed certificate will do for the note
	pemCert := "-----BEGIN CERTIFICATE-----\nMIIBhTCCASugAwIBAgIQIRi6zePL6mKjOipn+dNuaTAKBggqhkjOPQQDAjASMRAw\nDgYDVQQKEwdBY21lIENvMB4XDTE3MTAyMDE5NDMwNloXDTE4MTAyMDE5NDMwNlow\nEjEQMA4GA1UEChMHQWNtZSBDbzBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABD0d\n7VNhbWvZLWPuj/RtHFjvtJBEwOkhbN/BnnE8rnZR8+sbwnc/KhCk3FhnpHZnQz7B\n5aETbbIgmuvewdjvSBSjYzBhMA4GA1UdDwEB/wQEAwICpDATBgNVHSUEDDAKBggr\nBgEFBQcDATAPBgNVHRMBAf8EBTADAQH/MCkGA1UdEQQiMCCCDmxvY2FsaG9zdDo1\nNDUzgg4xMjcuMC4wLjE6NTQ1MzAKBggqhkjOPQQDAgNIADBFAiEA2zpJEPQyz6/l\nWf86aX6PepsntZv2GYlA5UpabfT2EZICICpJ5h/iI+i341gBmLiAFQOyTDT+/wQc\n6MF9+Yw1Yy0t\n-----END CERTIFICATE-----\n"
	_ = os.WriteFile(ca, []byte(pemCert), 0o600)
	if note, err := applyTLS(c, "https", pkg.TLSOptions{CAFile: ca}); err != nil || !strings.Contains(note, "-ca-cert") {
		t.Errorf("ca-cert: %q %v", note, err)
	}
}
