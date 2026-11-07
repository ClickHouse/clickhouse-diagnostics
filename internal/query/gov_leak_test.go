package query

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestGovQueries_NoRawIdentifiersOrDDL 在 CI 阶段守护 gov 模式的承诺：
// queries.gov/ 下的每个 .sql（根目录和版本子目录）都必须哈希标识符列，
// 且不得随包携带 schema/DDL 文本。
//
// 之所以有这个测试，是因为该承诺曾被一个 onprem 变体的逐字拷贝悄悄破坏 ——
// queries.gov/25.4.1.0/system.tables.sql 曾携带原始的 database/table 名称
// 以及完整 DDL，直到有人阅读代码时才发现。当 gov 镜像 onprem 的版本阶梯时，
// 复制粘贴是天然的失败模式，因此它需要测试，而不是靠警觉。
func TestGovQueries_NoRawIdentifiersOrDDL(t *testing.T) {
	govDir := "../../queries.gov"
	if _, err := os.Stat(govDir); os.IsNotExist(err) {
		t.Skip("queries.gov/ 不存在")
	}

	// 会成片暴露 schema 或基础设施文本的列。它们绝不能出现在 gov 查询中，
	// 无论是否哈希 —— 对 DDL 数据块做哈希毫无意义，而且它在 gov 模式下
	// 没有诊断价值。
	forbidden := []string{
		"create_table_query",
		"engine_full",
		"as_select",
		"metadata_path",
		"data_paths",
		"loading_dependent_database",
		"loading_dependent_table",
		"parameterized_view_parameters",
	}
	// dependencies_* / loading_dependencies_* 过去被完全禁用。
	// 它们是 schema graph（模式关系图）的边，而一个由哈希后表名组成的数组
	// 所暴露的信息，并不比它旁边哈希后的 `database` / `name` 列更多 ——
	// 因此它们移到了下面的 mustBeHashed 中，这里的守护条件是文件把它们
	// 包进 SHA256（对元素做 arrayMap），而不是文件从不提及它们。
	// loading_dependent_* 保持禁用：关系图不需要它们，
	// 每条边只需要一个方向。

	// 如果文件选择了这些标识符列，就必须对它们做哈希。
	//
	// 按文件而不是按行检查：gov 查询可以合法地在子查询中选择裸列，
	// 再由外层 SELECT 对结果做哈希（system.disks 正是如此）。因此规则是
	// “只要这个文件投影了该标识符，就必须在某处对它做哈希” ——
	// 这恰恰能守护复制粘贴造成的回归（一个被丢进 queries.gov/ 的 onprem
	// 文件不会哈希任何东西）。它无法捕获既哈希某列、又同时原样输出该列的
	// 文件；那需要真正的结果列分析，因此仍属于评审关注点。
	// 覆盖标识符列，以及内部携带标识符的自由文本列 —— 其中每一列都是在
	// 本分支评审时发现为原始值并手工加上哈希的，因此守护必须包含它们，
	// 否则只能防住没人犯过的泄漏。
	mustBeHashed := []string{
		"database", "table", "name", "user", "path", "host_name", "host_address",
		"last_exception", "postpone_reason", "last_error_message",
		"partition", "origin", "replica_name", "comment", "source",
		// schema graph 的边和 MV（物化视图）的写入目标（system.tables），
		// 以及 system.view_refreshes 的 view 列 —— 全都是 `db` / `table` 名称。
		"dependencies_database", "dependencies_table",
		"loading_dependencies_database", "loading_dependencies_table",
		"target_database", "target_table", "view",
		// parts_max_block_recent 以及之后每个 per-host gov 文件中的
		// per-replica 身份（hostName()）。partition_id 不在此列：按约定
		// 每个 gov parts 文件都原样携带它（被哈希的是 `partition` 表达式）。
		"hostname",
	}

	// 有文档记录的豁免项：列名与某个标识符冲突，但其值不是客户数据。
	// 以 BASE 名称为键，这样未来的版本目录拷贝（queries.gov/25.x/system.errors.sql）
	// 会保留该豁免，而不是误报失败。
	exempt := map[string]map[string]bool{
		// system.errors.name 是 ClickHouse 错误常量（UNKNOWN_TABLE、
		// TOO_MANY_PARTS 等），不是 schema 标识符。
		"system.errors.sql": {"name": true},
		// system.settings.name / system.server_settings.name 是 ClickHouse
		// 设置名（max_threads、background_pool_size 等），不是客户标识符，
		// 对它们做哈希会让 collector 无法阅读：重点正是要看出哪些设置发生了偏离。
		//
		// 这两个 collector 中没有任何东西被哈希 —— 名称和值都没有 ——
		// 而且这是刻意的而非疏忽。设置值是一个调优旋钮，对它的哈希是不可逆的
		// 噪声，因为 PrintGovNameMapping 从 system.tables 构建映射 CSV，
		// 因此只覆盖 database 和 table 名称。
		// 所以 system.settings 原样输出每个值，而 system.server_settings 会
		// REMOVE（移除）少数点名客户基础设施的值（任何路径或 URL 形状的值，
		// 以及 database、replica 和 workload 名称），而不是加盐：
		// 被移除的值无法泄漏，也不需要密钥才能读取。该规则位于 SQL 文件的
		// 头部注释中，不在这里 —— mustBeHashed 只能表达“对它做哈希”，
		// 因此本测试无法强制执行移除。
		//
		// 值得评审者留意的一点：客户定义的自定义设置会以他们自己选择的名称
		// 出现在这里，因此 name 列并非像 system.errors.name 那样
		// 完全由 ClickHouse 控制。
		"system.settings.sql":        {"name": true},
		"system.server_settings.sql": {"name": true},
		// system.merge_tree_settings.name 是具有相同性质的第三层设置。
		// 它的 String 值在 SQL 中处理：policy 和 disk 名称做哈希，
		// 路径形状和列表形状的值 REMOVE。
		"system.merge_tree_settings.sql": {"name": true},
	}

	var files []string
	err := filepath.Walk(govDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(p, ".sql") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("未找到 gov .sql 文件")
	}

	for _, path := range files {
		rel, _ := filepath.Rel(govDir, path)
		t.Run(rel, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// 去掉注释 —— 整行注释和行尾注释都去掉 —— 以免注释在检查中
			// 藏起一个投影（否则 "name  -- hashed below" 会逃过检查，
			// 因为以前只截掉以 -- 开头的行）。注释可以合法地提及列名，
			// 因此它们也不能被算作投影。
			var code []string
			for _, line := range strings.Split(string(raw), "\n") {
				if i := strings.Index(line, "--"); i >= 0 {
					line = line[:i]
				}
				if s := strings.TrimSpace(line); s != "" {
					code = append(code, s)
				}
			}
			body := strings.Join(code, "\n")

			for _, f := range forbidden {
				if regexp.MustCompile(`\b` + f + `\b`).MatchString(body) {
					t.Errorf("携带了 schema/DDL 列 %q —— gov 不得暴露它", f)
				}
			}
			for _, id := range mustBeHashed {
				if exempt[filepath.Base(rel)][id] {
					continue
				}
				// 把该标识符作为独立投影来匹配。其中每一部分都被
				// 真实形态所需要：
				//   leading    — 行首、逗号或 SELECT（内联列表的第一项
				//                前面两者都没有）。
				//   qualifier  — 可选的 `t.` 前缀。没有它，带表限定的
				//                投影就永远匹配不上，因为前一个字符是 `.` ——
				//                而 `SELECT t.database, t.name FROM system.tables t`
				//                正是 dashboard 的 tablesListSQL 的写法，
				//                也就是最可能的粘贴来源。
				//   trailing   — 逗号、行尾或 FROM（单行查询的最后一项
				//                后面两者都没有）。
				projected := regexp.MustCompile(
					`(?mi)(?:^|,|\bSELECT\b)\s*(?:\w+\.)?` + id + `\s*(?:,|$|\s+FROM\b)`).MatchString(body)

				// 带别名的投影不满足上面任何 trailing 分支，
				// 因此需要自己的模式 —— 而且这种形态是真实存在的：
				// `t.database AS database` / `t.name AS table_name` 正是
				// queries.query_analysis/tables_for_query.sql 的写法，
				// 也就是本仓库内部的一个粘贴来源。
				//
				// 用单独的正则而不是可选的别名分组，因为 RE2 没有前瞻，
				// 而可选的 `\s+\w+` 会吞掉 `ORDER BY …, path ASC`。
				// 要求显式的 AS 关键字也能避免把 `partition_id AS …`
				// 读成 `partition`（id 之后的字符必须是空白）。
				aliased := regexp.MustCompile(
					`(?mi)(?:^|,|\bSELECT\b)\s*(?:\w+\.)?` + id + `\s+AS\s+\w+`).MatchString(body)
				// “已哈希”指某个 SHA256 表达式被起了这个名字的别名 ——
				// 覆盖直接形式（SHA256(concat(database, …)) AS database）
				// 以及对派生表达式做哈希（… concat(splitByChar('.', tables)[1],
				// …) as database），后者为 query_log_details 所使用。
				hashed := regexp.MustCompile(`(?i)SHA256\(.*\)\s*AS\s+` + id + `\b`).MatchString(body)
				if (projected || aliased) && !hashed {
					t.Errorf("投影了标识符 %q 却从未对它做哈希 —— 看起来像是 onprem/cloud 变体未经哈希的拷贝", id)
				}
			}
		})
	}
}

// TestGovQueries_HashedAliasDoesNotShadowAFilteredColumn 守护一个陷阱：
// 它会悄悄让过滤失效，而不使查询报错。
//
// 在 ClickHouse 中别名是全局的。一个投影
// `hex(SHA256(concat(database, '…'))) AS database` 的 gov 文件，会把
// `database` 以哈希值的形式纳入作用域，于是后面裸写的
// `WHERE database NOT IN ('system', …)` 会把一个 64 字符的十六进制字符串
// 与 'system' 比较，匹配每一行，排除条件就失效了 —— 查询仍然成功，
// 这就是为什么只有测试能抓住它。它曾出现在
// queries.gov/system.parts_max_block_recent.sql 上：在一台拥有 408 个
// system part 和 6 个用户 part 的服务器上，它让 LIMIT 200 塞满了
// system 日志分区。修复方法是用表别名限定被过滤的列（`p.database`），
// 正如 system.part_log_3_days.sql 用 `pl.` 所做的那样。
func TestGovQueries_HashedAliasDoesNotShadowAFilteredColumn(t *testing.T) {
	govDir := "../../queries.gov"
	if _, err := os.Stat(govDir); os.IsNotExist(err) {
		t.Skip("queries.gov/ 不存在")
	}
	// gov 会哈希、同时实际操作中也会被过滤的列。
	cols := []string{"database", "table", "name", "user", "host_name", "partition", "origin", "source", "replica_name", "view"}
	var files []string
	err := filepath.Walk(govDir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".sql") {
			files = append(files, p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("未找到 gov .sql 文件")
	}
	for _, path := range files {
		rel, _ := filepath.Rel(govDir, path)
		t.Run(rel, func(t *testing.T) {
			var sql strings.Builder
			for _, line := range strings.Split(readFileForTest(t, path), "\n") {
				if i := strings.Index(line, "--"); i >= 0 {
					line = line[:i]
				}
				sql.WriteString(line + "\n")
			}
			body := sql.String()
			lower := strings.ToLower(body)
			cut := strings.Index(lower, "where")
			if cut < 0 {
				return // 没有过滤，没有可被遮蔽的东西
			}
			filter := body[cut:]
			if g := strings.Index(strings.ToLower(filter), "group by"); g >= 0 {
				filter = filter[:g] // 对别名做 GROUP BY 是正确的、有意为之
			}
			for _, c := range cols {
				// 限定符刻意设为可选：写这个守护所针对的文件
				// 哈希的是 `p.database`，而只匹配裸列的检测器会跳过
				// 它要保护的那个查询 —— 从 WHERE 中删掉 `p.` 本会重现该回归，
				// 而测试仍然通过。
				hashed := regexp.MustCompile(`SHA256\(concat\(\s*(?:[A-Za-z_]\w*\.)?` + c + `\s*,`).MatchString(body)
				aliased := regexp.MustCompile(`(?i)\bAS\s+` + c + `\b`).MatchString(body)
				if !hashed || !aliased {
					continue
				}
				// 在过滤中不带限定地使用该名称会解析到哈希值。
				if regexp.MustCompile(`(^|[^.\w])` + c + `\s*(=|!=|<>|\bNOT\s+IN\b|\bIN\b|\bLIKE\b|\bNOT\s+LIKE\b)`).MatchString(filter) {
					t.Errorf("%s: %q 被哈希进同名别名，随后又不带限定地被过滤 —— "+
						"该别名遮蔽了原列，因此过滤会匹配每一行。请用表别名限定它（例如 p.%s）。",
						rel, c, c)
				}
			}
		})
	}
}
