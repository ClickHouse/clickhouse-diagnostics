package main

import (
	"testing"

	"clickhouse-diagnostic/internal/runlog"
)

// The checks and the dashboard learn from the execution log why a file is
// missing: skipped and failed are named, a table the server does not have
// is plain "not collected".
func TestCollectorStatus(t *testing.T) {
	rec := runlog.New()
	rec.Record(runlog.Entry{Stage: "collector", Name: "system.parts.sql", Status: "skipped", Extra: "essential collection"})
	rec.Record(runlog.Entry{Stage: "collector", Name: "system.errors.sql", Status: "failed", Error: "Code: 159. Timeout exceeded (TIMEOUT_EXCEEDED)"})
	rec.Record(runlog.Entry{Stage: "collector", Name: "system.text_log.sql", Status: "failed", Error: "Code: 60. Table system.text_log does not exist. (UNKNOWN_TABLE)"})
	rec.Record(runlog.Entry{Stage: "collector", Name: "system.disks.sql", Status: "ok"})
	st := collectorStatus(rec)
	if st["system.parts"].State != "skipped" || st["system.parts"].Reason != "essential collection" {
		t.Errorf("skipped: %+v", st["system.parts"])
	}
	if st["system.errors"].State != "failed" {
		t.Errorf("failed: %+v", st["system.errors"])
	}
	if _, ok := st["system.text_log"]; ok {
		t.Error("an absent table is not a failure")
	}
	if len(st) != 2 {
		t.Errorf("only skipped and failed collectors: %v", st)
	}
}
