package pkg

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
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
	if tr, ok := freshConnectionClient(0).Transport.(*http.Transport); !ok || !tr.DisableKeepAlives {
		t.Error("freshConnectionClient must disable keep-alives")
	}
}
