package query

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"clickhouse-diagnostic/internal"
)

// writeFiles 在 dir 下按给定的“相对路径 → 内容”创建文件。
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func v(major, minor, patch, build int) internal.Version {
	return internal.Version{Major: major, Minor: minor, Patch: patch, Build: build}
}

// asMap 以文件名作为结果的键，便于断言。
func asMap(files []internal.QueryFile) map[string]internal.QueryFile {
	m := make(map[string]internal.QueryFile, len(files))
	for _, f := range files {
		m[f.Name] = f
	}
	return m
}

func TestFindVersionedFiles_RootOnly(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"a.sql": "SELECT 1",
		"b.sql": "SELECT 2",
	})

	files, err := FindVersionedFiles(dir, v(25, 4, 1, 0), ".sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("期望 2 个文件，实际为 %d 个", len(files))
	}
	// 按名称排序，以保证执行顺序确定。
	if files[0].Name != "a.sql" || files[1].Name != "b.sql" {
		t.Errorf("期望排序后的 [a.sql b.sql]，实际为 [%s %s]", files[0].Name, files[1].Name)
	}
}

func TestFindVersionedFiles_OverrideWhenServerIsNewEnough(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"a.sql":          "SELECT 'root'",
		"24.1.1.0/a.sql": "SELECT 'v24'",
	})

	files, err := FindVersionedFiles(dir, v(25, 4, 1, 0), ".sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("覆盖后期望 1 个文件，实际为 %d 个", len(files))
	}
	if files[0].DirName != "24.1.1.0" {
		t.Errorf("期望来自 24.1.1.0 的覆盖，实际 DirName=%q", files[0].DirName)
	}
}

func TestFindVersionedFiles_IncompatibleVersionDirSkipped(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"a.sql":          "SELECT 'root'",
		"26.1.1.0/a.sql": "SELECT 'future'",
	})

	files, err := FindVersionedFiles(dir, v(25, 4, 1, 0), ".sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("期望 1 个文件，实际为 %d 个", len(files))
	}
	if files[0].DirName != "" {
		t.Errorf("期望根文件（服务器版本太旧，不适用 26.1.1.0），实际 DirName=%q", files[0].DirName)
	}
}

func TestFindVersionedFiles_HighestCompatibleVersionWins(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"a.sql":             "SELECT 'root'",
		"23.8.1.0/a.sql":    "SELECT 'v23'",
		"25.4.1.0/a.sql":    "SELECT 'v25'",
		"26.1.1.0/a.sql":    "SELECT 'future'",
		"23.8.1.0/b.sql":    "SELECT 'b-v23'",
		"invalid-dir/c.sql": "SELECT 'not-a-version'",
		"c.sql":             "SELECT 'c-root'",
	})

	files, err := FindVersionedFiles(dir, v(25, 4, 1, 0), ".sql")
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(files)
	if len(m) != 3 {
		t.Fatalf("期望 3 个唯一文件，实际为 %d 个", len(m))
	}
	if m["a.sql"].DirName != "25.4.1.0" {
		t.Errorf("a.sql：期望 25.4.1.0（最高兼容版本），实际为 %q", m["a.sql"].DirName)
	}
	if m["b.sql"].DirName != "23.8.1.0" {
		t.Errorf("b.sql：期望 23.8.1.0（仅存在于版本目录），实际为 %q", m["b.sql"].DirName)
	}
	// invalid-dir 不是版本目录 → 其中的文件必须被忽略，
	// 只留下根目录的 c.sql。
	if m["c.sql"].DirName != "" {
		t.Errorf("c.sql：期望根目录（无效版本目录被忽略），实际为 %q", m["c.sql"].DirName)
	}
}

// TestFindVersionedFiles_TieredLadder 复现真实的
// queries.query_analysis/query_details.sql 版本阶梯（root → 23.8.1.0 →
// 23.9.1.0 → 23.11.1.0），再加上 asynchronous_insert_log 模式：
// 一个仅存在于版本目录的文件，在较旧的服务器上必须消失。
func TestFindVersionedFiles_TieredLadder(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"query_details.sql":           "SELECT 'baseline'",
		"23.8.1.0/query_details.sql":  "SELECT 'plus query_cache_usage'",
		"23.9.1.0/query_details.sql":  "SELECT 'plus peak_threads_usage'",
		"23.11.1.0/query_details.sql": "SELECT 'plus hostname column'",
		// 没有根目录对应文件 —— 就像 system.asynchronous_insert_log
		//（该表本身从 22.10 起才存在）。
		"22.10.1.0/async_log.sql": "SELECT 'table exists from 22.10'",
	})

	cases := []struct {
		server        internal.Version
		wantDetails   string // query_details.sql 期望的 DirName
		wantAsyncSeen bool
	}{
		{v(22, 8, 1, 0), "", false},
		{v(22, 10, 1, 0), "", true},
		{v(23, 8, 5, 0), "23.8.1.0", true},
		{v(23, 10, 1, 0), "23.9.1.0", true},
		{v(25, 4, 1, 0), "23.11.1.0", true},
	}
	for _, tc := range cases {
		files, err := FindVersionedFiles(dir, tc.server, ".sql")
		if err != nil {
			t.Fatal(err)
		}
		m := asMap(files)
		if got := m["query_details.sql"].DirName; got != tc.wantDetails {
			t.Errorf("服务器 %d.%d：query_details 来自 %q，期望 %q",
				tc.server.Major, tc.server.Minor, got, tc.wantDetails)
		}
		if _, seen := m["async_log.sql"]; seen != tc.wantAsyncSeen {
			t.Errorf("服务器 %d.%d：async_log seen=%v，期望 %v",
				tc.server.Major, tc.server.Minor, seen, tc.wantAsyncSeen)
		}
	}
}

// TestFindVersionedFiles_RealRepoDirs 守护的是随包发布的目录树，而不是
// 合成的目录树：每个查询/告警目录都必须干净地解析，且每个版本目录覆盖
// 都必须有对应的根文件 —— 除了有文档记录的仅版本文件（在各自版本门控
// 以下不存在的表）。这是 CI 检查，确保在某个模式下新增却被重命名的版本层级，
// 或拼写错误的版本目录，都无法悄悄进入代码库。
func TestFindVersionedFiles_RealRepoDirs(t *testing.T) {
	// 有意只存在于版本目录中的文件，以 "<dir>/<file>" 为键，
	// 使豁免范围与其理由一样窄：asynchronous_insert_log 表在 22.10 中加入，
	// 因此 onprem/gov 没有根文件 —— 但 cloud 有（它从不运行那么旧的版本），
	// 在那里豁免不应适用。
	versionOnly := map[string]bool{
		"queries.onprem/system.asynchronous_insert_log_7_days.sql": true,
		"queries.gov/system.asynchronous_insert_log_7_days.sql":    true,
		// system.server_settings 在 23.3 中加入，因此 onprem/gov（下限
		// 22.8）没有根变体。Cloud 之所以排除，理由与上面完全相同：
		// 它的下限是 23.5，因此它带有根文件，如果该文件将来丢失，
		// 这项检查必须继续失败。
		"queries.onprem/system.server_settings.sql": true,
		"queries.gov/system.server_settings.sql":    true,
		// system.zookeeper_connection 在 23.8 中加入，system.blob_storage_log
		// 在 23.11，system.error_log 在 24.8 —— 全都高于每个模式的下限
		//（onprem/gov 为 22.8，cloud 为 23.5），因此没有模式拥有根变体：
		// 根文件会在没有该表的服务器上被选中，并以 UNKNOWN_TABLE 失败，
		// 而不是被跳过。
		"queries.onprem/system.zookeeper_connection.sql":    true,
		"queries.gov/system.zookeeper_connection.sql":       true,
		"queries.cloud/system.zookeeper_connection.sql":     true,
		"queries.onprem/system.blob_storage_log_7_days.sql": true,
		"queries.gov/system.blob_storage_log_7_days.sql":    true,
		"queries.cloud/system.blob_storage_log_7_days.sql":  true,
		"queries.onprem/system.error_log_7_days.sql":        true,
		"queries.gov/system.error_log_7_days.sql":           true,
		"queries.cloud/system.error_log_7_days.sql":         true,
		// system.view_refreshes 随可刷新 materialized view（物化视图）在 23.12
		// 引入 —— 高于每个下限，因此没有模式拥有根变体。
		"queries.onprem/system.view_refreshes.sql": true,
		"queries.gov/system.view_refreshes.sql":    true,
		"queries.cloud/system.view_refreshes.sql":  true,
		// system.database_replicas 在 25.9 引入；它取代了
		// system.clusters.is_active，而后者会让服务器向每个
		// Replicated 数据库副本询问其状态。
		"queries.onprem/system.database_replicas.sql": true,
		"queries.gov/system.database_replicas.sql":    true,
		"queries.cloud/system.database_replicas.sql":  true,
		// system.aggregated_zookeeper_log 在 25.9 引入，`component` 在 26.2，
		// `is_subrequest` 在 26.3。26.3 之前，子请求与独立请求共享分桶，
		// 并携带整个 Multi 的延迟，因此这三个 collector 仅从 26.3 起存在，
		// 较旧的服务器会跳过它们。
		"queries.onprem/system.aggregated_zookeeper_log_by_hour_2_days.sql":  true,
		"queries.gov/system.aggregated_zookeeper_log_by_hour_2_days.sql":     true,
		"queries.cloud/system.aggregated_zookeeper_log_by_hour_2_days.sql":   true,
		"queries.onprem/system.aggregated_zookeeper_log_by_minute_1_day.sql": true,
		"queries.gov/system.aggregated_zookeeper_log_by_minute_1_day.sql":    true,
		"queries.cloud/system.aggregated_zookeeper_log_by_minute_1_day.sql":  true,
		"queries.onprem/system.aggregated_zookeeper_log_paths_1_day.sql":     true,
		"queries.gov/system.aggregated_zookeeper_log_paths_1_day.sql":        true,
		"queries.cloud/system.aggregated_zookeeper_log_paths_1_day.sql":      true,
		// system.zookeeper_connection.xid 在 24.3 加入；该规则会读取它，
		// 并且刻意没有根对应文件 —— 在较旧的服务器上该检查是不适用，
		// 而不是失败。
		"alerts/keeper_xid_renewal_due.yaml": true,
	}
	dirs := map[string]string{
		"../../queries.onprem":         ".sql",
		"../../queries.gov":            ".sql",
		"../../queries.cloud":          ".sql",
		"../../queries.query_analysis": ".sql",
		"../../alerts":                 ".yaml",
	}
	high := internal.Version{Major: 99, Minor: 1, Patch: 1, Build: 1}
	for dir, ext := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				t.Skipf("%s 不存在", dir)
			}
			files, err := FindVersionedFiles(dir, high, ext)
			if err != nil {
				t.Fatalf("解析失败：%v", err)
			}
			if len(files) == 0 {
				t.Fatal("解析出零个文件")
			}
			// 每个覆盖都必须遮蔽一个同名根文件，
			// 除非它是有文档记录的仅版本文件。
			for _, f := range files {
				if f.DirName == "" || versionOnly[filepath.Base(dir)+"/"+f.Name] {
					continue
				}
				if _, err := os.Stat(filepath.Join(dir, f.Name)); err != nil {
					t.Errorf("覆盖 %s/%s 没有对应的根文件 —— 被重命名的覆盖会变成一个独立文件", f.DirName, f.Name)
				}
			}
		})
	}
}

// TestRealRepoLadders_Monotonic 把 SQL 评审每轮手工进行的检查自动化：
// 随着服务器版本升高，文件所选中的列集合绝不能收缩。一个忘记了低版本层级
// 曾有列的层级，会在较新的服务器上悄悄丢数据 —— 而且只在较新的服务器上，
// 这恰恰是没人会在本地跑的场景。
//
// 列集合用每个文件投影的别名/裸列来近似；这比较粗略，但足以捕获丢列，
// 而丢列正是要防的失败模式。
func TestRealRepoLadders_Monotonic(t *testing.T) {
	ladders := []internal.Version{
		{Major: 22, Minor: 8, Patch: 1}, {Major: 22, Minor: 11, Patch: 1},
		{Major: 23, Minor: 4, Patch: 1}, {Major: 23, Minor: 11, Patch: 1},
		{Major: 24, Minor: 2, Patch: 1}, {Major: 25, Minor: 4, Patch: 1},
	}
	// 没有占位符豁免：用字面量顶替的低层级
	//（'n/a' AS size_uncompressed）仍会把它起成真实输出名的别名，
	// 因此较高层级保持相同名称，集合并不会收缩。正是这个约定让检查保持简单 ——
	// 强制它，而不是开例外。（较早的版本这里有一个永远匹配不上的正则，
	// 因为 projectedColumns 返回的是输出名称，而不是表达式。）
	type seen struct {
		cols map[string]bool
		ver  internal.Version
		dir  string
	}
	for _, dir := range []string{"../../queries.onprem", "../../queries.gov", "../../queries.cloud", "../../queries.query_analysis"} {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				t.Skipf("%s 不存在", dir)
			}
			prev := map[string]seen{} // 文件 → 上一次所选层级投影的内容
			for _, v := range ladders {
				files, err := FindVersionedFiles(dir, v, ".sql")
				if err != nil {
					t.Fatalf("%d.%d: %v", v.Major, v.Minor, err)
				}
				for _, f := range files {
					raw, err := os.ReadFile(f.FullPath)
					if err != nil {
						t.Fatal(err)
					}
					cur := projectedColumns(string(raw))
					p, had := prev[f.Name]
					if had {
						for col := range p.cols {
							if !cur[col] {
								t.Errorf("%s：列 %q 在 %d.%d（%s）选中的层级中被投影，"+
									"但在 %d.%d（%s）选中的层级中缺失 —— 版本阶梯不得收缩",
									f.Name, col,
									p.ver.Major, p.ver.Minor, rungLabel(p.dir),
									v.Major, v.Minor, rungLabel(f.DirName))
							}
						}
					}
					prev[f.Name] = seen{cols: cur, ver: v, dir: f.DirName}
				}
			}
		})
	}
}

// rungLabel 为失败信息命名一个层级（当文件来自目录根而非版本子目录时为
// "root"）。
func rungLabel(dirName string) string {
	if dirName == "" {
		return "root"
	}
	return dirName + "/"
}

// projectedColumns 提取查询投影的输出列名：有别名时取别名，
// 否则取裸列。注释、FROM 子句及其后内容、聚合内部都被忽略。
func projectedColumns(sql string) map[string]bool {
	out := map[string]bool{}
	reAlias := regexp.MustCompile(`(?i)\sAS\s+` + "`?" + `([A-Za-z_][A-Za-z0-9_.]*)` + "`?" + `\s*,?$`)
	reBare := regexp.MustCompile("^`?([A-Za-z_][A-Za-z0-9_.]*)`?,$")
	for _, line := range strings.Split(sql, "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "--") {
			continue
		}
		up := strings.ToUpper(s)
		if strings.HasPrefix(up, "FROM ") || strings.HasPrefix(up, "WHERE ") ||
			strings.HasPrefix(up, "GROUP BY") || strings.HasPrefix(up, "ORDER BY") ||
			strings.HasPrefix(up, "FORMAT ") || strings.HasPrefix(up, "LIMIT ") {
			continue
		}
		if m := reAlias.FindStringSubmatch(s); m != nil {
			out[m[1]] = true
			continue
		}
		if m := reBare.FindStringSubmatch(s); m != nil {
			out[m[1]] = true
		}
	}
	return out
}

func TestFindVersionedFiles_ExtensionFilter(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"rule.yaml":          "name: x",
		"RULE2.YAML":         "name: y",
		"readme.md":          "# docs",
		"query.sql":          "SELECT 1",
		"25.4.1.0/rule.yaml": "name: x-new",
	})

	files, err := FindVersionedFiles(dir, v(25, 4, 1, 0), ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(files)
	if len(m) != 2 {
		t.Fatalf("期望 2 个 yaml 文件，实际为 %d 个：%v", len(m), m)
	}
	if m["rule.yaml"].DirName != "25.4.1.0" {
		t.Errorf("rule.yaml：期望 25.4.1.0 覆盖，实际为 %q", m["rule.yaml"].DirName)
	}
	if _, ok := m["RULE2.YAML"]; !ok {
		t.Error("扩展名匹配应不区分大小写")
	}
}

// 在 macOS 上解压发行包再把文件夹拷贝到服务器，会在每个文件和目录旁留下
// AppleDouble 的 "._name"。下游没有任何地方按扩展名过滤 —— manager.go 会执行
// 遍历返回的一切 —— 因此在遍历跳过点文件之前，"._system.parts.sql" 会被发送到
// 服务器并被拒绝，"._replica_readonly.yaml" 会以
// "yaml: control characters are not allowed" 解析失败。一个真实的 bundle 报告
// 84 个采集器中有 47 个失败，15 条告警规则全部无法解析，原因就在于此，
// 把三个真正的失败淹没了。
func TestFindVersionedFiles_SkipsAppleDoubleAndDotfiles(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"system.parts.sql":            "SELECT 1",
		"._system.parts.sql":          "\x00\x05\x16\x07AppleDouble",
		".DS_Store":                   "\x00\x00\x00\x01",
		"._23.5.1.0":                  "\x00\x05\x16\x07AppleDouble",
		"23.5.1.0/system.parts.sql":   "SELECT 2",
		"23.5.1.0/._system.parts.sql": "\x00\x05\x16\x07AppleDouble",
		"._24.8.1.0/system.parts.sql": "SELECT 3",
	})

	files, err := FindVersionedFiles(dir, v(25, 4, 1, 0), ".sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasPrefix(filepath.Base(f.Name), ".") || strings.Contains(f.Path, string(filepath.Separator)+".") || strings.HasPrefix(f.Path, ".") {
			t.Errorf("点文件泄漏进了结果：name=%q path=%q", f.Name, f.Path)
		}
	}
	// 唯一真实的版本化覆盖胜过唯一真实的根文件。
	if len(files) != 1 {
		t.Fatalf("期望 1 个被选中的文件，实际为 %d 个：%+v", len(files), files)
	}
	if got := files[0].DirName; got != "23.5.1.0" {
		t.Errorf("期望 23.5.1.0 的覆盖，实际 DirName=%q path=%q", got, files[0].Path)
	}
}

// 当调用方传入相对路径或以点开头的目录时，遍历不得跳过 rootDir 本身 ——
// "." 和 "./queries.onprem" 都是合法的。
func TestFindCompatibleQueries_DotRootDirStillWalked(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"a.sql": "SELECT 1"})

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	for _, root := range []string{".", "./"} {
		files, err := NewFinder().FindCompatibleQueries(root, v(25, 4, 1, 0))
		if err != nil {
			t.Fatalf("rootDir %q：%v", root, err)
		}
		if len(files) != 1 {
			t.Errorf("rootDir %q：期望 1 个文件，实际为 %d 个：%+v", root, len(files), files)
		}
	}
}
