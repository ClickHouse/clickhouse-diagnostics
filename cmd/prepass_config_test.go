package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clickhouse-diagnostic/internal/config"
)

// The merge report written by the tool and read by the skill's pre-pass:
// a report produced by config.MergeReport must raise HC-10.11-10.14, so a
// change of wording on one side cannot silently switch the checks off.
func TestInspectBundle_ConfigMergeReport(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	script := filepath.Join("..", "skills", "clickhouse-diagnostic", "scripts", "inspect_bundle.py")
	b := filepath.Join(t.TempDir(), "clickhouse_diagnostic_20261110_090000")
	cfg := filepath.Join(b, "configuration")
	files := map[string]string{
		"config.xml":              "<clickhouse><max_concurrent_queries>100</max_concurrent_queries><max_thread_pool_size>10000</max_thread_pool_size></clickhouse>",
		"config.d/a.xml":          "<clickhouse><max_concurrent_queries>200</max_concurrent_queries><max_thread_pool_size>15000</max_thread_pool_size></clickhouse>",
		"config.d/z_new.xml":      "<clickhouse><max_concurrent_queries>400</max_concurrent_queries></clickhouse>",
		"preprocessed/config.xml": "<clickhouse><max_concurrent_queries>200</max_concurrent_queries><max_thread_pool_size>15000</max_thread_pool_size></clickhouse>",
	}
	for rel, body := range files {
		p := filepath.Join(cfg, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(b, "system.version_20261110_090000.jsonl"), []byte(`{"version":"26.7.1.1"}`+"\n"), 0o644)
	start := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	report := config.MergeReport(cfg,
		[]config.ServerSetting{{Name: "max_thread_pool_size", Value: "15000", ChangeableWithoutRestart: "No"}},
		map[string]time.Time{"config": start.Add(-time.Hour)}, start, // merged config older than the start: HC-10.13
		map[string]time.Time{"config.xml": start.Add(-48 * time.Hour), "config.d/a.xml": start.Add(-2 * time.Hour),
			"config.d/z_new.xml": start.Add(30 * time.Minute)}) // newer than the merged config: HC-10.12
	if err := os.WriteFile(filepath.Join(cfg, "merge_report.txt"), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(py, "-I", "-B", script, b).CombinedOutput()
	if err != nil {
		t.Fatalf("pre-pass failed (exit %v):\n%s", err, out)
	}
	for _, want := range []string{"HC-10.11", "HC-10.12", "HC-10.13", "max_concurrent_queries = 200", "config.d/z_new.xml"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("pre-pass lacks %q:\n%s\n--- report ---\n%s", want, out, report)
		}
	}

	// HC-10.14: a restart-only setting in a file changed after the start
	// and included in the merged config.
	report = config.MergeReport(cfg,
		[]config.ServerSetting{{Name: "max_thread_pool_size", Value: "15000", ChangeableWithoutRestart: "No"}},
		map[string]time.Time{"config": start.Add(2 * time.Hour)}, start,
		map[string]time.Time{"config.xml": start.Add(-48 * time.Hour), "config.d/a.xml": start.Add(time.Hour),
			"config.d/z_new.xml": start.Add(-time.Hour)})
	_ = os.WriteFile(filepath.Join(cfg, "merge_report.txt"), []byte(report), 0o644)
	out, _ = exec.Command(py, "-I", "-B", script, b).CombinedOutput()
	if !strings.Contains(string(out), "HC-10.14") || !strings.Contains(string(out), "max_thread_pool_size = 15000") {
		t.Errorf("pre-pass lacks HC-10.14:\n%s\n--- report ---\n%s", out, report)
	}
}
