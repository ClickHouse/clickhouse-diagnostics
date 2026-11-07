package runlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRender_SectionsAndOrdering(t *testing.T) {
	r := New()
	r.SetMeta("mode", "onprem")
	r.SetMeta("server", "26.7.5.10")
	r.Record(Entry{Stage: "collector", Name: "system.parts.sql", Status: "ok", Duration: 1200 * time.Millisecond, Bytes: 66_528_876, Rows: 50000})
	r.Record(Entry{Stage: "collector", Name: "system.part_log_3_days.sql", Source: "23.11.1.0", Status: "ok", Duration: 41 * time.Second, Bytes: 192_796_599, Rows: 556430})
	r.Record(Entry{Stage: "collector", Name: "system.crash_log.sql", Status: "failed", Duration: 20 * time.Millisecond, Rows: -1,
		Error: "error executing query: non-OK status: 404, body: Code: 60. DB::Exception: Unknown table expression identifier 'system.crash_log'\nsecond line | with pipe " + strings.Repeat("x", 400)})
	r.Record(Entry{Stage: "alert", Name: "too_many_parts.yaml", Status: "fired", Duration: 300 * time.Millisecond, Rows: -1, Extra: "1 instance(s)"})
	r.Phase("collectors", 43*time.Second, "")
	r.Phase("dashboard", 4*time.Second, "")
	out := r.Render(time.Now().Add(50 * time.Second))

	for _, want := range []string{
		"clickhouse-diagnostic 执行日志",
		"mode:     onprem",
		"collectors：2 成功，1 失败，0 为空",
		"alerts：    1 条规则 — 1 触发，0 正常，0 失败，0 跳过",
		"最耗时的 collector",
		"失败的 collector（1）",
		"| # | stage | name | source | status | duration_ms | bytes | rows | note | error |",
		"| 2 | collector | system.part_log_3_days.sql | 23.11.1.0 | ok | 41000 | 192796599 | 556430 |  |  |",
		"phases：    collectors 43.00 s | dashboard 4.00 s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("渲染结果缺少 %q\n---\n%s", want, out)
		}
	}
	// 最耗时列表按耗时排序：part_log 在前。
	top := out[strings.Index(out, "最耗时"):]
	if strings.Index(top, "system.part_log_3_days.sql") > strings.Index(top, "system.parts.sql") {
		t.Error("最耗时列表未按耗时排序")
	}
	// 错误文本：已截断、单行、管道符已替换，以保证表格仍可解析。
	if strings.Contains(out, strings.Repeat("x", 301)) {
		t.Error("错误文本未被截断")
	}
	failed := out[strings.Index(out, "| 3 | collector"):]
	failed = failed[:strings.Index(failed, "\n")]
	if strings.Count(failed, "|") != 11 {
		t.Errorf("失败行含有游离的管道符或换行：%q", failed)
	}
}

func TestWrite_CreatesFileAndNilIsSafe(t *testing.T) {
	dir := t.TempDir()
	r := New()
	r.Record(Entry{Stage: "collector", Name: "system.version.sql", Status: "ok", Duration: time.Millisecond, Bytes: 25, Rows: 1})
	p, err := r.Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p) != FileName {
		t.Errorf("文件名不符合预期：%s", p)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	var nilRec *Recorder
	nilRec.Record(Entry{})
	nilRec.Phase("x", 0, "")
	if p, err := nilRec.Write(dir); err != nil || p != "" {
		t.Errorf("nil 记录器必须是无操作，得到 %q %v", p, err)
	}
}

func TestGovRedaction(t *testing.T) {
	r := New().WithGov(true)
	r.Record(Entry{Stage: "collector", Name: "system.parts.sql", Status: "failed", Rows: -1,
		Error: "error executing query: non-OK status: 404, body: Code: 60. DB::Exception: Unknown table expression identifier 'customer_db.orders' in scope SELECT … (UNKNOWN_TABLE)"})
	r.Record(Entry{Stage: "collector", Name: "system.tables.sql", Status: "failed", Rows: -1, Error: "dial tcp 10.1.2.3:8123: connection refused"})
	r.Phase("dashboard", time.Second, "failed: could not read customer_db.orders")
	out := r.Render(time.Now())
	r.Record(Entry{Stage: "collector", Name: "system.x.sql", Status: "failed", Rows: -1, Error: "Code: 60. DB::Exception: something about (CUSTOMER_TABLE) (UNKNOWN_TABLE)"})
	for _, leak := range []string{"customer_db", "orders", "10.1.2.3", "CUSTOMER_TABLE", "UNKNOWN_TABLE"} {
		if strings.Contains(out, leak) {
			t.Errorf("gov 日志泄漏了 %q：\n%s", leak, out)
		}
	}
	for _, want := range []string{"Code: 60 — gov 模式下已隐去消息", "gov 模式下已隐去错误文本"} {
		if !strings.Contains(out, want) {
			t.Errorf("gov 日志缺少 %q", want)
		}
	}
	// 非 gov 模式保留文本。
	plain := New()
	plain.Record(Entry{Stage: "collector", Name: "x.sql", Status: "failed", Rows: -1, Error: "Code: 60. Unknown table customer_db.orders (UNKNOWN_TABLE)"})
	if !strings.Contains(plain.Render(time.Now()), "customer_db.orders") {
		t.Error("非 gov 记录器必须保留错误文本")
	}
}
