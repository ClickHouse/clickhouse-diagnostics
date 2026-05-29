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

// reGovSalt restricts the gov-mode salt to a printable ASCII shape that
// cannot break out of the surrounding SQL string literal and cannot
// accidentally match the validator's forbidden-keyword regex (which uses
// \bWORD\b boundaries — keeping the salt strictly alphanumeric avoids
// any word-boundary surprise with values like "DELETE_2026").
var reGovSalt = regexp.MustCompile(`^[A-Za-z0-9]{8,64}$`)

// ValidateGovSalt rejects salts that contain anything other than
// 8–64 ASCII alphanumeric characters. The strict shape prevents SQL
// escaping issues and rules out salts that contain DML/DDL keywords
// as bare words.
func ValidateGovSalt(salt string) error {
	if salt == "" {
		return fmt.Errorf("gov-mode salt is required (use --salt or the interactive prompt)")
	}
	if !reGovSalt.MatchString(salt) {
		return fmt.Errorf("gov-mode salt must be 8–64 ASCII alphanumeric characters (A–Z, a–z, 0–9)")
	}
	return nil
}

// PrintGovNameMapping queries system.tables and emits a database/table
// → hex(SHA256(name+salt)) mapping using the customer-supplied salt
// (the same value the executor substitutes into '%salt%' in .sql files).
// The console summary shows up to `consoleLimit` rows so the operator can
// sanity-check the hashes; the full mapping is written to a CSV alongside
// the backup folder.
//
// The CSV is placed at outputDir (parent of backupDir), NOT inside
// backupDir, so the support archive does not contain it. The whole
// point of gov-mode hashing is that the salt-decoded names stay with
// the customer — leaking the mapping would defeat that.
//
// salt must be pre-validated (alphanumeric only) — see ValidateGovSalt.
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
		return fmt.Errorf("gov-mode mapping query failed: %w", err)
	}

	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		fmt.Println("\n[gov mode] No user databases/tables found — name mapping skipped.")
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
		return fmt.Errorf("create mapping file: %w", err)
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
	fmt.Println("[gov mode] Hashed database/table mapping (keep LOCAL — do NOT share with support):")
	fmt.Printf("  %-30s %-30s %-16s %-16s\n", "database", "table", "database_hash", "table_hash")
	fmt.Printf("  %s\n", strings.Repeat("-", 96))
	for i, r := range rows {
		if i == consoleLimit {
			fmt.Printf("  … and %d more — see CSV for the full list\n", len(rows)-consoleLimit)
			break
		}
		fmt.Printf("  %-30s %-30s %-16s %-16s\n",
			truncTo(r.db, 30), truncTo(r.tbl, 30),
			truncTo(r.dbHash, 14)+"…", truncTo(r.tblHash, 14)+"…")
	}
	fmt.Printf("\nFull mapping written to: %s\n", mapPath)
	fmt.Println("(file is OUTSIDE the support archive — do not send it to ClickHouse support)")
	fmt.Println()
	return nil
}

func truncTo(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
