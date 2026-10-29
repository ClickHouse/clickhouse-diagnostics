package alert

import (
	"fmt"
	"math"
	"sort"
	"time"

	"clickhouse-diagnostic/internal/bundle"
)

// Shared helpers for the Go checks: they recreate, over the collected
// files, the few SQL functions the rules used.

// serverClock returns the instant the rules' windows end — the server's
// now() at collection (system.version collected_at), or the explicit -to
// when that is earlier — and the server timezone every DateTime in the
// files is written in.
func serverClock(b *bundle.Bundle) (time.Time, *time.Location, error) {
	rows, err := b.Rows("system.version")
	if err != nil {
		return time.Time{}, nil, err
	}
	if len(rows) == 0 || rows[0].Str("collected_at") == "" {
		return time.Time{}, nil, fmt.Errorf("system.version has no collected_at (collected by an older version of the tool)")
	}
	loc := time.UTC
	if tz := rows[0].Str("timezone"); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	now, err := time.ParseInLocation("2006-01-02 15:04:05", rows[0].Str("collected_at"), loc)
	if err != nil {
		return time.Time{}, nil, fmt.Errorf("system.version collected_at: %w", err)
	}
	// With -to the files end there; the rules' look-back ends with them.
	if end := b.WindowEnd(); !end.IsZero() && end.Before(now) {
		now = end.In(loc)
	}
	return now, loc, nil
}

// serverHost is the host the onprem/gov files describe (hashed in gov).
func serverHost(b *bundle.Bundle) string {
	if rows, err := b.Rows("system.version"); err == nil && len(rows) > 0 {
		return rows[0].Str("hostname")
	}
	return ""
}

// parseTime reads a DateTime string from a collected row in the server's
// timezone; ok=false when empty or malformed.
func parseTime(s string, loc *time.Location) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04:05.000000", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// dateDiffHours is ClickHouse dateDiff('hour', a, b): the number of hour
// boundaries crossed, not the elapsed hours rounded.
func dateDiffHours(a, b time.Time) int64 {
	ta := a.Truncate(time.Hour)
	tb := b.Truncate(time.Hour)
	return int64(tb.Sub(ta) / time.Hour)
}

// formatReadableSize is ClickHouse formatReadableSize: powers of 1024,
// two decimals ("100.00 B", "1.50 GiB").
func formatReadableSize(n float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	i := 0
	for math.Abs(n) >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	return fmt.Sprintf("%.2f %s", n, units[i])
}

// roundTo is ClickHouse round(x, d) on a Float64: nearest, ties to even.
func roundTo(x float64, d int) float64 {
	p := math.Pow(10, float64(d))
	return math.RoundToEven(x*p) / p
}

// medianHigh is quantileExactHigh(0.5): the upper median.
func medianHigh(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return s[len(s)/2]
}

// row builds a result row (column → value) in the shape the message uses.
type row = map[string]interface{}
