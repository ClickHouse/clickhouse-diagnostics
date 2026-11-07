package dashboard

import (
	"strings"
	"testing"
)

// Logs 面板之所以安全，只是因为它有界。原始服务器日志会被 tail 复制进 bundle，
// 每个文件最多 50 MiB；把它们嵌入会生成浏览器无法打开的文档——因此改为在
// Collected Files 下列出链接。这些上限就是护栏。
func TestTextLogSQL_IsBounded(t *testing.T) {
	sql := (&Generator{mode: "onprem"}).textLogSQL()
	for _, want := range []string{
		"LIMIT 1000",                              // textLogRowCap
		"leftUTF8(message, 300)",                  // textLogMessageCap——leftUTF8，因此上限确实是字符数，截断不会拆开一个字符
		"INTERVAL 24 HOUR",                        // 仅最近
		"'Warning', 'Error', 'Critical', 'Fatal'", // 仅分诊过的——Critical 位于 Fatal 和 Error 之间，因此必须在这里
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("text_log 查询丢失了它的界 %q：\n%s", want, sql)
		}
	}
	if textLogRowCap > 2000 || textLogMessageCap > 500 {
		t.Errorf("上限提高到了 payload 无法承载的程度：%d 行 x %d 字符",
			textLogRowCap, textLogMessageCap)
	}
}

// 分析器会把 WHERE 中的标识符解析到 SELECT 别名，因此
// `toString(event_time) AS event_time` + `WHERE event_time > now() - ...`
// 会把 String 与 DateTime 比较 → NO_COMMON_TYPE（26.4.5.143 上为 code 386）。
// 谓词必须放在子查询中，那里的 event_time 仍然是 DateTime。
func TestTextLogSQL_FiltersOnTypedTimestamp(t *testing.T) {
	sql := (&Generator{mode: "onprem"}).textLogSQL()
	where := strings.Index(sql, "WHERE event_time >")
	alias := strings.Index(sql, "toString(event_time) AS event_time")
	if where < 0 || alias < 0 {
		t.Fatalf("查询形态意外改变：\n%s", sql)
	}
	if where < alias {
		t.Error("event_time 谓词不得解析到字符串化后的别名")
	}
	if !strings.Contains(sql, "FROM (") {
		t.Errorf("期望过滤器被包裹在子查询中：\n%s", sql)
	}
}

func TestTemplate_LogsPanelIsConditional(t *testing.T) {
	for _, want := range []string{
		`id="sec-logs"`, `id="nav-logs"`, "const rows=DATA.text_log||[];",
		"if(!rows.length) return;", "window.logsFilter", "log-pagination",
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("logs 面板缺少 %q", want)
		}
	}
	// 分页：行数上限约束 payload，分页约束 DOM。
	if !strings.Contains(htmlTemplate, "const PAGE=100;") {
		t.Error("logs 表必须分页；1000 行 <tr> 正是页面变慢的原因")
	}
}
