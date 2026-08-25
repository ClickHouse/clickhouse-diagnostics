package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTree lays out a fake ClickHouse config directory.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCollectPreservesTree pins that files keep their position below
// configDir. Flattening to the base name silently dropped one of any two
// same-named files in different directories — config.d/storage.xml and
// users.d/storage.xml are both ordinary names — and lost the directory
// that determines ClickHouse's config merge order.
func TestCollectPreservesTree(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "configuration")
	writeTree(t, src, map[string]string{
		"config.d/storage.xml": "<clickhouse><s3>DISK-SETTINGS</s3></clickhouse>",
		"users.d/storage.xml":  "<clickhouse><profiles>USER-PROFILES</profiles></clickhouse>",
	})

	if err := NewCollector().Collect(src, dst, false); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	for rel, want := range map[string]string{
		"config.d/storage.xml": "DISK-SETTINGS",
		"users.d/storage.xml":  "USER-PROFILES",
	} {
		got, err := os.ReadFile(filepath.Join(dst, rel))
		if err != nil {
			t.Errorf("%s: not collected: %v", rel, err)
			continue
		}
		if !strings.Contains(string(got), want) {
			t.Errorf("%s: want content containing %q, got %q", rel, want, got)
		}
	}
}

// TestCollectIsRunScoped pins that the destination is the caller's, so
// two runs against different hosts cannot bleed into each other. The
// destination used to be a fixed "./configuration" that nothing emptied,
// so a second run archived whatever the first left behind.
func TestCollectIsRunScoped(t *testing.T) {
	hostA, hostB := t.TempDir(), t.TempDir()
	writeTree(t, hostA, map[string]string{
		"config.d/macros.xml": "<clickhouse><macros>HOST-A-SHARD-1</macros></clickhouse>",
		"config.d/logger.xml": "<clickhouse><logger>HOST-A</logger></clickhouse>",
	})
	writeTree(t, hostB, map[string]string{
		"config.d/logger.xml": "<clickhouse><logger>HOST-B</logger></clickhouse>",
	})

	runA := filepath.Join(t.TempDir(), "configuration")
	runB := filepath.Join(t.TempDir(), "configuration")
	c := NewCollector()
	if err := c.Collect(hostA, runA, false); err != nil {
		t.Fatalf("Collect(hostA): %v", err)
	}
	if err := c.Collect(hostB, runB, false); err != nil {
		t.Fatalf("Collect(hostB): %v", err)
	}

	// hostB never had macros.xml; it must not appear in hostB's output.
	if _, err := os.Stat(filepath.Join(runB, "config.d/macros.xml")); !os.IsNotExist(err) {
		t.Error("host A's macros.xml leaked into host B's collection")
	}
	got, err := os.ReadFile(filepath.Join(runB, "config.d/logger.xml"))
	if err != nil {
		t.Fatalf("hostB logger.xml: %v", err)
	}
	if !strings.Contains(string(got), "HOST-B") {
		t.Errorf("hostB logger.xml has wrong content: %q", got)
	}
}

// TestCollectReportsWalkFailure pins that an unreadable config directory
// surfaces as an error rather than a silent no-op. Reading
// /etc/clickhouse-server/config.d/ normally requires root or the
// clickhouse group, so this is the common failure for an unprivileged
// run — and the caller must be able to warn about it.
func TestCollectReportsWalkFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not apply")
	}
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "configuration")
	writeTree(t, src, map[string]string{
		"config.d/storage.xml": "<clickhouse><s3>x</s3></clickhouse>",
	})
	locked := filepath.Join(src, "config.d")
	if err := os.Chmod(locked, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0750) })

	if err := NewCollector().Collect(src, dst, false); err == nil {
		t.Error("want an error for an unreadable config directory, got nil")
	}
}
