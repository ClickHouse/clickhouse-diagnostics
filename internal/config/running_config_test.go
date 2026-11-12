package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func configTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// The server's directory also holds its TLS key and certificate: only
// configuration files, at the top level and in *.d directories, are copied.
func TestCollect_OnlyConfigurationFiles(t *testing.T) {
	src := configTree(t, map[string]string{
		"config.xml":           "<clickhouse><a>1</a></clickhouse>",
		"users.xml":            "<clickhouse><users/></clickhouse>",
		"config.d/memory.xml":  "<clickhouse><b>2</b></clickhouse>",
		"users.d/default.xml":  "<clickhouse><users/></clickhouse>",
		"server.key":           "-----BEGIN PRIVATE KEY-----\nMIIE\n-----END PRIVATE KEY-----\n",
		"server.crt":           "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n",
		"ssl/other.xml":        "<clickhouse/>",
		"config.d/notes.txt":   "todo",
		"config.xml.bak":       "<clickhouse/>",
		"config.d/old/zzz.xml": "<clickhouse/>",
	})
	dst := t.TempDir()
	if err := NewCollector().Collect(src, dst, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"config.xml", "users.xml", "config.d/memory.xml", "users.d/default.xml"} {
		if _, err := os.Stat(filepath.Join(dst, want)); err != nil {
			t.Errorf("%s not collected", want)
		}
	}
	for _, never := range []string{"server.key", "server.crt", "ssl/other.xml", "config.d/notes.txt", "config.xml.bak", "config.d/old/zzz.xml"} {
		if _, err := os.Stat(filepath.Join(dst, never)); err == nil {
			t.Errorf("%s collected: not configuration", never)
		}
	}
}

func TestSourceFiles_MergeOrder(t *testing.T) {
	src := configTree(t, map[string]string{
		"config.xml": "<clickhouse/>", "users.xml": "<clickhouse/>",
		"config.d/b.xml": "<clickhouse/>", "config.d/a.xml": "<clickhouse/>", "users.d/u.xml": "<clickhouse/>",
		"server.key": "x",
	})
	var got []string
	for _, f := range SourceFiles(src) {
		r, _ := filepath.Rel(src, f)
		got = append(got, filepath.ToSlash(r))
	}
	if strings.Join(got, " ") != "config.xml users.xml config.d/a.xml config.d/b.xml users.d/u.xml" {
		t.Errorf("order: %v", got)
	}
	// The old default, config.d/, brings the main files beside it.
	got = nil
	for _, f := range SourceFiles(filepath.Join(src, "config.d")) {
		got = append(got, filepath.Base(f))
	}
	if strings.Join(got, " ") != "config.xml users.xml a.xml b.xml u.xml" {
		t.Errorf("from config.d/: the whole configuration around it, got %v", got)
	}
}

func TestPreprocessedDirAndDiscovery(t *testing.T) {
	src := configTree(t, map[string]string{
		"config.xml":        "<clickhouse><!-- <path>/commented/</path> --><path>/data/ch/</path></clickhouse>",
		"config.d/path.xml": "<clickhouse><path>/data/ch2/</path></clickhouse>",
	})
	if got := PreprocessedDir("/var/lib/x/", SourceFiles(src)); got != "/var/lib/x/preprocessed_configs" {
		t.Errorf("server path wins: %s", got)
	}
	if got := PreprocessedDir("", SourceFiles(src)); got != "/data/ch2/preprocessed_configs" {
		t.Errorf("the last <path> of the sources, comments ignored: %s", got)
	}
	if got := PreprocessedDir("", nil); got != "/var/lib/clickhouse/preprocessed_configs" {
		t.Errorf("default: %s", got)
	}
	pre := configTree(t, map[string]string{"config.xml": "<clickhouse/>"})
	if got := DiscoveryFiles(src, pre); len(got) != 1 || got[0] != filepath.Join(pre, "config.xml") {
		t.Errorf("the merged config.xml is read first: %v", got)
	}
	if got := DiscoveryFiles(src, filepath.Join(pre, "missing")); len(got) != 2 {
		t.Errorf("without it, the sources: %v", got)
	}
}

func mergeFixture(t *testing.T) string {
	return configTree(t, map[string]string{
		"config.xml": `<clickhouse>
  <max_concurrent_queries>100</max_concurrent_queries>
  <max_thread_pool_size>10000</max_thread_pool_size>
  <logger><level>trace</level></logger>
  <zookeeper><node><host>k1</host></node><node><host>k2</host></node></zookeeper>
  <listen_host>::</listen_host>
</clickhouse>`,
		"config.d/a_memory.xml": `<clickhouse><max_concurrent_queries>200</max_concurrent_queries><listen_host>::</listen_host></clickhouse>`,
		"config.d/b_tuning.xml": `<clickhouse>
  <max_concurrent_queries>300</max_concurrent_queries>
  <max_thread_pool_size>20000</max_thread_pool_size>
  <logger><level>debug</level></logger>
  <zookeeper><node><host>k3</host></node></zookeeper>
</clickhouse>`,
		"users.xml":            `<clickhouse><users><default><password>REMOVED</password><profile>default</profile></default></users></clickhouse>`,
		"users.d/override.xml": `<clickhouse><users><default><password>REMOVED</password><profile>readonly</profile></default></users></clickhouse>`,
		"preprocessed/config.xml": `<clickhouse>
  <max_concurrent_queries>300</max_concurrent_queries>
  <max_thread_pool_size>20000</max_thread_pool_size>
  <logger><level>debug</level></logger>
  <zookeeper><node><host>k1</host></node><node><host>k2</host></node><node><host>k3</host></node></zookeeper>
  <listen_host>::</listen_host>
</clickhouse>`,
		"preprocessed/users.xml": `<clickhouse><users><default><password>REMOVED</password><profile>readonly</profile></default></users></clickhouse>`,
	})
}

func TestMergeReport_Conflicts(t *testing.T) {
	dir := mergeFixture(t)
	r := MergeReport(dir, nil, nil, time.Time{}, nil)
	for _, want := range []string{
		"different values: 4",
		"config: max_concurrent_queries",
		"config.d/b_tuning.xml                        = 300",
		"= 300   ← from config.d/b_tuning.xml",
		"config: logger/level",
		"= debug   ← from config.d/b_tuning.xml",
		"users: users/default/profile",
		"= readonly   ← from users.d/override.xml",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("report lacks %q:\n%s", want, r)
		}
	}
	// A list (Keeper nodes) is not a conflict; the same value twice is
	// counted, not listed; the password is the same REMOVED everywhere.
	for _, never := range []string{"zookeeper/node/host", "config: listen_host", "users/default/password"} {
		if strings.Contains(r, never) {
			t.Errorf("report lists %q:\n%s", never, r)
		}
	}
	if !strings.Contains(r, "more element(s) defined in more than one file with the same value") {
		t.Errorf("identical duplicates not counted:\n%s", r)
	}
}

func TestMergeReport_ChangedSinceStart(t *testing.T) {
	dir := mergeFixture(t)
	started := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	settings := []ServerSetting{
		{Name: "max_thread_pool_size", Value: "20000", ChangeableWithoutRestart: "No"},
		{Name: "max_concurrent_queries", Value: "300", ChangeableWithoutRestart: "Yes"},
	}
	times := map[string]time.Time{
		"config.xml": started.Add(-24 * time.Hour), "config.d/a_memory.xml": started.Add(-time.Hour),
		"config.d/b_tuning.xml": started.Add(time.Hour), // edited after the start
	}
	// Reloaded after the edit: in the merged config, but the restart-only
	// setting it carries is not live until the next restart.
	r := MergeReport(dir, settings, map[string]time.Time{"config": started.Add(2 * time.Hour)}, started, times)
	i := strings.Index(r, "## Changed since the server started")
	sec := r[i:]
	for _, want := range []string{"config.d/b_tuning.xml", "— in the merged config.xml", "max_thread_pool_size", "= 20000 — running value from a file changed since the start"} {
		if !strings.Contains(sec, want) {
			t.Errorf("lacks %q:\n%s", want, sec)
		}
	}
	if strings.Contains(sec, "max_concurrent_queries") || strings.Contains(sec, "a_memory.xml") {
		t.Errorf("a reloadable setting or an unchanged file is listed:\n%s", sec)
	}
	// The merged config is older than the edit: the reload failed.
	r = MergeReport(dir, settings, map[string]time.Time{"config": started.Add(30 * time.Minute)}, started, times)
	if !strings.Contains(r, "NOT in the merged config.xml") || !strings.Contains(r, "did not pick it up") {
		t.Errorf("a failed reload is not reported:\n%s", r)
	}
	if strings.Contains(r, "takes effect only after a restart") {
		t.Errorf("a file that is not running is not 'pending a restart':\n%s", r)
	}
	// Merged config older than the start: not rewritten at startup.
	if r = MergeReport(dir, settings, map[string]time.Time{"config": started.Add(-48 * time.Hour)}, started, times); !strings.Contains(r, "did not rewrite it when it started") {
		t.Errorf("a merged config older than the start is not called out:\n%s", r)
	}
	// Nothing changed since the start.
	times["config.d/b_tuning.xml"] = started.Add(-time.Minute)
	if r = MergeReport(dir, settings, map[string]time.Time{"config": started}, started, times); !strings.Contains(r, "no configuration file changed since the server started") {
		t.Errorf("unchanged:\n%s", r)
	}
}

// The report is built from the collected, sanitised copies: a password set
// differently in two files appears only as REMOVED.
func TestMergeReport_NeverShowsSecrets(t *testing.T) {
	src := configTree(t, map[string]string{
		"users.xml":         `<clickhouse><users><default><password>first-Secret-1</password></default></users></clickhouse>`,
		"users.d/other.xml": `<clickhouse><users><default><password>second-Secret-2</password></default></users></clickhouse>`,
	})
	dst := t.TempDir()
	if err := NewCollector().Collect(src, dst, false); err != nil {
		t.Fatal(err)
	}
	r := MergeReport(dst, nil, nil, time.Time{}, nil)
	if strings.Contains(r, "Secret") {
		t.Fatalf("a password reached the report:\n%s", r)
	}
}

// The running value is credited to the last file in merge order that sets
// it — the one whose override took effect.
func TestMergeReport_SetByIsTheLastFile(t *testing.T) {
	dir := configTree(t, map[string]string{
		"config.xml":              "<clickhouse><max_concurrent_queries>300</max_concurrent_queries></clickhouse>",
		"config.d/a.xml":          "<clickhouse><max_concurrent_queries>200</max_concurrent_queries></clickhouse>",
		"config.d/b.xml":          "<clickhouse><max_concurrent_queries>300</max_concurrent_queries></clickhouse>",
		"preprocessed/config.xml": "<clickhouse><max_concurrent_queries>300</max_concurrent_queries></clickhouse>",
	})
	if r := MergeReport(dir, nil, nil, time.Time{}, nil); !strings.Contains(r, "= 300   ← from config.d/b.xml") {
		t.Errorf("set by:\n%s", r)
	}
}

// Review round: one regression per finding.
func TestMergeReport_ReviewFixes(t *testing.T) {
	started := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

	// A list whose entries come one per file is not a conflict.
	dir := configTree(t, map[string]string{
		"config.xml":              "<clickhouse><zookeeper><node><host>k1</host></node></zookeeper></clickhouse>",
		"config.d/k.xml":          "<clickhouse><zookeeper><node><host>k2</host></node></zookeeper></clickhouse>",
		"preprocessed/config.xml": "<clickhouse><zookeeper><node><host>k1</host></node><node><host>k2</host></node></zookeeper></clickhouse>",
	})
	if r := MergeReport(dir, nil, nil, time.Time{}, nil); !strings.Contains(r, "different values: 0") {
		t.Errorf("a list built across files is reported as a conflict:\n%s", r)
	}

	// After a failed reload a newer file can match the old running value:
	// it is not credited, since the merged config predates it.
	dir = configTree(t, map[string]string{
		"config.xml":              "<clickhouse><max_concurrent_queries>100</max_concurrent_queries></clickhouse>",
		"config.d/a.xml":          "<clickhouse><max_concurrent_queries>200</max_concurrent_queries></clickhouse>",
		"config.d/z_new.xml":      "<clickhouse><max_concurrent_queries>100</max_concurrent_queries></clickhouse>",
		"preprocessed/config.xml": "<clickhouse><max_concurrent_queries>100</max_concurrent_queries></clickhouse>",
	})
	times := map[string]time.Time{"config.xml": started.Add(-time.Hour), "config.d/a.xml": started.Add(time.Hour),
		"config.d/z_new.xml": started.Add(3 * time.Hour)}
	r := MergeReport(dir, nil, map[string]time.Time{"config": started.Add(30 * time.Minute)}, started, times)
	if strings.Contains(r, "← from config.d/z_new.xml") || !strings.Contains(r, "= 100   ← from config.xml") {
		t.Errorf("a file the merged config predates is credited:\n%s", r)
	}

	// users.xml and config.xml are judged against their own merged file.
	dir = configTree(t, map[string]string{
		"config.xml": "<clickhouse/>", "users.xml": "<clickhouse/>",
		"users.d/u.xml":           "<clickhouse><profiles><default><max_threads>4</max_threads></default></profiles></clickhouse>",
		"preprocessed/config.xml": "<clickhouse/>", "preprocessed/users.xml": "<clickhouse/>",
	})
	times = map[string]time.Time{"config.xml": started.Add(-time.Hour), "users.xml": started.Add(-time.Hour), "users.d/u.xml": started.Add(time.Hour)}
	r = MergeReport(dir, nil, map[string]time.Time{"config": started, "users": started.Add(2 * time.Hour)}, started, times)
	if strings.Contains(r, "NOT in the merged") || !strings.Contains(r, "— in the merged users.xml") {
		t.Errorf("a users.d change loaded into users.xml is judged against config.xml:\n%s", r)
	}

	// A changed file whose restart-only value is overridden later does not
	// claim it; only the running value's file does.
	dir = configTree(t, map[string]string{
		"config.xml":              "<clickhouse><max_thread_pool_size>10000</max_thread_pool_size></clickhouse>",
		"config.d/a.xml":          "<clickhouse><max_thread_pool_size>12000</max_thread_pool_size><logger><level>debug</level></logger></clickhouse>",
		"config.d/b.xml":          "<clickhouse><max_thread_pool_size>15000</max_thread_pool_size></clickhouse>",
		"preprocessed/config.xml": "<clickhouse><max_thread_pool_size>15000</max_thread_pool_size><logger><level>debug</level></logger></clickhouse>",
	})
	times = map[string]time.Time{"config.xml": started.Add(-time.Hour), "config.d/a.xml": started.Add(time.Hour), "config.d/b.xml": started.Add(-time.Hour)}
	settings := []ServerSetting{{Name: "max_thread_pool_size", Value: "15000", ChangeableWithoutRestart: "No"}}
	r = MergeReport(dir, settings, map[string]time.Time{"config": started.Add(2 * time.Hour)}, started, times)
	if strings.Contains(r[strings.Index(r, "## Changed"):], "max_thread_pool_size") {
		t.Errorf("an overridden restart-only value is claimed by the edited file:\n%s", r)
	}
}

// Without system.server_settings, the merged directory comes from the
// root-level <path> of the server config — not a disk's nested <path>.
func TestPreprocessedDir_RootPathOnly(t *testing.T) {
	src := configTree(t, map[string]string{
		"config.xml":           "<clickhouse><path>/data/ch/</path></clickhouse>",
		"config.d/storage.xml": "<clickhouse><storage_configuration><disks><cold><path>/mnt/cold/</path></cold></disks></storage_configuration></clickhouse>",
		"users.xml":            "<clickhouse><path>/not/here/</path></clickhouse>",
	})
	if got := PreprocessedDir("", SourceFiles(src)); got != "/data/ch/preprocessed_configs" {
		t.Errorf("got %s", got)
	}
}

// Second review round.
func TestReviewRound2(t *testing.T) {
	started := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

	// A from_env override is credited with the resolved running value and
	// takes part in the restart check.
	dir := configTree(t, map[string]string{
		"config.xml":              "<clickhouse><max_thread_pool_size>10000</max_thread_pool_size></clickhouse>",
		"config.d/env.xml":        `<clickhouse><max_thread_pool_size from_env="POOL"/></clickhouse>`,
		"preprocessed/config.xml": "<clickhouse><max_thread_pool_size>16000</max_thread_pool_size></clickhouse>",
	})
	times := map[string]time.Time{"config.xml": started.Add(-time.Hour), "config.d/env.xml": started.Add(time.Hour)}
	settings := []ServerSetting{{Name: "max_thread_pool_size", Value: "16000", ChangeableWithoutRestart: "No"}}
	r := MergeReport(dir, settings, map[string]time.Time{"config": started.Add(2 * time.Hour)}, started, times)
	if !strings.Contains(r, "← from config.d/env.xml") || !strings.Contains(r, "max_thread_pool_size                       = 16000 — running value") {
		t.Errorf("a from_env override is not attributed:\n%s", r)
	}

	// An explicit users.d/ still finds config.d/'s data path; an unrelated
	// top-level file does not supply one; a YAML override does.
	src := configTree(t, map[string]string{
		"config.xml":        "<clickhouse><path>/data/a/</path></clickhouse>",
		"z_old.xml":         "<clickhouse><path>/data/old/</path></clickhouse>",
		"config.d/path.xml": "<clickhouse><path>/data/b/</path></clickhouse>",
		"users.d/u.xml":     "<clickhouse/>",
	})
	if got := PreprocessedDir("", SourceFiles(filepath.Join(src, "users.d"))); got != "/data/b/preprocessed_configs" {
		t.Errorf("explicit users.d/: %s", got)
	}
	src = configTree(t, map[string]string{
		"config.xml":         "<clickhouse><path>/data/a/</path></clickhouse>",
		"z_old.xml":          "<clickhouse><path>/data/old/</path></clickhouse>",
		"config.d/path.yaml": "path: /data/yaml/\n",
	})
	if got := PreprocessedDir("", SourceFiles(src)); got != "/data/yaml/preprocessed_configs" {
		t.Errorf("YAML override, unrelated file ignored: %s", got)
	}
}
