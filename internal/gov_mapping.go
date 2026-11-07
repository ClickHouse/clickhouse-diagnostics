package internal

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"clickhouse-diagnostic/pkg"
)

// reGovSalt 将 gov 模式的 salt 限制为可打印 ASCII 形态，
// 使其无法跳出周边 SQL 字符串字面量，也无法
// 意外匹配校验器的禁用关键词正则（后者使用
// \bWORD\b 边界——保持 salt 严格为字母数字可避免
// 像 "DELETE_2026" 这样的值引发任何词边界意外）。
var reGovSalt = regexp.MustCompile(`^[A-Za-z0-9]{8,64}$`)

// ValidateGovSalt 拒绝包含 8–64 个 ASCII 字母或数字之外
// 任何字符的 salt。严格形态可避免 SQL 转义问题，
// 并排除以裸词形式包含 DML/DDL 关键词的 salt。
func ValidateGovSalt(salt string) error {
	if salt == "" {
		return fmt.Errorf("gov 模式必须提供 salt（使用 --salt 或交互式提示）")
	}
	if !reGovSalt.MatchString(salt) {
		return fmt.Errorf("gov 模式的 salt 必须是 8–64 个 ASCII 字母或数字（A–Z、a–z、0–9）")
	}
	return nil
}

// PrintGovNameMapping 查询 system.tables，并使用客户提供的 salt
// （也就是执行器替换到 .sql 文件中 '%salt%' 的同一个值）
// 输出 database/table → hex(SHA256(name+salt)) 映射。
// 控制台摘要最多显示 `consoleLimit` 行，以便运维人员
// 抽查哈希；完整映射会与备份文件夹一起写入 CSV。
//
// CSV 放在 outputDir（backupDir 的父目录），而不是
// backupDir 内，因此支持归档不包含它。gov 模式哈希的
// 全部意义就在于经 salt 解码的名称留在客户处——
// 泄漏映射会毁掉这一点。
//
// salt 必须预先校验（仅字母数字）——见 ValidateGovSalt。
func PrintGovNameMapping(client *pkg.ClickHouseClient, outputDir, backupDir, salt string) error {
	const consoleLimit = 30

	q := fmt.Sprintf(
		`SELECT database, name AS table,
			hex(SHA256(concat(database, '%s'))) AS database_hash,
			hex(SHA256(concat(name, '%s'))) AS table_hash
		 FROM system.tables
		 WHERE database NOT IN ('system','information_schema','INFORMATION_SCHEMA')
		 ORDER BY database, name
		 FORMAT TSV`,
		salt, salt)

	raw, err := client.ExecuteQuery(q)
	if err != nil {
		return fmt.Errorf("gov 模式名称映射查询失败：%w", err)
	}

	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		fmt.Println("\n[gov 模式] 未发现用户数据库/表——跳过名称映射。")
		return nil
	}

	type row struct{ db, tbl, dbHash, tblHash string }
	var rows []row
	for _, line := range strings.Split(trimmed, "\n") {
		cols := strings.Split(line, "\t")
		if len(cols) != 4 {
			continue
		}
		rows = append(rows, row{cols[0], cols[1], cols[2], cols[3]})
	}

	mapPath := filepath.Join(outputDir, filepath.Base(backupDir)+"_gov_name_mapping.csv")
	f, err := os.OpenFile(mapPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("创建映射文件失败：%w", err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	if err := w.Write([]string{"database", "table", "database_hash", "table_hash"}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := w.Write([]string{r.db, r.tbl, r.dbHash, r.tblHash}); err != nil {
			return err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("[gov 模式] 已哈希的数据库/表映射（仅保留在本机——切勿与支持团队共享）：")
	fmt.Printf("  %-30s %-30s %-16s %-16s\n", "database", "table", "database_hash", "table_hash")
	fmt.Printf("  %s\n", strings.Repeat("-", 96))
	for i, r := range rows {
		if i == consoleLimit {
			fmt.Printf("  … 另有 %d 条——完整列表见 CSV\n", len(rows)-consoleLimit)
			break
		}
		fmt.Printf("  %-30s %-30s %-16s %-16s\n",
			truncTo(r.db, 30), truncTo(r.tbl, 30),
			truncTo(r.dbHash, 14)+"…", truncTo(r.tblHash, 14)+"…")
	}
	fmt.Printf("\n完整映射已写入：%s\n", mapPath)
	fmt.Println("（该文件位于支持归档之外——不要发送给 ClickHouse 支持团队）")
	fmt.Println()
	return nil
}

func truncTo(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
