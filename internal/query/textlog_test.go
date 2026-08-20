package query

import (
	"strings"
	"testing"
	"time"

	"clickhouse-diagnostic/internal"
)

func win(t *testing.T) (time.Time, time.Time) {
	t.Helper()
	from, _ := time.Parse(time.RFC3339, "2026-08-01T10:00:00Z")
	return from, from.Add(2 * time.Hour)
}

// TestTextLogOpts_RequiresWindow pins the reason this collector is opt-in:
// text_log is high-volume, so an unbounded dump is refused rather than
// silently producing a multi-gigabyte archive.
func TestTextLogOpts_RequiresWindow(t *testing.T) {
	from, to := win(t)
	cases := []struct {
		name    string
		opts    TextLogOpts
		wantErr string
	}{
		{"no window at all", TextLogOpts{}, "requires both --from and --to"},
		{"only from", TextLogOpts{From: from}, "requires both --from and --to"},
		{"only to", TextLogOpts{To: to}, "requires both --from and --to"},
		{"inverted", TextLogOpts{From: to, To: from}, "must be after"},
		{"bad level", TextLogOpts{From: from, To: to, Level: "verbose"}, "not a ClickHouse log level"},
		{"valid", TextLogOpts{From: from, To: to}, ""},
		{"valid with level", TextLogOpts{From: from, To: to, Level: "warning"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.opts.Validate()
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if c.opts.RowLimit != DefaultTextLogRowLimit {
					t.Errorf("row limit should default to %d, got %d",
						DefaultTextLogRowLimit, c.opts.RowLimit)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("got %v, want error containing %q", err, c.wantErr)
			}
		})
	}
}

// TestTextLogSQL_VersionGate: message_format_string arrived in 23.1, so it
// must not be selected below that — the same gate the .sql rungs apply.
func TestTextLogSQL_VersionGate(t *testing.T) {
	from, to := win(t)
	opts := TextLogOpts{From: from, To: to}
	if err := opts.Validate(); err != nil {
		t.Fatal(err)
	}
	c := NewTextLogCollector(nil, "onprem")

	old := c.buildSQL(opts, internal.Version{Major: 22, Minor: 8})
	if strings.Contains(old, "message_format_string") {
		t.Error("22.8 must not select message_format_string (added in 23.1)")
	}
	newer := c.buildSQL(opts, internal.Version{Major: 23, Minor: 1})
	if !strings.Contains(newer, "message_format_string") {
		t.Error("23.1+ should select message_format_string")
	}
	if !strings.Contains(c.buildSQL(opts, internal.Version{Major: 25, Minor: 4}), "message_format_string") {
		t.Error("25.4 should select message_format_string")
	}
}

// TestTextLogSQL_ModeAwareTable: cloud must fan out, single-node must not.
func TestTextLogSQL_ModeAwareTable(t *testing.T) {
	from, to := win(t)
	opts := TextLogOpts{From: from, To: to}
	_ = opts.Validate()
	v := internal.Version{Major: 25, Minor: 4}

	cloud := NewTextLogCollector(nil, "cloud").buildSQL(opts, v)
	if !strings.Contains(cloud, "clusterAllReplicas(default, system.text_log)") {
		t.Errorf("cloud should fan out, got:\n%s", cloud)
	}
	for _, mode := range []string{"onprem", "gov"} {
		sql := NewTextLogCollector(nil, mode).buildSQL(opts, v)
		if strings.Contains(sql, "clusterAllReplicas") {
			t.Errorf("%s must query the local table, got:\n%s", mode, sql)
		}
	}
}

func TestTextLogSQL_WindowLevelAndLimit(t *testing.T) {
	from, to := win(t)
	opts := TextLogOpts{From: from, To: to, Level: "warning", RowLimit: 42}
	if err := opts.Validate(); err != nil {
		t.Fatal(err)
	}
	sql := NewTextLogCollector(nil, "onprem").buildSQL(opts, internal.Version{Major: 25, Minor: 4})

	for _, want := range []string{
		"2026-08-01 10:00:00", // from, UTC
		"2026-08-01 12:00:00", // to, UTC
		"LIMIT 42",
		"level <= 'Warning'", // enum is severity-ordered, so >= severity is <=
		"ORDER BY event_time_microseconds ASC",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL missing %q:\n%s", want, sql)
		}
	}
	// The generated SQL must survive the same read-only validation every
	// other query goes through.
	if err := ValidateQueryContent(sql); err != nil {
		t.Errorf("generated text_log SQL fails security validation: %v", err)
	}
}

func TestTextLogSQL_NoLevelFilterWhenUnset(t *testing.T) {
	from, to := win(t)
	opts := TextLogOpts{From: from, To: to}
	_ = opts.Validate()
	sql := NewTextLogCollector(nil, "onprem").buildSQL(opts, internal.Version{Major: 25, Minor: 4})
	if strings.Contains(sql, "level <=") {
		t.Error("no --text-log-level should mean no level filter")
	}
}
