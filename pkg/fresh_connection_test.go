package pkg

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// A load balancer pins a kept-alive connection to one backend, so a probe
// that wants to see the balancer must open a new connection every time.
// The server below answers with the client's ephemeral port — the same on
// a reused connection, different on a fresh one — which is exactly the
// difference between the shared client and ExecuteQueryFreshConnection.
func TestExecuteQueryFreshConnection_OpensANewConnectionPerCall(t *testing.T) {
	var mu sync.Mutex
	var remotes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		remotes = append(remotes, r.RemoteAddr)
		mu.Unlock()
		_, _ = w.Write([]byte(r.RemoteAddr + "\n"))
	}))
	defer srv.Close()
	hostPort := strings.TrimPrefix(srv.URL, "http://")
	host, port, _ := strings.Cut(hostPort, ":")
	c := NewClickHouseClient("http", host, port, "", "")

	// Shared client: keep-alive reuses the connection, so the server sees
	// one remote address for three calls — the behaviour that hid the
	// balancer.
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		out, err := c.ExecuteQuery("SELECT hostName()")
		if err != nil {
			t.Fatal(err)
		}
		seen[strings.TrimSpace(out)] = true
	}
	if len(seen) != 1 {
		t.Fatalf("shared client should reuse one connection, saw %d remote addrs: %v", len(seen), seen)
	}

	// Fresh connection per call: three different ephemeral ports.
	seen = map[string]bool{}
	for i := 0; i < 3; i++ {
		out, err := c.ExecuteQueryFreshConnection("SELECT hostName()")
		if err != nil {
			t.Fatal(err)
		}
		seen[strings.TrimSpace(out)] = true
	}
	if len(seen) != 3 {
		t.Fatalf("fresh-connection probes must not share a connection, saw %d remote addrs: %v", len(seen), seen)
	}
}

// The one-shot transport must keep the default transport's connection-level
// bounds (a zero-value Transport has neither a dial nor a TLS-handshake
// timeout), stay on HTTP/1.1 (h2 multiplexes — the reuse the probe avoids)
// and never outlive the probe bound, even when the collectors' query
// timeout is unbounded.
func TestFreshConnectionClient_KeepsTransportBounds(t *testing.T) {
	c := freshConnectionClient(0)
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatal("transport is not *http.Transport")
	}
	if !tr.DisableKeepAlives {
		t.Error("keep-alives must be disabled")
	}
	if tr.DialContext == nil || tr.TLSHandshakeTimeout == 0 {
		t.Error("dial and TLS-handshake bounds must come from the default transport")
	}
	if tr.ForceAttemptHTTP2 || tr.TLSNextProto == nil || len(tr.TLSNextProto) != 0 {
		t.Error("HTTP/2 must be disabled for one-shot probes")
	}
	if c.Timeout != probeTimeout {
		t.Errorf("unbounded client timeout must fall back to probeTimeout, got %v", c.Timeout)
	}
	if got := freshConnectionClient(5 * time.Second).Timeout; got != 5*time.Second {
		t.Errorf("a shorter client timeout must win, got %v", got)
	}
	if got := freshConnectionClient(10 * time.Minute).Timeout; got != probeTimeout {
		t.Errorf("a longer client timeout must be capped at probeTimeout, got %v", got)
	}
}
