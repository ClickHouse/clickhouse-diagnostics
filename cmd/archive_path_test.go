package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// 归档过去会被写入当前目录，文件名为 <run-dir-name>.tar.gz，忽略
// -output-dir。运行目录按运行启动的那一秒命名，因此同一秒内启动的两次运行——
// 一台节点对四个 replica 连续做的四次采集正是如此——会写入同样的文件名，
// 后一个归档会静默替换前一个。丢失的那个 bundle 恰恰是唯一记录了故障的
// 那个。现在归档放在其运行目录旁边，位于 -output-dir 之内。

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
	stray := filepath.Base(want) // 旧代码写入的位置：当前工作目录
	t.Cleanup(func() { os.Remove(stray) })

	if err := createArchiveWithTimestamp(run); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("归档不在 -output-dir 内：%v", err)
	}
	if _, err := os.Stat(stray); err == nil {
		t.Errorf("归档被写入当前目录（%s），而不是 -output-dir", stray)
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
		t.Fatalf("每次运行都必须保留自己的归档：a=%v b=%v", errA, errB)
	}
	if bytes.Equal(archA, archB) {
		t.Error("两个归档完全相同——一次运行的 bundle 覆盖了另一次运行的")
	}
}
