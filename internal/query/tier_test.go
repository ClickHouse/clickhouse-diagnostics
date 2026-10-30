package query

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseTier(t *testing.T) {
	for in, want := range map[string]string{
		"SELECT 1":                                  TierFull,
		"-- tier: essential\nSELECT 1":              TierEssential,
		"-- a comment\n--   tier:   full  \nSELECT": TierFull,
		"SELECT 1 -- tier: essential":               TierFull, // only a line of its own counts
	} {
		got, err := ParseTier(in)
		if err != nil || got != want {
			t.Errorf("ParseTier(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseTier("-- tier: fast\nSELECT 1"); err == nil {
		t.Error("an unknown tier was accepted")
	}
}

// Every variant of a collector (root and version directories) must declare
// the same tier — otherwise the essential set would change with the server
// version — and the collectors that decide most incidents must be essential
// in every mode that has them.
func TestShippedQueryFiles_TiersAreConsistent(t *testing.T) {
	mustBeEssential := []string{"system.version", "system.metrics", "system.asynchronous_metrics", "system.processes",
		"system.errors", "system.replicas", "system.replication_queue", "system.merges", "system.mutations", "system.disks",
		"system.zookeeper_connection", "system.text_log", "system.error_log_7_days", "system.part_log_3_days",
		"system.metric_log_7_days", "system.query_log_details_7_days"}
	mustBeFull := []string{"system.tables", "system.columns", "system.parts", "system.detached_parts", "system.clusters", "system.stack_trace"}
	for _, mode := range []string{"onprem", "cloud", "gov"} {
		root := "../../queries." + mode
		tiers := map[string]string{}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".sql") {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			tier, err := ParseTier(string(b))
			if err != nil {
				t.Errorf("%s: %v", path, err)
				return nil
			}
			name := strings.TrimSuffix(d.Name(), ".sql")
			if prev, ok := tiers[name]; ok && prev != tier {
				t.Errorf("%s/%s: variants disagree on the tier (%s vs %s)", mode, name, prev, tier)
			}
			tiers[name] = tier
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range mustBeEssential {
			if tier, ok := tiers[n]; ok && tier != TierEssential {
				t.Errorf("%s/%s must be essential", mode, n)
			}
		}
		for _, n := range mustBeFull {
			if tier, ok := tiers[n]; ok && tier != TierFull {
				t.Errorf("%s/%s must stay full (expensive per table)", mode, n)
			}
		}
	}
}

func TestApply_MaxLookbackCapsDefaultsOnly(t *testing.T) {
	sql := "WHERE event_time > {from:7d} AND event_time > {from:3d} AND event_time > {from:12h} AND event_time <= {to:now}"
	got := Apply(sql, Vars{MaxLookback: 24 * time.Hour})
	for _, want := range []string{"now() - INTERVAL 24 HOUR AND event_time > now() - INTERVAL 24 HOUR", "now() - INTERVAL 12 HOUR", "<= now()"} {
		if !strings.Contains(got, want) {
			t.Errorf("capped window lacks %q:\n%s", want, got)
		}
	}
	// An explicit -from wins over the cap: the operator asked for that window.
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if got := Apply(sql, Vars{MaxLookback: 24 * time.Hour, From: from}); !strings.Contains(got, "2026-10-01 00:00:00") || strings.Contains(got, "24 HOUR") {
		t.Errorf("-from did not win over the cap:\n%s", got)
	}
	// No cap: the query's own default.
	if got := Apply(sql, Vars{}); !strings.Contains(got, "INTERVAL 7 DAY") {
		t.Errorf("uncapped window changed:\n%s", got)
	}
}

// The collectors that wait on a live Keeper session say so, in every
// variant; the cheap replicas columns do not.
func TestShippedQueryFiles_NeedsKeeper(t *testing.T) {
	want := map[string]bool{"system.zookeeper_connection": true, "system.database_replicas": true,
		"system.replicas_problem_tables": true, "system.distributed_ddl_queue": true, "system.replicas": false, "system.errors": false}
	for _, mode := range []string{"onprem", "cloud", "gov"} {
		_ = filepath.WalkDir("../../queries."+mode, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".sql") {
				return err
			}
			name := strings.TrimSuffix(d.Name(), ".sql")
			w, ok := want[name]
			if !ok {
				return nil
			}
			b, _ := os.ReadFile(p)
			if NeedsKeeper(string(b)) != w {
				t.Errorf("%s: needs keeper = %v, want %v", p, !w, w)
			}
			return nil
		})
	}
}
