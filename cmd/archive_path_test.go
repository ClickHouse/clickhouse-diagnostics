package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The archive used to be written to the current directory as
// <run-dir-name>.tar.gz, ignoring -output-dir. The run directory is named by
// the second the run started, so two runs started in the same second — four
// back-to-back collections from one node against four replicas did exactly
// that — wrote the same file name, and the later archive silently replaced
// the earlier one. The bundle that was lost was the only one carrying the
// fault. The archive now lands next to its run directory, inside -output-dir.

func makeRunDir(t *testing.T, outputDir, content string) string {
	t.Helper()
	run := filepath.Join(outputDir, "clickhouse_backup_20260101_000000")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "system.version_20260101_000000.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestCreateArchiveWithTimestamp_WritesIntoOutputDir(t *testing.T) {
	out := t.TempDir()
	run := makeRunDir(t, out, `{"version":"26.7.1.1"}`+"\n")
	want := filepath.Join(out, "clickhouse_backup_20260101_000000.tar.gz")
	stray := filepath.Base(want) // where the old code put it: the CWD
	t.Cleanup(func() { os.Remove(stray) })

	if err := createArchiveWithTimestamp(run); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("archive not inside -output-dir: %v", err)
	}
	if _, err := os.Stat(stray); err == nil {
		t.Errorf("archive written to the current directory (%s), not -output-dir", stray)
	}
}

func TestCreateArchiveWithTimestamp_SameSecondRunsDoNotOverwrite(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	runA := makeRunDir(t, a, `{"node":"replica-a"}`+"\n")
	runB := makeRunDir(t, b, `{"node":"replica-b"}`+"\n")
	t.Cleanup(func() { os.Remove(filepath.Base(runA) + ".tar.gz") })

	for _, run := range []string{runA, runB} {
		if err := createArchiveWithTimestamp(run); err != nil {
			t.Fatal(err)
		}
	}
	archA, errA := os.ReadFile(runA + ".tar.gz")
	archB, errB := os.ReadFile(runB + ".tar.gz")
	if errA != nil || errB != nil {
		t.Fatalf("each run must keep its own archive: a=%v b=%v", errA, errB)
	}
	if bytes.Equal(archA, archB) {
		t.Error("the two archives are identical — one run's bundle replaced the other's")
	}
}
