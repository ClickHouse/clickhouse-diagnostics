package alert

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"clickhouse-diagnostic/internal/bundle"
)

// Check evaluates one rule over the collected data: it returns the rows
// that fire it, with the columns its message uses. A source the run did
// not collect is returned as *bundle.NotCollected.
type Check func(b *bundle.Bundle, mode string) ([]map[string]interface{}, error)

var (
	checksMu sync.RWMutex
	checks   = map[string]Check{}
)

// RegisterCheck makes a check available to rules as `check: go:<name>`.
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
		return nil, fmt.Errorf("unknown check %q (want go:<registered name>)", spec)
	}
	return c, nil
}

// evalCheck runs a Go-check rule. Nothing is sent to the server: the rows
// come from the files the collectors wrote.
func (ev *Evaluator) evalCheck(def Definition, r Result, sql string, followUps []Investigation) Result {
	c, err := lookupCheck(def.Check)
	if err != nil {
		r.Error = err.Error()
		fmt.Printf("  [alert] ERROR %q: %v\n", def.Name, err)
		return r
	}
	if ev.bundle == nil {
		r.Error = "no collected data to evaluate the rule over"
		return r
	}
	rows, err := c(ev.bundle, ev.mode)
	var nc *bundle.NotCollected
	if errors.As(err, &nc) {
		r.Skipped = true
		r.Reason = "not applicable: its source " + nc.Error()
		fmt.Printf("  [alert] skipped %q (%s)\n", def.Name, r.Reason)
		return r
	}
	if err != nil {
		r.Error = fmt.Sprintf("check: %v", err)
		r.Query, r.Investigate = sql, followUps
		return r
	}
	r.Rows = rows
	if len(rows) > 0 {
		fmt.Printf("  [alert] FIRED  %q — %d row(s)\n", def.Name, len(rows))
		r.Query, r.Investigate = sql, followUps
	}
	return r
}
