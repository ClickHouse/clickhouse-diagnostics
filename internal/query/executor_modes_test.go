package query

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/internal/collectmode"
	"clickhouse-diagnostic/internal/runlog"
	"clickhouse-diagnostic/pkg"
)

type modesFixture struct {
	t       *testing.T
	mu      sync.Mutex
	order   []string
	queries map[string]internal.QueryFile
	slow    string // a query whose text contains this answers TIMEOUT_EXCEEDED
	client  *pkg.ClickHouseClient
}

func newModesFixture(t *testing.T, files map[string]string) *modesFixture {
	f := &modesFixture{t: t, queries: map[string]internal.QueryFile{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		q := string(b)
		f.mu.Lock()
		for name := range files {
			if strings.Contains(q, "marker_"+strings.TrimSuffix(name, ".sql")+" ") {
				f.order = append(f.order, name)
			}
		}
		slow := f.slow != "" && strings.Contains(q, f.slow)
		f.mu.Unlock()
		if slow {
			w.WriteHeader(500)
			_, _ = w.Write([]byte("Code: 159. DB::Exception: Timeout exceeded: elapsed 30.0 seconds. (TIMEOUT_EXCEEDED)"))
			return
		}
		_, _ = w.Write([]byte("{\"x\":1}\n"))
	}))
	t.Cleanup(srv.Close)
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	f.client = pkg.NewClickHouseClient("http", host, port, "", "")
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		f.queries[name] = internal.QueryFile{Name: name, FullPath: p}
	}
	return f
}

func (f *modesFixture) run(w *collectmode.Watchdog, rec *runlog.Recorder) string {
	out, err := NewExecutor(f.client).WithMode("onprem").WithRecorder(rec).WithWatchdog(w).ExecuteQueries(f.queries, f.t.TempDir())
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func q(name, tier string) string {
	h := ""
	if tier != "" {
		h = "-- tier: " + tier + "\n"
	}
	return h + "SELECT 1 AS marker_" + strings.TrimSuffix(name, ".sql") + " FROM system.one"
}

var modeFiles = map[string]string{
	"b_full.sql": q("b_full.sql", ""), "a_full.sql": q("a_full.sql", "full"),
	"z_ess.sql": q("z_ess.sql", "essential"), "m_ess.sql": q("m_ess.sql", "essential"),
}

func TestExecutor_EssentialFirstThenByName(t *testing.T) {
	f := newModesFixture(t, modeFiles)
	f.run(nil, nil)
	want := []string{"m_ess.sql", "z_ess.sql", "a_full.sql", "b_full.sql"}
	if strings.Join(f.order, ",") != strings.Join(want, ",") {
		t.Fatalf("order %v, want %v", f.order, want)
	}
}

func TestExecutor_EssentialRunSkipsFullAndSaysWhy(t *testing.T) {
	f := newModesFixture(t, modeFiles)
	w := collectmode.NewWatchdog(0)
	w.Trip("memory at 94 % of the server limit")
	rec := runlog.New()
	f.run(w, rec)
	if strings.Join(f.order, ",") != "m_ess.sql,z_ess.sql" {
		t.Fatalf("essential run executed %v", f.order)
	}
	skipped := 0
	for _, e := range rec.Entries() {
		if e.Status == "skipped" {
			skipped++
			if !strings.Contains(e.Extra, "essential collection") || !strings.Contains(e.Extra, "memory at 94 %") {
				t.Errorf("skip reason %q", e.Extra)
			}
		}
	}
	if skipped != 2 {
		t.Fatalf("skipped %d, want 2", skipped)
	}
}

// A timeout during a full run trips the watchdog: the essential collectors
// still run, the full ones after the timeout are skipped.
func TestExecutor_TimeoutSwitchesTheRestToEssential(t *testing.T) {
	f := newModesFixture(t, map[string]string{
		"a_ess.sql": q("a_ess.sql", "essential"), "b_full.sql": q("b_full.sql", ""),
		"c_full.sql": q("c_full.sql", ""), "d_full.sql": q("d_full.sql", ""),
	})
	f.slow = "marker_b_full "
	w := collectmode.NewWatchdog(0)
	rec := runlog.New()
	f.run(w, rec)
	if got := strings.Join(f.order, ","); got != "a_ess.sql,b_full.sql" {
		t.Fatalf("after the timeout the run still executed: %s", got)
	}
	if tripped, why := w.Tripped(); !tripped || !strings.Contains(why, "b_full.sql") {
		t.Fatalf("watchdog not tripped: %q", why)
	}
}

func TestExecutor_BudgetSkipsEverythingLeft(t *testing.T) {
	f := newModesFixture(t, modeFiles)
	w := collectmode.NewWatchdog(time.Nanosecond)
	w.Start()
	time.Sleep(time.Millisecond)
	rec := runlog.New()
	f.run(w, rec)
	if len(f.order) != 0 {
		t.Fatalf("over budget, yet executed %v", f.order)
	}
	for _, e := range rec.Entries() {
		if e.Status != "skipped" || !strings.Contains(e.Extra, "time budget") {
			t.Errorf("entry %+v", e)
		}
	}
}

// Once the run is essential, the collectors that still run get 1-day
// windows instead of their 3- or 7-day defaults.
func TestExecutor_EssentialCapsWindows(t *testing.T) {
	var seen []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, string(b))
		mu.Unlock()
		_, _ = w.Write([]byte("{}\n"))
	}))
	defer srv.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	dir := t.TempDir()
	p := filepath.Join(dir, "e.sql")
	if err := os.WriteFile(p, []byte("-- tier: essential\nSELECT 1 FROM system.one WHERE now() > {from:7d}"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := collectmode.NewWatchdog(0)
	w.Trip("chosen")
	ex := NewExecutor(pkg.NewClickHouseClient("http", host, port, "", "")).WithMode("onprem").WithWatchdog(w)
	if _, err := ex.ExecuteQueries(map[string]internal.QueryFile{"e.sql": {Name: "e.sql", FullPath: p}}, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || !strings.Contains(seen[0], "INTERVAL 24 HOUR") || strings.Contains(seen[0], "7 DAY") {
		t.Fatalf("essential window not capped: %q", seen)
	}
}

func TestExecutor_KeeperDownSkipsKeeperCollectors(t *testing.T) {
	f := newModesFixture(t, map[string]string{
		"a_ess.sql": q("a_ess.sql", "essential"),
		"zk.sql":    "-- tier: essential\n-- needs: keeper\nSELECT 1 AS marker_zk FROM system.one",
	})
	rec := runlog.New()
	if _, err := NewExecutor(f.client).WithMode("onprem").WithRecorder(rec).WithKeeperDown("no Keeper session").ExecuteQueries(f.queries, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.order, ",") != "a_ess.sql" {
		t.Fatalf("executed %v", f.order)
	}
	for _, e := range rec.Entries() {
		if e.Name == "zk.sql" && (e.Status != "skipped" || !strings.Contains(e.Extra, "live Keeper session")) {
			t.Errorf("zk.sql entry %+v", e)
		}
	}
}
