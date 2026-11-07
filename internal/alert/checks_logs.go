package alert

import (
	"sort"
	"time"

	"clickhouse-diagnostic/internal/bundle"
)

// 对按小时分桶的日志文件（part_log_3_days、query_log_exceptions_1_day）做检查。
// 规则原本在“最近 N 小时”的原始行上运行；这些文件按小时分桶，因此一个窗口
// 就是结束时间晚于 now − N 的桶：最旧的桶可能再向前多覆盖将近一小时
//（这是按小时分桶可接受的精度损失）。

func init() {
	RegisterCheck("merges_stalled", checkMergesStalled)
	RegisterCheck("background_operation_failures", checkBackgroundFailures)
	RegisterCheck("high_exception_rate", checkHighExceptionRate)
	RegisterCheck("keeper_exception_spike", checkKeeperExceptionSpike)
	RegisterCheck("too_many_simultaneous_queries", checkTooManySimultaneous)
}

// inWindow 报告从 t 开始的小时桶是否结束于 now − d 之后，
// 并返回该桶的起点。
func inWindow(r bundle.Row, col string, now time.Time, loc *time.Location, d time.Duration) (time.Time, bool) {
	t, ok := parseTime(r.Str(col), loc)
	if !ok {
		return t, false
	}
	return t, t.Add(time.Hour).After(now.Add(-d)) && !t.After(now)
}

func fmtHour(t time.Time) string { return t.Format("2006-01-02 15:04:05") }

// merges_stalled：某台主机上有一小时新增 part 超过 100 个，
// 且没有成功的 merge。
func checkMergesStalled(b *bundle.Bundle, mode string) ([]row, error) {
	rows, err := b.Rows("system.part_log_3_days")
	if err != nil {
		return nil, err
	}
	now, loc, err := serverClock(b)
	if err != nil {
		return nil, err
	}
	type key struct{ host, hour string }
	type agg struct{ newParts, merges, failed int64 }
	m := map[key]*agg{}
	for _, r := range rows {
		t, ok := inWindow(r, "time", now, loc, 24*time.Hour)
		if !ok {
			continue
		}
		k := key{r.Str("hostname"), fmtHour(t)}
		a := m[k]
		if a == nil {
			a = &agg{}
			m[k] = a
		}
		switch r.Str("event_type") {
		case "NewPart":
			a.newParts += r.Int("count")
		case "MergeParts":
			if r.Int("error") == 0 {
				a.merges += r.Int("count")
			} else {
				a.failed += r.Int("count")
			}
		}
	}
	var out []row
	for k, a := range m {
		if a.newParts > 100 && a.merges == 0 {
			out = append(out, row{"hostname": k.host, "hour": k.hour, "new_parts": a.newParts, "merges": a.merges, "failed_merges": a.failed})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i]["hour"] != out[j]["hour"] {
			return out[i]["hour"].(string) < out[j]["hour"].(string)
		}
		return out[i]["hostname"].(string) < out[j]["hostname"].(string)
	})
	return out, nil
}

// background_operation_failures：同一 (hour, event type, error) 的失败超过 50 次，
// 取最大的 24 个。
func checkBackgroundFailures(b *bundle.Bundle, mode string) ([]row, error) {
	rows, err := b.Rows("system.part_log_3_days")
	if err != nil {
		return nil, err
	}
	now, loc, err := serverClock(b)
	if err != nil {
		return nil, err
	}
	type key struct {
		hour, event string
		code        int64
	}
	type agg struct {
		n   int64
		msg string
	}
	m := map[key]*agg{}
	for _, r := range rows {
		if r.Int("error") == 0 {
			continue
		}
		t, ok := inWindow(r, "time", now, loc, 24*time.Hour)
		if !ok {
			continue
		}
		k := key{fmtHour(t), r.Str("event_type"), r.Int("error")}
		a := m[k]
		if a == nil {
			a = &agg{}
			m[k] = a
		}
		a.n += r.Int("count")
		if a.msg == "" {
			a.msg = r.Str("exception")
		}
	}
	var out []row
	for k, a := range m {
		if a.n > 50 {
			out = append(out, row{"hour": k.hour, "event_type": k.event, "error": k.code, "failures": a.n, "example_message": a.msg})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["failures"].(int64) > out[j]["failures"].(int64) })
	if len(out) > 24 {
		out = out[:24]
	}
	return out, nil
}

// exceptionsByHour 从 query_log_exceptions_1_day 按 (hour, code) 统计失败查询，
// 该文件对每个查询只计一次（query_log_details_7_days 的按表行无法与
// 同一模式在不同表上的多次独立执行区分开来）。
func exceptionsByHour(b *bundle.Bundle, window time.Duration, keep func(r bundle.Row) bool) (map[[2]string]*excAgg, error) {
	rows, err := b.Rows("system.query_log_exceptions_1_day")
	if err != nil {
		return nil, err
	}
	now, loc, err := serverClock(b)
	if err != nil {
		return nil, err
	}
	out := map[[2]string]*excAgg{}
	for _, r := range rows {
		if !keep(r) {
			continue
		}
		t, ok := inWindow(r, "time", now, loc, window)
		if !ok {
			continue
		}
		k := [2]string{fmtHour(t), r.Str("exception_code")}
		a := out[k]
		if a == nil {
			a = &excAgg{}
			out[k] = a
		}
		a.n += r.Int("count")
		if a.msg == "" {
			a.msg = r.Str("exception")
		}
	}
	return out, nil
}

type excAgg struct {
	n   int64
	msg string
}

// high_exception_rate：一小时内同一错误码的失败查询超过 50 个。
func checkHighExceptionRate(b *bundle.Bundle, mode string) ([]row, error) {
	m, err := exceptionsByHour(b, 24*time.Hour, func(r bundle.Row) bool {
		return r.Int("exception_code") != 0 && r.Str("type") == "ExceptionWhileProcessing"
	})
	if err != nil {
		return nil, err
	}
	var out []row
	for k, a := range m {
		if a.n > 50 {
			out = append(out, row{"hour": k[0], "exception_code": (bundle.Row{"c": k[1]}).Int("c"), "error_count": a.n, "example_message": a.msg})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["error_count"].(int64) > out[j]["error_count"].(int64) })
	if len(out) > 24 {
		out = out[:24]
	}
	return out, nil
}

// keeper_exception_spike：一小时内以错误码 999 失败的查询超过 20 个。
func checkKeeperExceptionSpike(b *bundle.Bundle, mode string) ([]row, error) {
	m, err := exceptionsByHour(b, 24*time.Hour, func(r bundle.Row) bool {
		return r.Int("exception_code") == 999 && r.Str("type") == "ExceptionWhileProcessing"
	})
	if err != nil {
		return nil, err
	}
	var out []row
	for k, a := range m {
		if a.n > 20 {
			out = append(out, row{"hour": k[0], "error_count": a.n, "example_message": a.msg})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["hour"].(string) < out[j]["hour"].(string) })
	return out, nil
}

// too_many_simultaneous_queries：最近一小时内以错误码 202 被拒绝的查询
// 超过 10 个（结束时间晚于 now − 1 h 的小时桶：最多两个）。
func checkTooManySimultaneous(b *bundle.Bundle, mode string) ([]row, error) {
	m, err := exceptionsByHour(b, time.Hour, func(r bundle.Row) bool { return r.Int("exception_code") == 202 })
	if err != nil {
		return nil, err
	}
	var n int64
	msg := ""
	for _, a := range m {
		n += a.n
		if msg == "" {
			msg = a.msg
		}
	}
	if n <= 10 {
		return nil, nil
	}
	return []row{{"error_count": n, "example_message": msg}}, nil
}
