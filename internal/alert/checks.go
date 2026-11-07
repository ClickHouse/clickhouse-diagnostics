package alert

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"clickhouse-diagnostic/internal/bundle"
)

// Check 对已采集的数据评估一条规则：返回触发它的行，
// 并带上规则消息所使用的列。本次运行未采集的数据源会以
// *bundle.NotCollected 返回。
type Check func(b *bundle.Bundle, mode string) ([]map[string]interface{}, error)

var (
	checksMu sync.RWMutex
	checks   = map[string]Check{}
)

// RegisterCheck 使规则可以通过 `check: go:<name>` 使用某个 check。
func RegisterCheck(name string, c Check) {
	checksMu.Lock()
	defer checksMu.Unlock()
	checks[name] = c
}

func lookupCheck(spec string) (Check, error) {
	name, ok := strings.CutPrefix(spec, "go:")
	checksMu.RLock()
	c, found := checks[name]
	checksMu.RUnlock()
	if !ok || !found {
		return nil, fmt.Errorf("未知 check %q（应为 go:<registered name>）", spec)
	}
	return c, nil
}

// evalCheck 运行 Go-check 规则。不会向服务器发送任何内容：
// 行来自 collector 写出的文件。
func (ev *Evaluator) evalCheck(def Definition, r Result, sql string, followUps []Investigation) Result {
	c, err := lookupCheck(def.Check)
	if err != nil {
		r.Error = err.Error()
		fmt.Printf("  [alert] ERROR %q: %v\n", def.Name, err)
		return r
	}
	if ev.bundle == nil {
		r.Error = "没有可用于评估该规则的已采集数据"
		return r
	}
	rows, err := c(ev.bundle, ev.mode)
	var nc *bundle.NotCollected
	if errors.As(err, &nc) {
		r.Skipped = true
		r.Reason = "不适用：其数据源 " + nc.Error()
		fmt.Printf("  [alert] skipped %q (%s)\n", def.Name, r.Reason)
		return r
	}
	if err != nil {
		r.Error = fmt.Sprintf("check 执行失败: %v", err)
		r.Query, r.Investigate = sql, followUps
		return r
	}
	r.Rows = rows
	if len(rows) > 0 {
		fmt.Printf("  [alert] FIRED  %q — %d 行\n", def.Name, len(rows))
		r.Query, r.Investigate = sql, followUps
	}
	return r
}
