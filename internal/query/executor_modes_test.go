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
	slow    string // 文本中包含此字符串的查询会返回 TIMEOUT_EXCEEDED
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
		t.Fatalf("顺序 %v，期望 %v", f.order, want)
	}
}

func TestExecutor_EssentialRunSkipsFullAndSaysWhy(t *testing.T) {
	f := newModesFixture(t, modeFiles)
	w := collectmode.NewWatchdog(0)
	w.Trip("内存已达服务器上限的 94%")
	rec := runlog.New()
	f.run(w, rec)
	if strings.Join(f.order, ",") != "m_ess.sql,z_ess.sql" {
		t.Fatalf("essential 运行执行了 %v", f.order)
	}
	skipped := 0
	for _, e := range rec.Entries() {
		if e.Status == "skipped" {
			skipped++
			if !strings.Contains(e.Extra, "essential collection") || !strings.Contains(e.Extra, "内存已达") {
				t.Errorf("跳过原因 %q", e.Extra)
			}
		}
	}
	if skipped != 2 {
		t.Fatalf("跳过了 %d 个，期望 2 个", skipped)
	}
}

// full 运行期间的一次超时会触发 watchdog：essential collector 仍然运行，
// 超时之后的 full collector 被跳过。
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
		t.Fatalf("超时之后运行仍执行了：%s", got)
	}
	if tripped, why := w.Tripped(); !tripped || !strings.Contains(why, "b_full.sql") {
		t.Fatalf("watchdog 未被触发：%q", why)
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
		t.Fatalf("已超出预算，却仍执行了 %v", f.order)
	}
	for _, e := range rec.Entries() {
		if e.Status != "skipped" || !strings.Contains(e.Extra, "时间预算") {
			t.Errorf("记录 %+v", e)
		}
	}
}

// 一旦本次运行变为 essential，仍在运行的 collector 会得到 1 天的窗口，
// 而不是它们默认的 3 天或 7 天。
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
		t.Fatalf("essential 窗口未被设上限：%q", seen)
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
		t.Fatalf("执行了 %v", f.order)
	}
	for _, e := range rec.Entries() {
		if e.Name == "zk.sql" && (e.Status != "skipped" || !strings.Contains(e.Extra, "Keeper 会话")) {
			t.Errorf("zk.sql 记录 %+v", e)
		}
	}
}
