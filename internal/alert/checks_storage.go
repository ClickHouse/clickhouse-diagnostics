package alert

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"clickhouse-diagnostic/internal/bundle"
)

// Storage, parts, crash and replication-state checks. Each mirrors the SQL
// of its rule file (still in the YAML as `query:`, for the customer) over
// the collected files.

func init() {
	RegisterCheck("disk_space_low", checkDiskSpaceLow)
	RegisterCheck("too_many_parts", checkTooManyParts)
	RegisterCheck("large_parts", checkLargeParts)
	RegisterCheck("detached_parts_exist", checkDetachedParts)
	RegisterCheck("crash_log_entries", checkCrashLog)
	RegisterCheck("replication_queue_errors", checkReplicationQueueErrors)
	RegisterCheck("mutation_running_too_long", checkMutationsTooLong)
	RegisterCheck("replica_readonly", checkReplicaReadonly)
}

var systemDBs = map[string]bool{"system": true, "information_schema": true, "INFORMATION_SCHEMA": true}

// disk_space_low: disks under 15 % free that hold table data (the disks of
// the storage policies user tables use; every disk when that is unknown).
func checkDiskSpaceLow(b *bundle.Bundle, mode string) ([]row, error) {
	disks, err := b.Rows("system.disks")
	if err != nil {
		return nil, err
	}
	// Data disks: policies used by non-system tables → their disks. When
	// tables or storage_policies were not collected (an essential run) the
	// set is empty and every disk counts, like the rule's empty(dd.names).
	data := map[string]bool{}
	if tables, err := b.Rows("system.tables"); err == nil {
		policies := map[string]bool{}
		for _, t := range tables {
			// gov hashes database names: is_system_db carries the fact
			system := systemDBs[t.Str("database")] || t.Int("is_system_db") == 1
			if !system && t.Str("storage_policy") != "" {
				policies[t.Str("storage_policy")] = true
			}
		}
		if sp, err := b.Rows("system.storage_policies"); err == nil {
			for _, p := range sp {
				if policies[p.Str("policy_name")] {
					if arr, ok := p["disks"].([]any); ok {
						for _, d := range arr {
							data[fmt.Sprint(d)] = true
						}
					}
				}
			}
		}
	}
	host := serverHost(b)
	var out []row
	for _, d := range disks {
		total, free := d.Float("total_bytes"), d.Float("free_bytes")
		if total <= 0 || free/total >= 0.15 {
			continue
		}
		if len(data) > 0 && !data[d.Str("name")] {
			continue
		}
		h := d.Str("hostname")
		if h == "" {
			h = host
		}
		out = append(out, row{"hostname": h, "disk_name": d.Str("name"), "free_space": formatReadableSize(free),
			"total_space": formatReadableSize(total), "free_pct": roundTo(free/total*100, 1)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["free_pct"].(float64) < out[j]["free_pct"].(float64) })
	return out, nil
}

// too_many_parts: partitions with more than 300 active parts.
func checkTooManyParts(b *bundle.Bundle, mode string) ([]row, error) {
	parts, err := b.Rows("system.parts_by_partition")
	if err != nil {
		return nil, err
	}
	var out []row
	for _, p := range parts {
		if n := p.Int("parts"); n > 300 {
			out = append(out, row{"database": p.Str("database"), "table": p.Str("table"), "partition_id": p.Str("partition_id"), "parts_count": n})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["parts_count"].(int64) > out[j]["parts_count"].(int64) })
	return out, nil
}

// large_parts: active parts above 150 GiB. system.parts keeps the 50 000
// largest parts, so every such part is in the file.
func checkLargeParts(b *bundle.Bundle, mode string) ([]row, error) {
	parts, err := b.Rows("system.parts")
	if err != nil {
		return nil, err
	}
	var out []row
	for _, p := range parts {
		bytes := p.Float("bytes_on_disk")
		if p.Int("active") != 1 || bytes <= 150*1024*1024*1024 {
			continue
		}
		ratio := 0.0
		if c := p.Float("data_compressed_bytes"); c > 0 {
			ratio = roundTo(p.Float("data_uncompressed_bytes")/c, 2)
		}
		out = append(out, row{"database": p.Str("database"), "table": p.Str("table"), "part_name": p.Str("name"),
			"disk_name": p.Str("disk_name"), "size_on_disk": formatReadableSize(bytes), "bytes_on_disk": int64(bytes),
			"uncompressed_size": formatReadableSize(p.Float("data_uncompressed_bytes")), "compression_ratio": ratio,
			"rows": p.Int("rows"), "modification_time": p.Str("modification_time")})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["bytes_on_disk"].(int64) > out[j]["bytes_on_disk"].(int64) })
	return out, nil
}

// detached_parts_exist: detached parts per table (the collector already
// applies the same scope and skips the system databases).
func checkDetachedParts(b *bundle.Bundle, mode string) ([]row, error) {
	rows, err := b.Rows("system.detached_parts")
	if err != nil {
		return nil, err
	}
	type agg struct {
		db, tb  string
		n       int64
		bytes   float64
		reasons []string
		sized   bool
	}
	byTable := map[string]*agg{}
	var order []string
	// Cloud reads every replica: a part on shared storage is listed once
	// per replica, with the same path. Count it once, as the rule's
	// single-replica read does. Elsewhere one host lists each part once
	// (and may hold two of one block range, the _tryN copies).
	parts := map[string]bool{}
	for _, r := range rows {
		if systemDBs[r.Str("database")] {
			continue
		}
		if p := r.Str("path"); mode == "cloud" && p != "" {
			if parts[p] {
				continue
			}
			parts[p] = true
		}
		k := r.Str("database") + "\x00" + r.Str("table")
		a := byTable[k]
		if a == nil {
			a = &agg{db: r.Str("database"), tb: r.Str("table")}
			byTable[k] = a
			order = append(order, k)
		}
		a.n++
		if _, ok := r["bytes_on_disk"]; ok {
			a.sized = true
			a.bytes += r.Float("bytes_on_disk")
		}
		a.reasons = append(a.reasons, r.Str("reason"))
	}
	var out []row
	for _, k := range order {
		a := byTable[k]
		res := row{"database": a.db, "table": a.tb, "detached_count": a.n, "reasons": a.reasons}
		if a.sized {
			res["total_size"] = formatReadableSize(a.bytes)
		}
		res["_order"] = a.bytes
		if !a.sized {
			res["_order"] = float64(a.n)
		}
		out = append(out, res)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["_order"].(float64) > out[j]["_order"].(float64) })
	for _, r := range out {
		delete(r, "_order")
	}
	return out, nil
}

// crash_log_entries: the latest 20 crashes. In cloud, a missing file is
// ambiguous (the fan-out fails when any replica never crashed), so
// system.crash_log_presence decides: no replica has the table → not
// applicable; some do → their crashes could not be read, which is surfaced.
func checkCrashLog(b *bundle.Bundle, mode string) ([]row, error) {
	rows, err := b.Rows("system.crash_log")
	var nc *bundle.NotCollected
	if errors.As(err, &nc) && mode == "cloud" {
		pres, perr := b.Rows("system.crash_log_presence")
		if perr != nil {
			return nil, err
		}
		var with []string
		for _, p := range pres {
			if p.Int("has_crash_log") > 0 {
				with = append(with, p.Str("hostname"))
			}
		}
		if len(with) == 0 {
			return nil, &bundle.NotCollected{Collector: "system.crash_log", State: "not collected", Reason: "no replica has system.crash_log (none has crashed)"}
		}
		return nil, fmt.Errorf("system.crash_log exists on %d replica(s) (%s) but could not be read: %v", len(with), strings.Join(with, ", "), err)
	}
	if err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Str("event_time") > rows[j].Str("event_time") })
	var out []row
	for i, r := range rows {
		if i == 20 {
			break
		}
		out = append(out, row{"event_time": r.Str("event_time"), "signal": r.Str("signal"), "thread_id": r.Str("thread_id"),
			"query_id": r.Str("query_id"), "version": r.Str("version")})
	}
	return out, nil
}

// replication_queue_errors: entries with an exception, newest 50.
func checkReplicationQueueErrors(b *bundle.Bundle, mode string) ([]row, error) {
	rows, err := b.Rows("system.replication_queue")
	if err != nil {
		return nil, err
	}
	var hits []bundle.Row
	for _, r := range rows {
		if r.Str("last_exception") != "" {
			hits = append(hits, r)
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Str("create_time") > hits[j].Str("create_time") })
	var out []row
	for i, r := range hits {
		if i == 50 {
			break
		}
		out = append(out, row{"database": r.Str("database"), "table": r.Str("table"), "replica_name": r.Str("replica_name"),
			"type": r.Str("type"), "create_time": r.Str("create_time"), "num_tries": r.Int("num_tries"), "last_exception": r.Str("last_exception")})
	}
	return out, nil
}

// mutation_running_too_long: unfinished mutations older than 3 hours (by
// dateDiff('hour'), boundaries crossed), not being killed (24.1+).
func checkMutationsTooLong(b *bundle.Bundle, mode string) ([]row, error) {
	rows, err := b.Rows("system.mutations")
	if err != nil {
		return nil, err
	}
	now, loc, err := collectedAt(b) // a live snapshot: -to does not age it
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []row
	for _, r := range rows {
		if r.Int("parts_to_do") <= 0 || r.Int("is_killed") == 1 {
			continue
		}
		k := r.Str("database") + "\x00" + r.Str("table") + "\x00" + r.Str("mutation_id")
		if seen[k] { // cloud: every replica lists the same mutation
			continue
		}
		created, ok := parseTime(r.Str("create_time"), loc)
		if !ok {
			continue
		}
		h := dateDiffHours(created, now)
		if h <= 3 {
			continue
		}
		seen[k] = true
		out = append(out, row{"database": r.Str("database"), "table": r.Str("table"), "mutation_id": r.Str("mutation_id"),
			"command": r.Str("command"), "create_time_str": r.Str("create_time"), "hours_running": h, "parts_to_do": r.Int("parts_to_do")})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["hours_running"].(int64) > out[j]["hours_running"].(int64) })
	return out, nil
}

// replica_readonly: read-only replicated tables, with active/total replicas
// from the Keeper-backed file when it was collected ("?" otherwise — it is
// skipped when Keeper is down, which is when replicas go read-only).
func checkReplicaReadonly(b *bundle.Bundle, mode string) ([]row, error) {
	rows, err := b.Rows("system.replicas")
	if err != nil {
		return nil, err
	}
	counts := map[string][2]string{}
	if pt, err := b.Rows("system.replicas_problem_tables"); err == nil {
		for _, p := range pt {
			counts[p.Str("hostname")+"\x00"+p.Str("database")+"\x00"+p.Str("table")] = [2]string{p.Str("active_replicas"), p.Str("total_replicas")}
		}
	}
	var out []row
	for _, r := range rows {
		if r.Int("is_readonly") != 1 {
			continue
		}
		c, ok := counts[r.Str("hostname")+"\x00"+r.Str("database")+"\x00"+r.Str("table")]
		if !ok {
			c = [2]string{"?", "?"}
		}
		res := row{"database": r.Str("database"), "table": r.Str("table"), "is_session_expired": r.Int("is_session_expired"),
			"absolute_delay": r.Int("absolute_delay"), "active_replicas": c[0], "total_replicas": c[1]}
		if h := r.Str("hostname"); h != "" {
			res["hostname"] = h
		}
		out = append(out, res)
	}
	return out, nil
}
