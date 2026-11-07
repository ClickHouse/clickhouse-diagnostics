package query

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 裸的 `ARRAY JOIN tables` 会丢弃所有 `tables` 数组为空的 query_log 行 ——
// 而这恰恰是归档要覆盖的那类失败：一个在解析出表之前就抛错的查询
// （UNKNOWN_TABLE 60、解析错误 6/27）没有 tables，于是凭空消失。曾对着一个真实
// bundle 观察到：归档的 JSONL 只包含异常码 395 和 241，而实时 dashboard 查询
// （没有 array join）报告了 395、241、6 和 60。
func TestQueryLogDetails_UsesLeftArrayJoin(t *testing.T) {
	plainArrayJoin := regexp.MustCompile(`(?i)(^|[^T]\s)ARRAY\s+JOIN\s+tables`)

	var seen int
	for _, path := range realRepoSQLFiles(t) {
		if !strings.HasPrefix(filepath.Base(path), "system.query_log_details") {
			continue
		}
		seen++
		body := readFileForTest(t, path)
		if !strings.Contains(strings.ToUpper(body), "LEFT ARRAY JOIN TABLES") {
			t.Errorf("%s: 必须使用 LEFT ARRAY JOIN tables，否则零表的失败会被丢弃", path)
		}
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "--") {
				continue
			}
			if plainArrayJoin.MatchString(line) {
				t.Errorf("%s: 第 %q 行出现了裸的 ARRAY JOIN tables", path, strings.TrimSpace(line))
			}
		}
	}
	if seen == 0 {
		t.Fatal("未找到 system.query_log_details*.sql 文件 —— 该测试没有在守护任何东西")
	}
}

// part_log 的聚合完全不采集 part_name，而这个测试守护它可能重新出现的
// 两种方式。
//
// 作为 GROUP BY 键它是灾难性的：part_name 对每个 part 都是唯一的，因此以它为
// 键会把“聚合”变成源表逐行的拷贝 —— 在一个玩具数据集上 868 个事件产生 868 行，
// 在 7 天的生产流量上则是数百万行。
//
// 用 any(part_name) 采样则只是毫无用处：在分组覆盖的任意多个 part 中取一个
// 任意的 part，而且无法说明为什么选中它。它不是任何人能据此排查问题的抓手，
// 因此被直接去掉，而不是降级为采样。
func TestPartLog_DoesNotCollectPartName(t *testing.T) {
	var seen int
	for _, path := range realRepoSQLFiles(t) {
		if !strings.HasPrefix(filepath.Base(path), "system.part_log") {
			continue
		}
		seen++

		// 匹配前先去注释文本 —— 整行注释和行尾注释都去掉。这些文件会详细解释
		// 为什么没有 part_name，那段说明文字不能被当作投影。做法与
		// gov_leak_test.go 相同。
		var code []string
		for _, line := range strings.Split(readFileForTest(t, path), "\n") {
			if i := strings.Index(line, "--"); i >= 0 {
				line = line[:i]
			}
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				code = append(code, trimmed)
			}
		}

		if strings.Contains(strings.Join(code, "\n"), "part_name") {
			t.Errorf("%s: 不得采集 part_name —— 作为分组键它会逐行拷贝"+
				"源表，而 any(part_name) 是一个没人能据此行动的任意"+
				"采样", path)
		}
	}
	if seen == 0 {
		t.Fatal("未找到 system.part_log*.sql 文件 —— 该测试没有在守护任何东西")
	}
}

// 自由文本列必须用 leftUTF8 截断，绝不能用 left。
//
// left() 按字节计数。截断点落在多字节字符内部时会输出一个孤立的续接字节，
// 而 collector 会把 ClickHouse 的响应原样写到磁盘（executor.go），因此下游
// 没有任何东西会修复它：这一 .jsonl 行不再是合法的 UTF-8。严格的读取器会直接
// 拒绝它 —— Python 的 json 会对整行抛出 "invalid continuation byte" ——
// 而 jq 比较宽容，会悄悄替换为 U+FFFD，同样严重地破坏了文本，却不作声。
// 客户 SQL 中任何非 ASCII 字面量（'München'、CJK 标识符）都能触发，
// 这很常见而非罕见。
//
// leftUTF8 按码点计数，并在 22.8 中与 left 一同注册，因此它在支持版本下限上
// 可用，无需版本门控。
func TestArchiveQueries_TruncateFreeTextWithLeftUTF8(t *testing.T) {
	// 有文档记录的豁免项，以仓库相对路径为键。两条理由都是关于
	// 该值永远不会以原始截断文本的形式进入输出，而不是关于截断本身总体上安全。
	exempt := map[string]string{
		// last_error_trace 是 Array(UInt64)，被字符串化为方括号包裹的 ASCII
		// 数字，因此没有多字节字符会被截断。
		"queries.onprem/system.errors.sql": "toString(Array(UInt64)) is ASCII",
		"queries.cloud/system.errors.sql":  "toString(Array(UInt64)) is ASCII",
		// gov 会把截断后的 message 送入 SHA256 并以十六进制输出，
		// 因此非法字节不可能存活进归档。
		"queries.gov/system.text_log.sql": "truncated value is hashed to hex",
	}

	// 匹配 left( 但不匹配 leftUTF8( —— \b 帮不上忙，因为 "left" 是
	// "leftUTF8" 的前缀。
	bareLeft := regexp.MustCompile(`(?i)\bleft\s*\(`)

	var seen int
	for _, path := range realRepoSQLFiles(t) {
		rel := strings.TrimPrefix(filepath.ToSlash(path), "../../")
		if _, ok := exempt[rel]; ok {
			continue
		}
		seen++
		for i, line := range strings.Split(readFileForTest(t, path), "\n") {
			// 去掉注释 —— 整行注释和行尾注释都去掉。这些文件会详细讨论
			// left() 与 leftUTF8()，那段说明文字不能被匹配。
			if j := strings.Index(line, "--"); j >= 0 {
				line = line[:j]
			}
			if strings.TrimSpace(line) == "" {
				continue
			}
			// 先移除所有 leftUTF8(，这样剩下的只可能包含按字节截断的 left(。
			stripped := strings.ReplaceAll(strings.ReplaceAll(line, "leftUTF8(", ""), "leftUTF8 (", "")
			if bareLeft.MatchString(stripped) {
				t.Errorf("%s:%d: 使用了按字节截断的 left() —— 请改用 leftUTF8()，否则"+
					"归档的 .jsonl 可能包含非法 UTF-8：%q",
					rel, i+1, strings.TrimSpace(line))
			}
		}
	}
	if seen == 0 {
		t.Fatal("未找到非豁免的 .sql 文件 —— 该测试没有在守护任何东西")
	}
}

// 归档的 text_log collector 自称“Warning and worse”。在 level 枚举 Enum8 中，
// Critical 位于 Fatal 和 Error 之间，因此 ('Warning','Error','Fatal') 这样的
// 过滤会悄悄丢掉支持工程师打开 bundle 要找的正是那些行。dashboard 自己的
// text_log 查询已经包含 Critical；归档必须与之一致，否则两者对服务器上
// 发生了什么会给出不同说法。
func TestTextLog_IncludesCritical(t *testing.T) {
	levelFilter := regexp.MustCompile(`(?i)level\s+IN\s*\(([^)]*)\)`)

	var seen int
	for _, path := range realRepoSQLFiles(t) {
		if !strings.HasPrefix(filepath.Base(path), "system.text_log") {
			continue
		}
		// text_log_keeper_1_day 刻意读取所有级别：它统计的 Keeper 会话标记
		//（"Connected to ZooKeeper"、"Finalizing session"）是 Information/Debug
		// 行，因此 Warning-and-worse 过滤会返回空。它是一张标记直方图，
		// 不是严重级别切片，Critical 保证不适用。
		if strings.HasPrefix(filepath.Base(path), "system.text_log_keeper") {
			continue
		}
		seen++
		body := readFileForTest(t, path)
		m := levelFilter.FindStringSubmatch(body)
		if m == nil {
			t.Errorf("%s: 未找到 level IN (...) 过滤", path)
			continue
		}
		for _, lvl := range []string{"Warning", "Error", "Critical", "Fatal"} {
			if !strings.Contains(m[1], "'"+lvl+"'") {
				t.Errorf("%s: level 过滤 %q 漏掉了 %s", path, strings.TrimSpace(m[0]), lvl)
			}
		}
	}
	if seen == 0 {
		t.Fatal("未找到 system.text_log*.sql 文件 —— 该测试没有在守护任何东西")
	}
}
