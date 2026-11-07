package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pre-pass（skills/clickhouse-diagnostic/scripts/inspect_bundle.py）是
// 分析 skill 的强制第一步，它是纯 Python，且自身没有测试。本冒烟测试会构建
// 一个最小 bundle，覆盖“一个文件的行在另一个文件的行被迭代之后才被读取”的
// 路径，运行脚本，并要求干净退出。
//
// 它源于一次真实崩溃：mutations 代码块循环了
// `for (db, tb), c in per_table...`，Python 会把循环变量泄漏到外围作用域，
// 于是 `tb` 在函数剩余部分一直是 system.tables。一条未完成的 mutation——
// 一种日常状态——因此让之后每次行访问都变成 AttributeError，pre-pass 只打印
// traceback，别的什么都没有。一个同时带有待处理 mutation 和物化视图的 bundle
// 就是能复现该问题的两行 fixture。
func TestInspectBundle_SmokeOnMinimalBundle(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 不可用")
	}
	script := filepath.Join("..", "skills", "clickhouse-diagnostic", "scripts", "inspect_bundle.py")
	if _, err := os.Stat(script); err != nil {
		t.Skipf("pre-pass 脚本不存在：%v", err)
	}

	dir := t.TempDir()
	b := filepath.Join(dir, "clickhouse_backup_20260101_000000")
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, rows []map[string]any) {
		var sb strings.Builder
		for _, r := range rows {
			line, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			sb.Write(line)
			sb.WriteByte('\n')
		}
		if err := os.WriteFile(filepath.Join(b, name+"_20260101_000000.jsonl"), []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("system.version", []map[string]any{{"version": "26.7.1.1"}})
	write("system.settings", []map[string]any{{"name": "max_threads", "value": "8", "changed": 1}})
	// 物化视图：analyse() 在 mutations 代码块之后读取 system.tables 的行，
	// 而那里正是被泄漏的循环变量作祟之处。
	write("system.tables", []map[string]any{
		{"database": "db", "name": "t", "engine": "MergeTree", "total_rows": "10"},
		{"database": "db", "name": "mv", "engine": "MaterializedView",
			"create_table_query": "CREATE MATERIALIZED VIEW db.mv TO db.t AS SELECT * FROM db.src"},
	})
	// 一条未完成的 mutation 就足够：循环会遍历所有待处理的表，
	// 而不只是超过 >100 阈值的表。
	write("system.mutations", []map[string]any{
		{"database": "db", "table": "t", "mutation_id": "0000000000", "parts_to_do": "3", "is_done": "0",
			"create_time": "2026-01-01 00:00:00", "command": "UPDATE x = 1 WHERE y = 2"},
	})
	write("system.parts", []map[string]any{
		{"database": "db", "table": "t", "partition_id": "all", "active": 1, "rows": "10",
			"bytes_on_disk": "1024", "level": 0, "max_block_number": "1", "modification_time": "2026-01-01 00:00:00"},
	})

	out, err := exec.Command(py, script, b).CombinedOutput()
	if err != nil {
		t.Fatalf("pre-pass 在最小 bundle 上失败（退出码 %v）。\n"+
			"此处出现 traceback 意味着某个文件的行在被读取前已被覆盖。\n%s", err, out)
	}
	s := string(out)
	if strings.Contains(s, "Traceback") {
		t.Fatalf("pre-pass 打印了 traceback：\n%s", s)
	}
	for _, want := range []string{"诊断包检查", "26.7.1.1"} {
		if !strings.Contains(s, want) {
			t.Errorf("pre-pass 输出缺少 %q：\n%s", want, s)
		}
	}
}

// bundle 正在从 clickhouse_backup_<ts> 重命名为
// clickhouse_diagnostic_<ts>。pre-pass 必须能读取两者：旧名称的 bundle
// 仍在未结案的工单中。当两者位于同一个结果文件夹时，它必须按时间戳选取
// 最新的那个——若按名称排序，所有 clickhouse_diagnostic_* 都会压过所有
// clickhouse_backup_*，无论日期如何。
func TestInspectBundle_ReadsBothBundleNames(t *testing.T) {
	writeRun := func(dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range minimalBundle {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	collected := func(out string) (path, at string) {
		t.Helper()
		var r struct {
			Path        string `json:"path"`
			CollectedAt string `json:"collected_at"`
		}
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatalf("pre-pass 未输出 JSON：%v\n%s", err, out)
		}
		return r.Path, r.CollectedAt
	}

	// 结果文件夹中各有各的，其中旧名称对应的是更新的那次运行。
	parent := t.TempDir()
	writeRun(filepath.Join(parent, "clickhouse_diagnostic_20260102_000000"))
	writeRun(filepath.Join(parent, "clickhouse_backup_20260105_000000"))
	out, errOut, code := runPrepass(t, "--json", parent)
	if code != 0 {
		t.Fatalf("退出码 %d：%s", code, errOut)
	}
	if path, at := collected(out); filepath.Base(path) != "clickhouse_backup_20260105_000000" || at != "2026-01-05 00:00:00" {
		t.Errorf("选中的是 %s（collected %q）；期望最新的运行 clickhouse_backup_20260105_000000", filepath.Base(path), at)
	}

	// 使用新名称的归档。
	dir := t.TempDir()
	var members [][2]string
	for name, body := range minimalBundle {
		members = append(members, [2]string{"clickhouse_diagnostic_20260103_101500/" + name, body})
	}
	archive := filepath.Join(dir, "clickhouse_diagnostic_20260103_101500.tar.gz")
	writeTarGz(t, archive, members)
	out, errOut, code = runPrepass(t, "--json", "--extract-to", filepath.Join(dir, "x"), archive)
	if code != 0 {
		t.Fatalf("新名称归档：退出码 %d：%s", code, errOut)
	}
	if _, at := collected(out); at != "2026-01-03 10:15:00" {
		t.Errorf("新名称归档：collected_at = %q，期望 2026-01-03 10:15:00", at)
	}
}

// 文件的查找依据是 collector 名加上运行时间戳，绝不能只靠前缀：
// system.replicas_*.jsonl 也会匹配 system.replicas_problem_tables_*.jsonl，
// 因此当 replicas collector 失败（没有 system.replicas 文件）时，pre-pass 会把
// problem-tables 文件当作 system.replicas 读取——这与 system.parts_* 对
// system.parts_max_block_recent_* 的陷阱相同。
func TestInspectBundle_FileLookupIsExact(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 不可用")
	}
	script := filepath.Join("..", "skills", "clickhouse-diagnostic", "scripts", "inspect_bundle.py")
	b := filepath.Join(t.TempDir(), "clickhouse_diagnostic_20261029_120000")
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	put := func(name, body string) {
		if err := os.WriteFile(filepath.Join(b, name+"_20261029_120000.jsonl"), []byte(body+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("system.version", `{"version":"26.7.1.1"}`)
	// 没有 system.replicas 文件（collector 失败）；只有 problem-tables 文件，
	// 其中有一条只读行，绝不能从 "system.replicas" 被报告为 HC-1.4 / HC-3.x。
	put("system.replicas_problem_tables", `{"database":"db","table":"t","is_readonly":1,"is_session_expired":1,"queue_size":"0","absolute_delay":"9999","log_max_index":"1","log_pointer":"1","total_replicas":3,"active_replicas":1,"zookeeper_exception":""}`)
	out, err := exec.Command(py, "-I", "-B", script, b).CombinedOutput()
	if err != nil {
		t.Fatalf("pre-pass 失败：%v\n%s", err, out)
	}
	if strings.Contains(string(out), "9999") {
		t.Fatalf("system.replicas 的检查读取了 system.replicas_problem_tables：\n%s", out)
	}
}

// 下线的 replica 会从每个按 replica 的文件中缺席（请求以
// skip_unavailable_shards = 1 运行）；pre-pass 必须说明是哪一个。
func TestInspectBundle_ReplicasThatDidNotAnswer(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 不可用")
	}
	script := filepath.Join("..", "skills", "clickhouse-diagnostic", "scripts", "inspect_bundle.py")
	b := filepath.Join(t.TempDir(), "clickhouse_diagnostic_20261029_120000")
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	put := func(name, body string) {
		if err := os.WriteFile(filepath.Join(b, name+"_20261029_120000.jsonl"), []byte(body+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("system.version", `{"version":"26.7.1.1"}`)
	run := func(rows ...string) string {
		put("system.replicas_answered", strings.Join(rows, "\n"))
		out, err := exec.Command(py, "-I", "-B", script, b).CombinedOutput()
		if err != nil {
			t.Fatalf("pre-pass 失败：%v\n%s", err, out)
		}
		return string(out)
	}
	// 两台主机共享短名，其中一台下线，另有一个健康的 IP 别名：
	// 按集群位置匹配，因此恰好是停掉的那台被点名。
	out := run(`{"host_name":"ch-01.site-b","host_address":"10.0.0.2","pos":"2","answered":0}`,
		`{"host_name":"ch-01.site-a","host_address":"10.0.0.1","pos":"1","answered":1}`,
		`{"host_name":"10.0.0.3","host_address":"10.0.0.3","pos":"3","answered":1}`)
	if !strings.Contains(out, "3 个 replica 中有 1 个没有响应本次采集：ch-01.site-b") || !strings.Contains(out, "HC-0") {
		t.Errorf("停止的 replica 未被点名：\n%s", out)
	}
	if strings.Contains(out, "ch-01.site-a —") || strings.Contains(out, "10.0.0.3 —") || strings.Contains(out, ", 10.0.0.3") {
		t.Errorf("应答了的 replica 被点名：\n%s", out)
	}
	// 每个 replica 都应答了：无话可说。
	if out := run(`{"host_name":"a","pos":"1","answered":1}`, `{"host_name":"b","pos":"2","answered":1}`); strings.Contains(out, "没有响应本次采集") {
		t.Errorf("全部应答了，却仍告警：\n%s", out)
	}
}

// essential bundle 会首先说明这一点：其缺失的按表文件和 1 天窗口
// 是设计使然，而它为何进入 essential 本身就是一条 finding。
func TestInspectBundle_EssentialCollectionIsReported(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 不可用")
	}
	script := filepath.Join("..", "skills", "clickhouse-diagnostic", "scripts", "inspect_bundle.py")
	b := filepath.Join(t.TempDir(), "clickhouse_diagnostic_20261029_120000")
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "system.version_20261029_120000.jsonl"), []byte(`{"version":"26.7.1.1"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	xl := "clickhouse-diagnostic execution log\n====\nstarted:  2026-10-29 12:00:00 +0000\nmode:     onprem\n" +
		"collect:  essential collection, chosen automatically: memory at 94 % of the server limit — query timeout 30s\n\nSummary\n-------\n"
	if err := os.WriteFile(filepath.Join(b, "execution_log.txt"), []byte(xl), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(py, "-I", "-B", script, b).CombinedOutput()
	if err != nil {
		t.Fatalf("pre-pass 失败：%v\n%s", err, out)
	}
	for _, want := range []string{"精简采集", "memory at 94 %", "-collect full"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("输出缺少 %q\n%s", want, out)
		}
	}
}
