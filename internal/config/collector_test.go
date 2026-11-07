package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTree 布置一个假的 ClickHouse 配置目录。
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

// TestCollectPreservesTree 固定住一点：文件在 configDir 下保持原有位置。
// 压平到基本文件名会静默丢弃不同目录中任意两个同名文件之一——
// config.d/storage.xml 和 users.d/storage.xml 都是常见名称——
// 并丢失决定 ClickHouse 配置合并顺序的目录信息。
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
			t.Errorf("%s：未被采集：%v", rel, err)
			continue
		}
		if !strings.Contains(string(got), want) {
			t.Errorf("%s：期望内容包含 %q，得到 %q", rel, want, got)
		}
	}
}

// TestCollectIsRunScoped 固定住一点：目标目录由调用方决定，因此
// 针对不同主机的两次运行不会互相污染。目标目录过去是固定的
// "./configuration" 且没有任何清空机制，
// 于是第二次运行会归档第一次遗留的内容。
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

	// hostB 从来没有 macros.xml；它绝不能出现在 hostB 的输出中。
	if _, err := os.Stat(filepath.Join(runB, "config.d/macros.xml")); !os.IsNotExist(err) {
		t.Error("host A 的 macros.xml 泄漏到了 host B 的采集中")
	}
	got, err := os.ReadFile(filepath.Join(runB, "config.d/logger.xml"))
	if err != nil {
		t.Fatalf("hostB logger.xml: %v", err)
	}
	if !strings.Contains(string(got), "HOST-B") {
		t.Errorf("hostB logger.xml 内容错误：%q", got)
	}
}

// TestCollectReportsWalkFailure 固定住一点：不可读的配置目录
// 会以错误形式暴露，而不是静默无操作。读取
// /etc/clickhouse-server/config.d/ 通常需要 root 或
// clickhouse 组权限，因此这是非特权运行的常见失败——
// 调用方必须能够对此发出警告。
func TestCollectReportsWalkFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("以 root 运行：权限位不适用")
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
		t.Error("不可读的配置目录应当报错，实际得到 nil")
	}
}
