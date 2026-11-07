package main

import (
	"testing"

	"clickhouse-diagnostic/internal/runlog"
)

// checks 和 dashboard 会从执行日志中了解某个文件为何缺失：会标明
// skipped 和 failed，而服务器不存在的表则只是“未采集”。
func TestCollectorStatus(t *testing.T) {
	rec := runlog.New()
	rec.Record(runlog.Entry{Stage: "collector", Name: "system.parts.sql", Status: "skipped", Extra: "essential collection"})
	rec.Record(runlog.Entry{Stage: "collector", Name: "system.errors.sql", Status: "failed", Error: "Code: 159. Timeout exceeded (TIMEOUT_EXCEEDED)"})
	rec.Record(runlog.Entry{Stage: "collector", Name: "system.text_log.sql", Status: "failed", Error: "Code: 60. Table system.text_log does not exist. (UNKNOWN_TABLE)"})
	rec.Record(runlog.Entry{Stage: "collector", Name: "system.disks.sql", Status: "ok"})
	st := collectorStatus(rec)
	if st["system.parts"].State != "skipped" || st["system.parts"].Reason != "essential collection" {
		t.Errorf("skipped 状态：%+v", st["system.parts"])
	}
	if st["system.errors"].State != "failed" {
		t.Errorf("failed 状态：%+v", st["system.errors"])
	}
	if _, ok := st["system.text_log"]; ok {
		t.Error("不存在的表不算失败")
	}
	if len(st) != 2 {
		t.Errorf("只应有 skipped 和 failed 的 collector：%v", st)
	}
}
