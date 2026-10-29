package pkg

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Extra settings ride on every request next to readonly=1, escaped, and the
// pinned ones are still there — a setting added for one purpose (here
// skip_unavailable_shards, so one replica that is down no longer fails every
// clusterAllReplicas collector) must not drop the others.
func TestSetSetting_SentOnEveryRequest(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		_, _ = w.Write([]byte("1\n"))
	}))
	defer srv.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	c := NewClickHouseClient("http", host, port, "", "")
	c.SetSetting("skip_unavailable_shards", "1")
	c.SetSetting("log_comment", "a b&c")
	c.SetSetting("skip_unavailable_shards", "1") // idempotent: set, not appended twice
	for i := 0; i < 2; i++ {
		if _, err := c.ExecuteQuery("SELECT 1"); err != nil {
			t.Fatal(err)
		}
	}
	if len(queries) != 2 {
		t.Fatalf("requests = %d, want 2", len(queries))
	}
	for _, q := range queries {
		for _, want := range []string{"readonly=1", "max_execution_time=", "skip_unavailable_shards=1", "log_comment=a+b%26c"} {
			if !strings.Contains(q, want) {
				t.Errorf("request %q lacks %q", q, want)
			}
		}
		if strings.Count(q, "skip_unavailable_shards=") != 1 {
			t.Errorf("setting repeated in %q", q)
		}
	}
}
