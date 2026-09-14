#!/usr/bin/env python3
"""inspect_bundle.py — deterministic first pass over a clickhouse-diagnostic bundle.

Usage:
    python3 inspect_bundle.py <clickhouse_backup_*.tar.gz | extracted dir> [--json] [--extract-to DIR]

Standard library only. Reads the bundle from disk, never writes into it, never
talks to the network. Prints a coverage statement, an inventory and the health
checks that can be computed without SQL (see references/health-checks.md for
the full rule set the assistant applies on top of this).

Exit code 0 even when findings are present; exit 2 only when the input cannot
be read (bad path, not a bundle, unsafe archive member).
"""
from __future__ import annotations

import argparse
import glob
import json
import os
import re
import sys
import tarfile
import tempfile
from collections import Counter
from datetime import datetime

GIB = 1024 ** 3

ERROR_NAMES = {
    6: "CANNOT_PARSE_TEXT", 10: "NOT_FOUND_COLUMN_IN_BLOCK", 27: "CANNOT_PARSE_INPUT_ASSERTION_FAILED",
    32: "ATTEMPT_TO_READ_AFTER_EOF", 33: "CANNOT_READ_ALL_DATA", 36: "BAD_ARGUMENTS", 40: "CHECKSUM_DOESNT_MATCH",
    43: "ILLEGAL_TYPE_OF_ARGUMENT", 44: "ILLEGAL_COLUMN", 46: "UNKNOWN_FUNCTION", 47: "UNKNOWN_IDENTIFIER",
    48: "NOT_IMPLEMENTED", 49: "LOGICAL_ERROR", 53: "TYPE_MISMATCH", 57: "TABLE_ALREADY_EXISTS", 60: "UNKNOWN_TABLE",
    62: "SYNTAX_ERROR", 70: "CANNOT_CONVERT_TYPE", 76: "CANNOT_OPEN_FILE", 80: "INCORRECT_QUERY", 81: "UNKNOWN_DATABASE",
    107: "FILE_DOESNT_EXIST", 115: "UNKNOWN_SETTING", 117: "INCORRECT_DATA", 128: "TOO_LARGE_ARRAY_SIZE",
    139: "NO_ELEMENTS_IN_CONFIG", 156: "DICTIONARIES_WAS_NOT_LOADED", 158: "TOO_MANY_ROWS", 159: "TIMEOUT_EXCEEDED",
    160: "TOO_SLOW", 164: "READONLY", 173: "CANNOT_ALLOCATE_MEMORY", 184: "ILLEGAL_AGGREGATION", 192: "UNKNOWN_USER",
    193: "WRONG_PASSWORD", 195: "IP_ADDRESS_NOT_ALLOWED", 198: "DNS_ERROR", 201: "QUOTA_EXCEEDED",
    202: "TOO_MANY_SIMULTANEOUS_QUERIES", 203: "NO_FREE_CONNECTION", 204: "CANNOT_FSYNC", 209: "SOCKET_TIMEOUT",
    210: "NETWORK_ERROR", 224: "REPLICA_IS_ALREADY_ACTIVE", 225: "NO_ZOOKEEPER", 226: "NO_FILE_IN_DATA_PART",
    232: "NO_SUCH_DATA_PART", 233: "BAD_DATA_PART_NAME", 235: "DUPLICATE_DATA_PART", 236: "ABORTED",
    241: "MEMORY_LIMIT_EXCEEDED", 242: "TABLE_IS_READ_ONLY", 243: "NOT_ENOUGH_SPACE", 244: "UNEXPECTED_ZOOKEEPER_ERROR",
    246: "CORRUPTED_DATA", 252: "TOO_MANY_PARTS", 253: "REPLICA_ALREADY_EXISTS", 277: "INDEX_NOT_USED",
    279: "ALL_CONNECTION_TRIES_FAILED", 285: "TOO_FEW_LIVE_REPLICAS", 286: "UNSATISFIED_QUORUM_FOR_PREVIOUS_WRITE",
    290: "LIMIT_EXCEEDED", 306: "TOO_DEEP_RECURSION", 307: "TOO_MANY_BYTES", 308: "UNEXPECTED_NODE_IN_ZOOKEEPER",
    318: "INVALID_CONFIG_PARAMETER", 319: "UNKNOWN_STATUS_OF_INSERT", 341: "UNFINISHED", 344: "SUPPORT_IS_DISABLED",
    349: "CANNOT_INSERT_NULL_IN_ORDINARY_COLUMN", 359: "TABLE_SIZE_EXCEEDS_MAX_DROP_SIZE_LIMIT",
    373: "SESSION_IS_LOCKED", 384: "PART_IS_TEMPORARILY_LOCKED", 389: "INSERT_WAS_DEDUPLICATED",
    394: "QUERY_WAS_CANCELLED", 396: "TOO_MANY_ROWS_OR_BYTES", 439: "CANNOT_SCHEDULE_TASK", 452: "SETTING_CONSTRAINT_VIOLATION",
    460: "CANNOT_CREATE_TIMER", 472: "READONLY_SETTING", 497: "ACCESS_DENIED", 499: "S3_ERROR",
    507: "UNABLE_TO_SKIP_UNUSED_SHARDS", 516: "AUTHENTICATION_FAILED", 517: "CANNOT_ASSIGN_ALTER",
    519: "NO_REMOTE_SHARD_AVAILABLE", 574: "DISTRIBUTED_TOO_MANY_PENDING_BYTES", 692: "TOO_MANY_MUTATIONS",
    741: "TABLE_UUID_MISMATCH", 999: "KEEPER_EXCEPTION", 1000: "POCO_EXCEPTION", 1001: "STD_EXCEPTION",
    1002: "UNKNOWN_EXCEPTION", 395: "FUNCTION_THROW_IF_VALUE_IS_NON_ZERO", 84: "DIRECTORY_ALREADY_EXISTS",
    131: "TOO_LARGE_STRING_SIZE", 142: "NOT_FOUND_NODE", 174: "CYCLIC_ALIASES", 234: "NO_REPLICA_HAS_PART", 473: "DEADLOCK_AVOIDED",
    478: "UNKNOWN_POLICY",
}

SYSTEM_DBS = {"system", "INFORMATION_SCHEMA", "information_schema"}
TRUNC_HEADER = "### support-diagnostic: TRUNCATED"


# ----------------------------------------------------------------------------- helpers

for _code, _name in {
    57: "TABLE_ALREADY_EXISTS", 84: "DIRECTORY_ALREADY_EXISTS", 86: "RECEIVED_ERROR_FROM_REMOTE_IO_SERVER",
    107: "FILE_DOESNT_EXIST", 159: "TIMEOUT_EXCEEDED", 221: "NO_SUCH_INTERSERVER_IO_ENDPOINT",
    319: "UNKNOWN_STATUS_OF_INSERT", 499: "S3_ERROR", 504: "FILE_ALREADY_EXISTS",
    571: "DATABASE_REPLICATION_FAILED", 735: "QUERY_WAS_CANCELLED_BY_CLIENT",
}.items():
    ERROR_NAMES.setdefault(_code, _name)

ZK_HW_COL = "sum(ProfileEvent_ZooKeeperHardwareExceptions)"
ZK_TX_COL = "sum(ProfileEvent_ZooKeeperTransactions)"


def hour_key(v):
    d = parse_dt(v)
    return d.strftime("%Y-%m-%d %H:00") if d else None


def die(msg: str) -> None:
    print(f"error: {msg}", file=sys.stderr)
    sys.exit(2)


def num(v):
    """Quoted UInt64/Int64 arrive as strings; convert without losing precision."""
    if isinstance(v, bool):
        return int(v)
    if isinstance(v, (int, float)):
        return v
    if isinstance(v, str):
        s = v.strip()
        if re.fullmatch(r"-?\d+", s):
            return int(s)
        try:
            return float(s)
        except ValueError:
            return None
    return None


def human(n) -> str:
    n = num(n)
    if n is None:
        return "?"
    for unit in ("B", "KiB", "MiB", "GiB", "TiB", "PiB"):
        if abs(n) < 1024 or unit == "PiB":
            return f"{n:.2f} {unit}" if unit != "B" else f"{int(n)} B"
        n /= 1024
    return str(n)


_DT_RE = re.compile(
    r"^(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}:\d{2})(?:\.\d{1,9})?(?:Z|[+-]\d{2}:?\d{2})?$"
)


def parse_dt(s):
    """Parse the timestamp shapes a bundle can contain into a naive datetime.

    Accepts ClickHouse DateTime / DateTime64 text (``2026-08-25 12:00:38``,
    ``2026-08-25 12:00:38.123456``) and RFC 3339 (``2026-08-25T12:00:38Z``,
    ``2026-08-25T12:00:38.5+02:00``). Fractional seconds and the zone suffix are
    dropped: the script only ever compares values from the same file, which
    share one zone, so spans and windows stay correct.
    """
    if not s or not isinstance(s, str):
        return None
    m = _DT_RE.match(s.strip())
    if not m:
        return None
    try:
        return datetime.strptime(f"{m.group(1)} {m.group(2)}", "%Y-%m-%d %H:%M:%S")
    except ValueError:
        return None


def safe_extract(archive: str, dest: str) -> None:
    with tarfile.open(archive, "r:*") as tar:
        for m in tar.getmembers():
            name = m.name
            if name.startswith("/") or ".." in name.split("/") or m.issym() or m.islnk():
                die(f"refusing unsafe archive member: {name}")
            if not (m.isfile() or m.isdir()):  # devices, FIFOs, anything non-regular
                die(f"refusing non-regular archive member: {name} (type {m.type!r})")
        try:
            tar.extractall(dest, filter="data")  # Python ≥ 3.12
        except TypeError:
            tar.extractall(dest)


def find_run_dir(path: str) -> str:
    if glob.glob(os.path.join(path, "system.version_*.*")):
        return path
    kids = sorted(glob.glob(os.path.join(path, "clickhouse_backup_*")))
    kids = [k for k in kids if os.path.isdir(k)]
    if len(kids) == 1:
        return kids[0]
    if kids:
        return kids[-1]
    die(f"{path} does not look like a clickhouse-diagnostic bundle (no system.version_* file)")
    return path


def first(pattern: str, base: str):
    hits = sorted(glob.glob(os.path.join(base, pattern)))
    return hits[0] if hits else None


def read_jsonl(path):
    rows = []
    if not path or not os.path.exists(path):
        return rows
    with open(path, encoding="utf-8", errors="replace") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                rows.append(json.loads(line))
            except json.JSONDecodeError:
                continue
    return rows


def line_count(path: str) -> int:
    n = 0
    with open(path, "rb") as fh:
        for _ in fh:
            n += 1
    return n


# ----------------------------------------------------------------------------- analysis

def inventory(base: str):
    files = []
    for root, _dirs, names in os.walk(base):
        for n in sorted(names):
            p = os.path.join(root, n)
            rel = os.path.relpath(p, base)
            size = os.path.getsize(p)
            entry = {"file": rel, "bytes": size}
            if n.endswith((".jsonl", ".tsv")):
                entry["rows"] = max(0, line_count(p) - (2 if n.endswith(".tsv") else 0))
            if rel.startswith("logs" + os.sep) and size > 0:
                with open(p, "rb") as fh:
                    head = fh.readline(200).decode("utf-8", "replace")
                if head.startswith(TRUNC_HEADER):
                    entry["truncated"] = head.strip()
            files.append(entry)
    return files


def detect_mode(base: str, files) -> str:
    """gov: no dashboard/crash_log/stack_trace, alerts_summary.json present, hashed names.
    cloud: clusters point at *.clickhouse.cloud or part_log carries several hostnames."""
    names = {f["file"] for f in files}
    has = lambda prefix: any(n.startswith(prefix) for n in names)
    if not has("dashboard.html") and not has("system.crash_log_") and not has("system.stack_trace_") and "alerts_summary.json" in names:
        return "gov"
    clusters = read_jsonl(first("system.clusters_*.jsonl", base))
    if any("clickhouse.cloud" in str(r.get("host_name", "")) for r in clusters):
        return "cloud"
    pl = read_jsonl(first("system.part_log_*_days_*.jsonl", base))
    hosts = {r.get("hostname") for r in pl if r.get("hostname")}
    if len(hosts) > 1:
        return "cloud"
    return "onprem"


def load_dashboard_data(base: str):
    p = os.path.join(base, "dashboard.html")
    if not os.path.exists(p):
        return None
    with open(p, encoding="utf-8", errors="replace") as fh:
        html = fh.read()
    m = re.search(r"const DATA = (\{.*?\});\s*\n", html, re.S)
    if not m:
        return None
    try:
        return json.loads(m.group(1))
    except json.JSONDecodeError:
        return None


def analyse(base: str):
    out = {"bundle": os.path.basename(os.path.normpath(base)), "findings": [], "notes": []}
    findings = out["findings"]

    def add(sev, area, msg, evidence, check=""):
        findings.append({"severity": sev, "area": area, "message": msg, "evidence": evidence, "check": check})

    files = inventory(base)
    out["files"] = files
    out["mode"] = detect_mode(base, files)

    m = re.search(r"clickhouse_backup_(\d{8}_\d{6})", out["bundle"])
    run_ts = datetime.strptime(m.group(1), "%Y%m%d_%H%M%S") if m else None
    out["collected_at"] = run_ts.isoformat(sep=" ") if run_ts else None

    ver_rows = read_jsonl(first("system.version_*.jsonl", base))
    out["version"] = ver_rows[0].get("version") if ver_rows else None

    empty = [f["file"] for f in files if f.get("rows") == 0]
    out["empty_files"] = empty
    trunc = [f for f in files if f.get("truncated")]
    for f in trunc:
        add("info", "logs", "log file is tail-truncated; first surviving line is not the start", f["truncated"], "HC-0")

    # ---- coverage from query_log_details
    ql = read_jsonl(first("system.query_log_details_7_days_*.jsonl", base))
    if ql:
        times = [parse_dt(r.get("time")) for r in ql]
        times = [t for t in times if t]
        out["query_log_window"] = {"from": min(times).isoformat(sep=" "), "to": max(times).isoformat(sep=" "),
                                   "hour_buckets": len(set(times))}
    tl = read_jsonl(first("system.text_log_2*.jsonl", base))  # not the histogram / keeper-marker files
    if tl:
        ts = [parse_dt(r.get("event_time")) for r in tl]
        ts = [t for t in ts if t]
        if ts:
            out["text_log_window"] = {"rows": len(tl), "from": min(ts).isoformat(sep=" "), "to": max(ts).isoformat(sep=" ")}
            if len(tl) >= 2000:
                add("info", "logs", "system.text_log slice hit the 2000-row cap; it covers only "
                    f"{min(ts)} → {max(ts)} — do not conclude absence of errors for the full day", f"{len(tl)} rows", "HC-11.4")

    # ---- alerts
    alerts = None
    summ = os.path.join(base, "alerts_summary.json")
    if os.path.exists(summ):
        with open(summ, encoding="utf-8") as fh:
            s = json.load(fh)
        alerts = [{"name": r["name"], "severity": r.get("severity"), "state": r.get("state"),
                   "instances": r.get("instance_count", 0)} for r in s.get("rules", [])]
    else:
        data = load_dashboard_data(base)
        if data and isinstance(data.get("alerts"), list):
            alerts = []
            for a in data["alerts"]:
                rows = a.get("rows") or []
                state = "error" if a.get("error") else ("skipped" if a.get("skipped") else ("fired" if rows else "clean"))
                alerts.append({"name": a.get("name"), "severity": a.get("severity"), "state": state, "instances": len(rows)})
    out["alerts"] = alerts
    for a in alerts or []:
        if a["state"] == "fired":
            add(a["severity"] or "warning", "alerts", f"alert `{a['name']}` fired ({a['instances']} instance(s))",
                "dashboard.html DATA.alerts / alerts_summary.json", "HC-1")
        elif a["state"] == "error":
            add("info", "alerts", f"alert `{a['name']}` could not run (not a finding — rule error/grant/version)", "", "HC-0")

    # ---- crash log
    crash = read_jsonl(first("system.crash_log_*.jsonl", base))
    if crash:
        add("critical", "availability", f"{len(crash)} crash_log row(s) — server received a fatal signal",
            "; ".join(f"{r.get('event_time')} signal {r.get('signal')} v{r.get('version')}" for r in crash[:3]), "HC-1.1")

    # ---- replicas / replication queue
    reps = read_jsonl(first("system.replicas_*.jsonl", base))
    ro = [r for r in reps if num(r.get("is_readonly")) == 1]
    if ro:
        add("critical", "replication", f"{len(ro)} replicated table(s) read-only",
            "; ".join(f"{r.get('database')}.{r.get('table')} session_expired={r.get('is_session_expired')}" for r in ro[:5]), "HC-1.4")
    delayed = [r for r in reps if (num(r.get("absolute_delay")) or 0) > 60]
    if delayed:
        worst = max(delayed, key=lambda r: num(r.get("absolute_delay")) or 0)
        add("warning", "replication", f"{len(delayed)} table(s) with absolute_delay > 60 s",
            f"max {worst.get('absolute_delay')} s on {worst.get('database')}.{worst.get('table')} queue_size={worst.get('queue_size')}", "HC-3.1")
    rq = read_jsonl(first("system.replication_queue_*.jsonl", base))
    if rq:
        by_type = Counter(r.get("type") for r in rq)
        with_exc = sum(1 for r in rq if r.get("last_exception"))
        big = {t: c for t, c in by_type.items() if c > 60}
        if with_exc or big:
            add("warning", "replication", f"replication queue: {len(rq)} entries, {with_exc} with last_exception",
                ", ".join(f"{t}={c}" for t, c in by_type.most_common(5)), "HC-3.4")

    # ---- disks
    disks = read_jsonl(first("system.disks_*.jsonl", base))
    for d in disks:
        pct = num(d.get("free_pct"))
        if pct is None:
            continue
        if pct < 5:
            add("critical", "disk", f"disk `{d.get('name')}` only {pct}% free", f"free {d.get('free_space')} of {d.get('total_space')}", "HC-4.1")
        elif pct < 15:
            add("warning", "disk", f"disk `{d.get('name')}` {pct}% free (< 15%)", f"free {d.get('free_space')} of {d.get('total_space')}", "HC-4.1")

    # ---- parts
    parts = read_jsonl(first("system.parts_*.jsonl", base))
    active = [p for p in parts if num(p.get("active")) == 1]
    per_part = Counter()
    l0 = Counter()
    rows_sum = Counter()
    bytes_by_db = Counter()
    for p in active:
        key = (p.get("database"), p.get("table"), p.get("partition_id"))
        per_part[key] += 1
        if num(p.get("level")) == 0:
            l0[key] += 1
        rows_sum[key] += num(p.get("rows")) or 0
        bytes_by_db[p.get("database")] += num(p.get("bytes_on_disk")) or 0
    out["active_parts"] = len(active)
    top = [(k, c) for k, c in per_part.most_common(10)]
    out["top_partitions"] = [{"database": k[0], "table": k[1], "partition_id": k[2], "parts": c,
                              "l0_parts": l0[k], "avg_rows": int(rows_sum[k] / c) if c else 0} for k, c in top]
    for k, c in top:
        if k[0] in SYSTEM_DBS:
            continue
        if c > 300:
            sev = "critical" if c >= 1000 else "warning"
            add(sev, "parts", f"{k[0]}.{k[1]} partition '{k[2]}' has {c} active parts (alert threshold 300)",
                f"level-0 parts {l0[k]}, avg rows/part {int(rows_sum[k] / c)}", "HC-2.1")
        elif l0[k] > 100:
            add("warning", "parts", f"{k[0]}.{k[1]} partition '{k[2]}' has {l0[k]} unmerged level-0 parts",
                f"{c} active parts total", "HC-2.2")
        if c >= 100 and rows_sum[k] / c < 10000:
            add("warning", "parts", f"{k[0]}.{k[1]} partition '{k[2]}': tiny parts (avg {int(rows_sum[k] / c)} rows over {c} parts) — small inserts",
                "system.parts rows/active part", "HC-2.3")
    huge = [p for p in active if (num(p.get("bytes_on_disk")) or 0) > 150 * GIB]
    if huge:
        add("warning", "parts", f"{len(huge)} active part(s) above 150 GiB (will not be merged further)",
            "; ".join(f"{p.get('database')}.{p.get('table')} {p.get('name')} {human(p.get('bytes_on_disk'))}" for p in huge[:3]), "HC-2.9")
    if bytes_by_db:
        total = sum(bytes_by_db.values())
        ranked = bytes_by_db.most_common(3)
        out["bytes_by_database_top3"] = [{"database": d, "bytes": b, "human": human(b)} for d, b in ranked]
        sysb = bytes_by_db.get("system", 0)
        if total and sysb / total > 0.2 and sysb > 5 * GIB:
            add("warning", "disk", f"`system` database holds {human(sysb)} ({sysb * 100 // total}% of active parts) — log tables without TTL?",
                "system.parts by database", "HC-4.4")

    # ---- detached parts
    det = read_jsonl(first("system.detached_parts_*.jsonl", base))
    if det:
        reasons = Counter(r.get("reason") for r in det)
        broken = sum(c for r, c in reasons.items() if r and "broken" in str(r))
        add("warning" if broken else "info", "parts", f"{len(det)} detached part(s)",
            ", ".join(f"{r or '(empty)'}={c}" for r, c in reasons.most_common()), "HC-2.11")

    # ---- merges / mutations
    merges = read_jsonl(first("system.merges_*.jsonl", base))
    for mg in merges:
        el = num(mg.get("elapsed")) or 0
        if el > 3600 and (num(mg.get("progress")) or 0) < 0.5:
            add("warning", "merges", f"merge on {mg.get('database')}.{mg.get('table')} running {int(el // 60)} min at {round((num(mg.get('progress')) or 0) * 100)}%",
                f"num_parts={mg.get('num_parts')} memory={human(mg.get('memory_usage'))} is_mutation={mg.get('is_mutation')}", "HC-2.5")
    muts = read_jsonl(first("system.mutations_*.jsonl", base))
    if muts:
        per_table = Counter((r.get("database"), r.get("table")) for r in muts)
        for (db, tb), c in per_table.most_common(5):
            if c > 100:
                add("critical" if c >= 900 else "warning", "mutations", f"{db}.{tb} has {c} pending mutations (throws at number_of_mutations_to_throw, default 1000)",
                    "system.mutations", "HC-8.2")
        if run_ts:
            for r in muts:
                ct = parse_dt(r.get("create_time"))
                todo = num(r.get("parts_to_do")) or 0
                if ct and todo > 0 and (run_ts - ct).total_seconds() > 3 * 3600:
                    add("warning", "mutations", f"mutation {r.get('mutation_id')} on {r.get('database')}.{r.get('table')} pending {int((run_ts - ct).total_seconds() // 3600)} h, parts_to_do={todo}",
                        f"command: {str(r.get('command'))[:120]}", "HC-8.1")

    # ---- errors
    errs = read_jsonl(first("system.errors_*.jsonl", base))
    errs_sorted = sorted(errs, key=lambda r: -(num(r.get("value")) or 0))
    out["top_errors"] = [{"code": num(r.get("code")), "name": r.get("name"), "count": num(r.get("value")),
                          "last": r.get("last_error_time")} for r in errs_sorted[:10]]
    for r in errs_sorted[:20]:
        code = num(r.get("code"))
        if code in (241, 173, 242, 999, 243, 252, 202, 49, 40, 226, 246):
            add("warning" if code not in (173, 243) else "critical", "errors",
                f"{r.get('name')} ({code}) counted {r.get('value')} times since server start",
                f"last at {r.get('last_error_time')}", "HC-1.5/HC-5.3")

    # ---- query_log exceptions (dedupe the LEFT ARRAY JOIN duplication) + hourly timeline
    timeline = {}  # hour -> signals

    def tl(h):
        return timeline.setdefault(h, {"queries": 0, "exceptions": 0, "codes": Counter(), "new_parts": 0,
                                       "merges": 0, "failed_bg": Counter(), "zk_hw": 0, "zk_tx": None})

    if ql:
        seen = set()
        exc = Counter()
        total = 0
        for r in ql:
            key = (r.get("time"), r.get("query_kind"), r.get("type"), r.get("user"), r.get("interface"),
                   r.get("normalized_query_hash"), r.get("exception_code"))
            if key in seen:
                continue
            seen.add(key)
            c = num(r.get("count")) or 0
            if r.get("type") == "QueryStart":
                continue
            total += c
            code = num(r.get("exception_code")) or 0
            h = hour_key(r.get("time"))
            if h:
                t = tl(h)
                t["queries"] += c
                if code:
                    t["exceptions"] += c
                    t["codes"][code] += c
            if code:
                exc[code] += c
        out["query_log"] = {"queries": total, "exceptions": sum(exc.values()),
                            "top_exception_codes": [{"code": c, "name": ERROR_NAMES.get(c, "?"), "count": n} for c, n in exc.most_common(8)]}
        if total and sum(exc.values()) / total > 0.05:
            add("warning", "queries", f"{sum(exc.values())} of {total} queries failed ({sum(exc.values()) * 100 // total}%)",
                ", ".join(f"{ERROR_NAMES.get(c, c)}={n}" for c, n in exc.most_common(5)), "HC-6.1")
        if exc.get(202, 0) > 10:
            add("warning", "queries", f"TOO_MANY_SIMULTANEOUS_QUERIES (202) × {exc[202]}", "query_log_details", "HC-6.2")
        if exc.get(252, 0):
            add("warning", "inserts", f"TOO_MANY_PARTS (252) × {exc[252]} — inserts rejected", "query_log_details", "HC-2.1")
        if exc.get(999, 0) or exc.get(319, 0) or exc.get(571, 0):
            add("warning", "keeper", f"Keeper-dependent failures in query_log: 999 × {exc.get(999, 0)}, 319 UNKNOWN_STATUS_OF_INSERT × {exc.get(319, 0)}, 571 DATABASE_REPLICATION_FAILED × {exc.get(571, 0)}",
                "see the incident timeline for the hours", "HC-3.8/P-57")
        if exc.get(107, 0) > 10:
            add("warning", "storage", f"FILE_DOESNT_EXIST (107) × {exc[107]} — parts whose files/blobs cannot be read (stale metadata on object storage after an incident, or missing files)",
                "query_log_details by table; check system.disks.type and blob_storage_log", "P-58")
        if exc.get(57, 0) > 50:
            add("info", "ddl", f"TABLE_ALREADY_EXISTS (57) × {exc[57]} — if the messages say 'UUID collision' on .tmp.inner_id tables this is Replicated-database DDL replay after a Keeper session loss",
                "distributed_ddl_queue, query_log Create rows with empty user", "P-57")

    # ---- part_log: merges stalled / failing background operations per hour
    pl_rows = read_jsonl(first("system.part_log_*_days_*.jsonl", base))
    if pl_rows:
        for r in pl_rows:
            h = hour_key(r.get("time"))
            if not h:
                continue
            t = tl(h)
            c = num(r.get("count")) or 0
            et = r.get("event_type")
            err = num(r.get("error")) or 0
            if et == "NewPart":
                t["new_parts"] += c
            elif et == "MergeParts" and not err:
                t["merges"] += c
            if err:
                t["failed_bg"][(et, err)] += c
        stalled = sorted(h for h, t in timeline.items() if t["new_parts"] > 100 and t["merges"] == 0)
        if stalled:
            add("critical" if len(stalled) >= 3 else "warning", "merges",
                f"{len(stalled)} hour(s) with inserts (NewPart > 100) and zero completed merges — merges were not running",
                "hours: " + ", ".join(stalled[:8]) + (" …" if len(stalled) > 8 else ""), "HC-2.12/P-57")
        bg = Counter()
        for t in timeline.values():
            bg.update(t["failed_bg"])
        top_bg = [(k, v) for k, v in bg.most_common(5) if v > 50]
        if top_bg:
            add("warning", "merges", "background operations failing repeatedly (part_log)",
                "; ".join(f"{et} code {err} {ERROR_NAMES.get(err, '')} × {v}" for (et, err), v in top_bg), "HC-2.5")

    # ---- metric_log_coordination: Keeper hardware exceptions per hour, read-only replicas, S3 errors
    mlc = read_jsonl(first("system.metric_log_coordination_3_days_*.jsonl", base))
    if mlc:
        cols = set(mlc[0].keys())
        for r in mlc:
            h = hour_key(r.get("time"))
            if not h:
                continue
            t = tl(h)
            t["zk_hw"] = num(r.get(ZK_HW_COL)) or 0
            t["zk_tx"] = num(r.get(ZK_TX_COL))
        ro_col = "max(CurrentMetric_ReadonlyReplica)"
        if ro_col in cols:
            ro_hours = sorted(hour_key(r.get("time")) for r in mlc if (num(r.get(ro_col)) or 0) > 0)
            if ro_hours:
                add("warning", "replication", f"read-only replicas present in {len(ro_hours)} hour(s) (metric_log ReadonlyReplica max > 0)",
                    f"first {ro_hours[0]}, last {ro_hours[-1]}", "HC-1.4")
        s3_cols = [c for c in cols if c.startswith("sum(ProfileEvent_") and ("S3" in c or "ObjectStorage" in c or "AzureBlobStorage" in c) and c.endswith("Errors)")]
        s3_err = {c: sum(num(r.get(c)) or 0 for r in mlc) for c in s3_cols}
        s3_err = {c: v for c, v in s3_err.items() if v}
        if s3_err:
            add("warning", "storage", "object-storage request errors in metric_log",
                ", ".join(f"{c[len('sum(ProfileEvent_'):-1]}={v}" for c, v in sorted(s3_err.items(), key=lambda kv: -kv[1])[:5]), "HC-4.10")

    if not mlc:
        # older bundles: fall back to the fixed-column metric_log aggregate for the Keeper signal
        for r in read_jsonl(first("system.metric_log_7_days_*.jsonl", base)):
            h = hour_key(r.get("time"))
            if h:
                tl(h)["zk_hw"] = num(r.get("zk_hw_exceptions")) or 0
                tl(h)["zk_tx"] = num(r.get("zk_transactions"))

    # ---- Keeper health test (HC-3.8): hardware exceptions AND transactions vs the 7-day median, per hour
    tx_vals = sorted(t["zk_tx"] for t in timeline.values() if t["zk_tx"] is not None)
    tx_med = tx_vals[len(tx_vals) // 2] if tx_vals else 0
    tx_hours = [h for h, t in timeline.items() if t["zk_tx"] is not None]
    edge_hours = {min(tx_hours), max(tx_hours)} if tx_hours else set()  # partial buckets at both ends of the window
    for h, t in timeline.items():
        t["tx_pct"] = round(100 * t["zk_tx"] / tx_med) if (t["zk_tx"] is not None and tx_med) else None
        if t["zk_hw"] > 1000 and t["tx_pct"] is not None and t["tx_pct"] < 50:
            t["keeper"] = "UNAVAILABLE"
        elif t["zk_hw"] > 1000:
            t["keeper"] = "blip"
        elif t["tx_pct"] is not None and t["tx_pct"] < 10 and h not in edge_hours:
            t["keeper"] = "idle/disconnected"
        else:
            t["keeper"] = ""
    unavailable = sorted(h for h, t in timeline.items() if t["keeper"] == "UNAVAILABLE")
    blips = sorted(h for h, t in timeline.items() if t["keeper"] == "blip")
    if unavailable:
        peak = max(unavailable, key=lambda h: timeline[h]["zk_hw"])
        add("critical", "keeper", f"Keeper effectively unavailable in {len(unavailable)} hour(s): > 1000 hardware exceptions/h while Keeper transactions fell below 50 % of the 7-day median ({tx_med}/h); peak {timeline[peak]['zk_hw']} exceptions at {peak} with traffic at {timeline[peak]['tx_pct']} %",
            "hours: " + ", ".join(unavailable[:8]) + (" …" if len(unavailable) > 8 else "") + " — every 999/319/571/252 finding in these hours is downstream (alert keeper_health)", "HC-3.8/P-57")
    if blips:
        add("warning", "keeper", f"Keeper session lost and re-established in {len(blips)} hour(s) while traffic stayed normal (> 1000 hardware exceptions/h)",
            "hours: " + ", ".join(blips[:8]) + (" …" if len(blips) > 8 else "") + " — read text_log_keeper markers for the minute and the Keeper host (alert keeper_connection_blips)", "HC-3.8")
    tk = read_jsonl(first("system.text_log_keeper_1_day_*.jsonl", base))
    if tk:
        mk = Counter()
        for r in tk:
            mk[r.get("marker")] += num(r.get("count")) or 0
        conn = [r for r in tk if r.get("marker") == "connected"]
        ex = conn[-1].get("example") if conn else ""
        if mk.get("session_expired") or mk.get("reconnecting"):
            add("warning", "keeper", "Keeper session markers in text_log (last day): " + ", ".join(f"{k}={v}" for k, v in mk.most_common()),
                (f"last reconnect: {str(ex)[:160]}" if ex else ""), "HC-3.8")
    el = read_jsonl(first("system.error_log_7_days_*.jsonl", base))
    if el:
        by_code = Counter()
        for r in el:
            by_code[num(r.get("code"))] += num(r.get("errors")) or 0
            h = hour_key(r.get("time"))
            if h and num(r.get("code")) in (999, 242, 319, 571):
                tl(h)["codes"][num(r.get("code"))] += 0  # make the hour visible in the timeline
        top = by_code.most_common(6)
        out["error_log_top"] = [{"code": c, "name": ERROR_NAMES.get(c, "?"), "count": n} for c, n in top]

    # ---- incident hours: elevated exceptions, stalled merges, Keeper loss, failing background ops
    exc_hours = [t["exceptions"] for t in timeline.values() if t["exceptions"]]
    med = sorted(exc_hours)[len(exc_hours) // 2] if exc_hours else 0
    incident = []
    for h in sorted(timeline):
        t = timeline[h]
        flags = []
        if t["exceptions"] >= 50 and (t["exceptions"] >= 0.2 * max(t["queries"], 1) or t["exceptions"] >= 5 * max(med, 1)):
            flags.append("exceptions")
        if t["new_parts"] > 100 and t["merges"] == 0:
            flags.append("no merges")
        if t.get("keeper"):
            flags.append(f"keeper {t['keeper']}")
        if sum(t["failed_bg"].values()) > 50:
            flags.append("bg failures")
        if flags:
            incident.append({"hour": h, "queries": t["queries"], "exceptions": t["exceptions"],
                             "top_codes": ", ".join(f"{ERROR_NAMES.get(c, c)}={n}" for c, n in t["codes"].most_common(3)),
                             "new_parts": t["new_parts"], "merges": t["merges"], "failed_bg": sum(t["failed_bg"].values()),
                             "zk_hw": t["zk_hw"], "tx_pct": t.get("tx_pct"), "flags": ", ".join(flags)})
    out["incident_hours"] = incident[:48]
    if incident:
        out["notes"].append(f"incident hours detected: {incident[0]['hour']} → {incident[-1]['hour']} ({len(incident)} hour(s)) — see the timeline table")

    # ---- snapshots added for Keeper / object-storage incidents
    am = read_jsonl(first("system.asynchronous_metrics_*.jsonl", base))
    uptime = None
    if am:
        ups = [num(r.get("value")) for r in am if r.get("metric") == "Uptime"]
        ups = [u for u in ups if u is not None]
        if ups:
            uptime = min(ups)
            out["uptime_seconds"] = uptime
    mt = read_jsonl(first("system.metrics_*.jsonl", base))
    if mt:
        by_host = {}
        for r in mt:
            by_host.setdefault(r.get("hostname", ""), {})[r.get("metric")] = num(r.get("value"))
        for host, mm in by_host.items():
            who = f" on {host}" if host else ""
            if mm.get("ZooKeeperSession") == 0:
                add("critical", "keeper", f"no Keeper session right now (ZooKeeperSession = 0){who}", "system.metrics", "HC-3.12")
            if (mm.get("ReadonlyReplica") or 0) > 0:
                add("critical", "replication", f"{mm['ReadonlyReplica']} read-only replica table(s) right now{who}", "system.metrics", "HC-1.4")
        cache = {h: mm.get("MetadataFromKeeperCacheObjects") for h, mm in by_host.items() if mm.get("MetadataFromKeeperCacheObjects") is not None}
        if len(cache) > 2:
            vals = sorted(cache.values())
            median = vals[len(vals) // 2]
            low = [h for h, v in cache.items() if median > 1000 and v < median / 100]
            if low:
                add("warning", "storage", f"{len(low)} replica(s) with an almost empty Keeper metadata cache (MetadataFromKeeperCacheObjects ≪ peers, median {int(median)})",
                    ", ".join(low[:6]), "HC-3.12/P-58")
    zc = read_jsonl(first("system.zookeeper_connection_*.jsonl", base))
    for r in zc:
        who = f" on {r['hostname']}" if r.get("hostname") else ""
        if num(r.get("is_expired")):
            add("critical", "keeper", f"Keeper session expired right now{who}", f"host {r.get('host')}", "HC-3.9")
        age = num(r.get("session_uptime_elapsed_seconds"))
        if age is not None and uptime and age < uptime / 10 and uptime > 3600:
            add("warning", "keeper", f"Keeper session is {int(age // 60)} min old on a server up {int(uptime // 3600)} h{who} — the session was re-established at {r.get('connected_time')}",
                f"host {r.get('host')}", "HC-3.9/P-57")
    dbs = read_jsonl(first("system.databases_*.jsonl", base))
    if dbs:
        eng = Counter(r.get("engine") for r in dbs)
        out["databases"] = dict(eng)
        if eng.get("Replicated", 0) >= 50:
            add("info", "keeper", f"{eng['Replicated']} Replicated databases (of {len(dbs)}) — one DDLWorker and Keeper watch set each, per replica",
                "system.databases", "HC-3.13")
    ddl = read_jsonl(first("system.distributed_ddl_queue_*.jsonl", base))
    if ddl:
        ref = run_ts or datetime.now()
        stuck = [r for r in ddl if r.get("status") not in ("Finished", None) and parse_dt(r.get("query_create_time")) and (ref - parse_dt(r.get("query_create_time"))).total_seconds() > 600]
        if stuck:
            hosts = Counter(r.get("host") for r in stuck)
            add("warning", "ddl", f"{len(stuck)} DDL queue row(s) not Finished after 10 min", "hosts: " + ", ".join(f"{h}×{n}" for h, n in hosts.most_common(4)), "HC-3.11")
        codes = Counter(num(r.get("exception_code")) for r in ddl if num(r.get("exception_code")))
        if codes:
            add("info", "ddl", "DDL queue exceptions", ", ".join(f"{ERROR_NAMES.get(c, c)}={n}" for c, n in codes.most_common(5)), "HC-3.11")
    bsl = read_jsonl(first("system.blob_storage_log_7_days_*.jsonl", base))
    if bsl:
        failed = sum(num(r.get("count")) or 0 for r in bsl if num(r.get("failed")))
        if failed:
            ex = next((r.get("example_error") for r in bsl if num(r.get("failed")) and r.get("example_error")), "")
            add("warning", "storage", f"{failed} failed object-storage operation(s) in blob_storage_log", str(ex)[:200], "HC-4.9/P-58")
    zl = read_jsonl(first("system.zookeeper_log_errors_1_day_*.jsonl", base))
    if zl:
        errs = Counter()
        loss_hours = set()
        for r in zl:
            e = r.get("error")
            n_failed = num(r.get("failed_requests")) or 0
            errs[e] += n_failed
            h = hour_key(r.get("time"))
            if h and e in ("ZSESSIONEXPIRED", "ZCONNECTIONLOSS", "ZOPERATIONTIMEOUT"):
                loss_hours.add(h)
        bad = {e: n for e, n in errs.items() if e in ("ZSESSIONEXPIRED", "ZCONNECTIONLOSS", "ZOPERATIONTIMEOUT")}
        if bad:
            add("warning", "keeper", "zookeeper_log: failed Keeper requests with loss errors — " + ", ".join(f"{e}={n}" for e, n in bad.items()),
                "hours: " + ", ".join(sorted(loss_hours)[:8]), "HC-3.10")
    th = read_jsonl(first("system.text_log_histogram_1_day_*.jsonl", base))
    if th:
        per_hour = Counter()
        cls = Counter()
        for r in th:
            if r.get("level") in ("Error", "Fatal", "Critical"):
                h = hour_key(r.get("time"))
                per_hour[h] += num(r.get("count")) or 0
                cls[r.get("logger_class")] += num(r.get("count")) or 0
        if per_hour:
            peak_h, peak_n = per_hour.most_common(1)[0]
            quiet = sorted(per_hour.values())[len(per_hour) // 2]
            out["text_log_histogram"] = {"error_hours": len(per_hour), "peak_hour": peak_h, "peak_errors": peak_n, "median_errors": quiet,
                                         "top_classes": [f"{c}={n}" for c, n in cls.most_common(6)]}
            if quiet and peak_n >= 10 * quiet:
                add("warning", "logs", f"Error-level log volume peaked at {peak_n} lines in hour {peak_h} (median hour {quiet})",
                    "top classes: " + ", ".join(f"{c}={n}" for c, n in cls.most_common(4)), "HC-11.5")

    # ---- one node of a SharedMergeTree cluster?
    st_rows = read_jsonl(first("system.settings_*.jsonl", base))
    cloud_mode = any(r.get("name") == "cloud_mode" and str(r.get("value")) in ("1", "true") for r in st_rows)
    shared = False
    tb = read_jsonl(first("system.tables_*.jsonl", base))
    if tb:
        shared = any(str(r.get("engine", "")).startswith("Shared") for r in tb)
    if (cloud_mode or shared) and out["mode"] == "onprem":
        cl = read_jsonl(first("system.clusters_*.jsonl", base))
        n = len({(r.get("host_name"), r.get("port")) for r in cl if r.get("cluster") == "default"}) or "N"
        out["notes"].append(f"SharedMergeTree cluster collected in onprem mode: this bundle describes ONE replica of {n} (parts, errors, part_log, query_log, text_log are per replica) — propose -mode cloud for the cluster view")

    # ---- dictionaries
    dicts = read_jsonl(first("system.dictionaries_*.jsonl", base))
    bad = [d for d in dicts if str(d.get("status", "")).startswith("FAILED") or d.get("last_exception")]
    if bad:
        add("warning", "dictionaries", f"{len(bad)} dictionary(ies) failed to load",
            "; ".join(f"{d.get('database')}.{d.get('name')} {d.get('status')}" for d in bad[:5]), "HC-9.1")

    # ---- metric_log memory / pools
    ml = read_jsonl(first("system.metric_log_7_days_*.jsonl", base))
    if ml:
        mem = [num(r.get("avg_memory_tracking_bytes")) or 0 for r in ml]
        pool = [num(r.get("max_merge_pool_tasks")) or 0 for r in ml]
        zk = sum(num(r.get("zk_hw_exceptions")) or 0 for r in ml)
        out["metric_log"] = {"hours": len(ml), "max_avg_memory_tracking": human(max(mem)), "max_merge_pool_tasks": max(pool),
                             "hours_pool_at_max": sum(1 for p in pool if p == max(pool)) if max(pool) else 0, "zk_hw_exceptions_total": zk}
        if zk and not any(f["check"].startswith("HC-3.8") for f in findings):
            add("warning", "keeper", f"{zk} ZooKeeper/Keeper hardware exceptions over {len(ml)} hours", "metric_log.zk_hw_exceptions", "HC-3.5")

    # ---- host info
    hi_path = os.path.join(base, "host_info.json")
    if os.path.exists(hi_path):
        with open(hi_path, encoding="utf-8") as fh:
            hi = json.load(fh)
        out["host"] = {"hostname": hi.get("os", {}).get("hostname"), "distro": f"{hi.get('os', {}).get('distro', '')} {hi.get('os', {}).get('distro_version', '')}".strip(),
                       "kernel": hi.get("os", {}).get("kernel_version"), "cpus": hi.get("cpu", {}).get("logical_cpus"),
                       "load": hi.get("cpu", {}).get("load_avg_1_5_15"), "ram": human(hi.get("memory", {}).get("total_bytes")),
                       "available": human(hi.get("memory", {}).get("available_bytes")), "notes": hi.get("notes")}
        t = hi.get("clickhouse_relevant_tunables", {}) or {}
        memi = hi.get("memory", {}) or {}
        if "always" in str(t.get("transparent_hugepages", "")):
            add("warning", "host", "transparent_hugepages = always (ClickHouse warns at startup)", "host_info.json", "HC-10.1")
        if str(t.get("vm_overcommit_memory", "")).strip() == "2":
            add("warning", "host", "vm.overcommit_memory = 2 (ClickHouse warns at startup)", "host_info.json", "HC-10.2")
        if memi.get("available") and (num(memi.get("available_bytes")) or 0) and num(memi.get("available_bytes")) < 2 * GIB:
            add("warning", "host", f"available memory {human(memi.get('available_bytes'))} < 2 GiB", "host_info.json", "HC-10.3")
        cg = num(t.get("cgroup_memory_limit_bytes"))
        tot = num(memi.get("total_bytes"))
        if cg and tot and cg < tot * 0.9:
            add("info", "host", f"cgroup memory limit {human(cg)} below host RAM {human(tot)} — the cgroup is the real ceiling", "host_info.json", "HC-10.4")
        nofile = num(t.get("clickhouse_open_files_soft"))
        if nofile and nofile < 500000:
            add("warning", "host", f"open files soft limit {nofile} (< 500000)", "host_info.json", "HC-10.5")
        swap_used = (num(memi.get("swap_total_bytes")) or 0) - (num(memi.get("swap_free_bytes")) or 0)
        if swap_used > 0:
            add("info", "host", f"swap in use: {human(swap_used)}", "host_info.json", "HC-5.6")
        cpus = num(hi.get("cpu", {}).get("logical_cpus"))
        load = hi.get("cpu", {}).get("load_avg_1_5_15") or []
        if cpus and load and num(load[0]) and num(load[0]) > 2 * cpus:
            add("warning", "host", f"load average {load[0]} on {cpus} CPUs", "host_info.json", "HC-5.5")
        if not hi.get("os", {}).get("available") or not hi.get("cpu", {}).get("available"):
            add("info", "host", "host_info.json is degraded (sections unavailable) — likely collected off the server; ignore host facts", str(hi.get("notes")), "HC-0")
    elif out["mode"] != "gov":
        out["notes"].append("host_info.json absent (cloud default, or -host-info off)")

    # ---- changed settings (collected since Sept 2026; server_settings needs >= 23.4)
    for fname, key in (("system.settings_*.jsonl", "settings_changed"), ("system.server_settings_*.jsonl", "server_settings_changed")):
        rows = read_jsonl(first(fname, base))
        if rows:
            changed = [r for r in rows if num(r.get("changed")) == 1]
            out[key] = [{"name": r.get("name"), "value": r.get("value"), "default": r.get("default")} for r in changed]
            watch = {"max_server_memory_usage_to_ram_ratio", "max_memory_usage", "max_concurrent_queries", "background_pool_size",
                     "background_fetches_pool_size", "number_of_free_entries_in_pool_to_execute_mutation", "compatibility",
                     "wait_for_async_insert", "parallel_distributed_insert_select", "mark_cache_size", "uncompressed_cache_size"}
            hits = [f"{r.get('name')}={r.get('value')}" for r in changed if r.get("name") in watch]
            if hits:
                add("info", "settings", f"{len(changed)} changed {'server ' if 'server' in key else ''}settings; notable: " + ", ".join(hits[:8]), fname, "HC-10.8")

    # ---- query_analysis/ (only with --query-id / --normalized-query-hash)
    qa_dir = os.path.join(base, "query_analysis")
    if os.path.isdir(qa_dir):
        qa = {"files": {}, "empty_not_applicable": []}
        for f in sorted(os.listdir(qa_dir)):
            rows = line_count(os.path.join(qa_dir, f)) if f.endswith(".jsonl") else None
            qa["files"][f] = rows
        det = read_jsonl(first("query_details_*.jsonl", qa_dir))
        if det:
            d = det[0]
            tables = d.get("tables") or []
            qa["query_id"] = d.get("query_id")
            qa["query_kind"] = d.get("query_kind")
            qa["user"] = d.get("user")
            qa["is_initial_query"] = num(d.get("is_initial_query"))
            qa["tables"] = tables
            qa["duration_ms"] = num(d.get("query_duration_ms"))
            qa["read_rows"] = num(d.get("read_rows"))
            qa["memory"] = d.get("memory_usage_human")
            table_function = any(str(t).startswith("_table_function.") for t in tables)
            qa["shape"] = ("worker sub-query of a distributed statement" if qa["is_initial_query"] == 0 else
                           "table-function query (no MergeTree parts)" if table_function else "MergeTree read")
            if qa["is_initial_query"] == 0:
                add("info", "query-analysis", "the analysed hash is a WORKER sub-query (is_initial_query = 0): its executions are shards of one distributed statement — compare per-replica shares, not slow vs fast runs; find the parent via initial_query_id in text_log_full", f"tables={tables}", "step 3")
            if table_function:
                qa["empty_not_applicable"] += [f for f in qa["files"] if f.startswith(("text_log_parts", "tables_for_query")) and qa["files"][f] == 0]
        pe = read_jsonl(first("profile_events_*.jsonl", qa_dir))
        if pe:
            ev = {r.get("metric"): num(r.get("value")) or 0 for r in pe}
            # wall = query_duration_ms; RealTimeMicroseconds is summed across threads and overstates it
            wall = (qa.get("duration_ms") or 0) * 1000 or (ev.get("RealTimeMicroseconds") or 0)
            qa["real_time_threads_seconds"] = round((ev.get("RealTimeMicroseconds") or 0) / 1e6, 1)
            budget = {}
            for k in ("OSCPUVirtualTimeMicroseconds", "OSCPUWaitMicroseconds", "NetworkSendElapsedMicroseconds", "NetworkReceiveElapsedMicroseconds",
                      "ParquetFetchWaitTimeMicroseconds", "ReadBufferFromS3Microseconds", "S3ReadMicroseconds", "DiskReadElapsedMicroseconds",
                      "DiskWriteElapsedMicroseconds", "ZooKeeperWaitMicroseconds"):
                if ev.get(k):
                    budget[k] = {"seconds": round(ev[k] / 1e6, 1), "pct_of_wall": round(100 * ev[k] / wall, 1) if wall else None}
            qa["wall_seconds"] = round(wall / 1e6, 1) if wall else None
            qa["time_budget"] = dict(sorted(budget.items(), key=lambda kv: -kv[1]["seconds"]))
            qa["selected_parts"] = ev.get("SelectedParts")
            qa["selected_marks"] = ev.get("SelectedMarks")
            qa["network_send_bytes"] = ev.get("NetworkSendBytes")
            cpu, wait = ev.get("OSCPUVirtualTimeMicroseconds") or 0, ev.get("OSCPUWaitMicroseconds") or 0
            if cpu and wait > 0.5 * cpu:
                add("warning", "query-analysis", f"CPU wait ≈ {round(100 * wait / cpu)}% of CPU used — the execution was CPU-throttled/contended", "profile_events OSCPUWaitMicroseconds vs OSCPUVirtualTimeMicroseconds", "P-22")
            ns = ev.get("NetworkSendElapsedMicroseconds") or 0
            if wall and ns > 0.5 * wall:
                add("warning", "query-analysis", f"network send is {round(100 * ns / wall)}% of wall time — rows are being shipped to the initiator/client (distributed INSERT SELECT? consider parallel_distributed_insert_select = 2)", f"NetworkSendBytes={human(ev.get('NetworkSendBytes'))}", "P-56")
        ex = read_jsonl(first("executions_timeline_*.jsonl", qa_dir))
        if ex:
            hosts = {r.get("hostname") for r in ex}
            ts = [parse_dt(r.get("ts")) for r in ex]
            ts = [t for t in ts if t]
            span_min = (max(ts) - min(ts)).total_seconds() / 60 if ts else None
            qa["executions"] = len(ex)
            qa["hosts"] = len(hosts)
            qa["span_minutes"] = round(span_min, 1) if span_min is not None else None
            if len(ex) > 1 and len(hosts) == len(ex) and span_min is not None and span_min < 60:
                qa["runs_or_shards"] = "shards (one per host within an hour — data skew, not regression)"
            else:
                qa["runs_or_shards"] = "runs"
        for f, rows in qa["files"].items():
            if rows == 0 and f.startswith("failed_"):
                qa["empty_not_applicable"].append(f)
        out["query_analysis"] = qa

    if out["mode"] == "gov":
        out["notes"].append("gov mode: identifiers are SHA-256 hashes; text, config, host facts and logs are withheld by design")

    sev_rank = {"critical": 0, "warning": 1, "info": 2}
    findings.sort(key=lambda f: sev_rank.get(f["severity"], 3))
    return out


# ----------------------------------------------------------------------------- rendering

def render_md(o) -> str:
    L = []
    L.append(f"# Bundle inspection — {o['bundle']}")
    L.append("")
    L.append(f"- ClickHouse version: **{o.get('version') or 'unknown'}** · mode: **{o['mode']}** · collected: {o.get('collected_at') or '?'} (collector local time)")
    qw = o.get("query_log_window")
    if qw:
        L.append(f"- query_log window: {qw['from']} → {qw['to']} ({qw['hour_buckets']} hour buckets)")
    tw = o.get("text_log_window")
    if tw:
        L.append(f"- text_log slice: {tw['rows']} rows, {tw['from']} → {tw['to']}")
    if o.get("host"):
        h = o["host"]
        if h.get("cpus") is not None:
            L.append(f"- host: {h.get('hostname')} · {h.get('distro')} · kernel {h.get('kernel')} · {h.get('cpus')} CPUs, load {h.get('load')} · RAM {h.get('ram')} (available {h.get('available')})")
        else:
            L.append(f"- host: {h.get('hostname')} — host_info.json degraded ({'; '.join(h.get('notes') or []) or 'sections unavailable'})")
    if o.get("active_parts") is not None:
        L.append(f"- active parts: {o['active_parts']}" + (" · largest dbs: " + ", ".join(f"{d['database']}={d['human']}" for d in o.get("bytes_by_database_top3", [])) if o.get("bytes_by_database_top3") else ""))
    if o.get("metric_log"):
        m = o["metric_log"]
        L.append(f"- metric_log: {m['hours']} h · peak hourly avg tracked memory {m['max_avg_memory_tracking']} · merge pool max {m['max_merge_pool_tasks']} ({m['hours_pool_at_max']} h at max) · zk_hw_exceptions {m['zk_hw_exceptions_total']}")
    if o.get("query_log"):
        q = o["query_log"]
        codes = ", ".join(f"{c['name']}({c['code']})={c['count']}" for c in q["top_exception_codes"]) or "none"
        L.append(f"- query_log: {q['queries']} finished/failed queries in window · {q['exceptions']} exceptions · top codes: {codes}")
    if o.get("uptime_seconds") is not None or o.get("databases"):
        bits = []
        if o.get("uptime_seconds") is not None:
            bits.append(f"server uptime {round(o['uptime_seconds'] / 3600, 1)} h (denominator for system.errors / system.events)")
        if o.get("databases"):
            bits.append("databases by engine: " + ", ".join(f"{k}={v}" for k, v in sorted(o["databases"].items(), key=lambda kv: -kv[1])))
        L.append("- " + " · ".join(bits))
    if o.get("error_log_top"):
        L.append("- error_log (7 days, all threads incl. background): " + ", ".join(f"{e['name']}({e['code']})={e['count']}" for e in o["error_log_top"]))
    if o.get("text_log_histogram"):
        t = o["text_log_histogram"]
        L.append(f"- text_log histogram: Error-level lines in {t['error_hours']} hour(s) · peak {t['peak_errors']} at {t['peak_hour']} (median hour {t['median_errors']}) · top classes: {', '.join(t['top_classes'])}")
    if o.get("empty_files"):
        L.append(f"- empty files: {', '.join(o['empty_files'])}")
    for key, label in (("settings_changed", "query/profile settings changed"), ("server_settings_changed", "server settings changed")):
        if o.get(key) is not None:
            names = ", ".join(f"{r['name']}={r['value']}" for r in o[key][:12])
            L.append(f"- {label}: {len(o[key])}" + (f" — {names}" + (" …" if len(o[key]) > 12 else "") if o[key] else ""))
    if o.get("query_analysis"):
        qa = o["query_analysis"]
        L.append("")
        L.append("## Query analysis")
        L.append(f"- focus: `{qa.get('query_id')}` · {qa.get('query_kind')} · user {qa.get('user')} · shape: **{qa.get('shape')}** · tables {qa.get('tables')}")
        L.append(f"- wall {qa.get('wall_seconds')} s (thread-summed real time {qa.get('real_time_threads_seconds')} s) · read_rows {qa.get('read_rows')} · memory {qa.get('memory')} · SelectedParts {qa.get('selected_parts')} · SelectedMarks {qa.get('selected_marks')} · NetworkSendBytes {human(qa.get('network_send_bytes')) if qa.get('network_send_bytes') else '?'}")
        if qa.get("time_budget"):
            L.append("- time budget: " + ", ".join(f"{k.replace('Microseconds','')} {v['seconds']} s ({v['pct_of_wall']}%)" for k, v in qa["time_budget"].items()))
        if qa.get("executions"):
            L.append(f"- executions in window: {qa['executions']} on {qa['hosts']} host(s) over {qa.get('span_minutes')} min → {qa.get('runs_or_shards')}")
        if qa.get("empty_not_applicable"):
            L.append(f"- empty because not applicable to this shape: {', '.join(qa['empty_not_applicable'])}")

    for n in o.get("notes", []):
        L.append(f"- note: {n}")
    L.append("")
    if o.get("alerts") is not None:
        fired = [a for a in o["alerts"] if a["state"] == "fired"]
        errored = [a for a in o["alerts"] if a["state"] == "error"]
        skipped = [a for a in o["alerts"] if a["state"] == "skipped"]
        L.append(f"## Alerts — {len(fired)} fired, {len(errored)} could not run, {len(skipped)} not applicable, {len(o['alerts'])} total")
        for a in fired:
            L.append(f"- **{a['severity']}** `{a['name']}` — {a['instances']} instance(s)")
        L.append("")
    L.append(f"## Findings ({len(o['findings'])})")
    if not o["findings"]:
        L.append("- none from the deterministic checks — continue with health-checks.md HC-1…HC-11")
    for f in o["findings"]:
        ev = f" — {f['evidence']}" if f["evidence"] else ""
        chk = f" [{f['check']}]" if f["check"] else ""
        L.append(f"- **{f['severity']}** ({f['area']}) {f['message']}{ev}{chk}")
    L.append("")
    if o.get("incident_hours"):
        L.append("## Incident timeline — hours with elevated failures, stalled merges or Keeper loss")
        L.append("| hour | queries | exceptions | top codes | new parts | merges | failed bg ops | zk hw exc | zk tx % of median | flags |")
        L.append("|---|---|---|---|---|---|---|---|---|---|")
        for t in o["incident_hours"]:
            pct = "" if t.get("tx_pct") is None else f"{t['tx_pct']}%"
            L.append(f"| {t['hour']} | {t['queries']} | {t['exceptions']} | {t['top_codes']} | {t['new_parts']} | {t['merges']} | {t['failed_bg']} | {t['zk_hw']} | {pct} | {t['flags']} |")
        L.append("")
    if o.get("top_partitions"):
        L.append("## Top partitions by active parts")
        L.append("| database | table | partition | parts | level-0 | avg rows/part |")
        L.append("|---|---|---|---|---|---|")
        for t in o["top_partitions"]:
            L.append(f"| {t['database']} | {t['table']} | {t['partition_id']} | {t['parts']} | {t['l0_parts']} | {t['avg_rows']} |")
        L.append("")
    if o.get("top_errors"):
        L.append("## system.errors (cumulative since server start)")
        L.append("| code | name | count | last seen |")
        L.append("|---|---|---|---|")
        for e in o["top_errors"]:
            L.append(f"| {e['code']} | {e['name']} | {e['count']} | {e['last']} |")
        L.append("")
    L.append("## Files")
    L.append("| file | bytes | rows |")
    L.append("|---|---|---|")
    for f in o["files"]:
        L.append(f"| {f['file']} | {f['bytes']} | {f.get('rows', '')} |")
    return "\n".join(L)


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("path", help="clickhouse_backup_*.tar.gz or an extracted bundle directory")
    ap.add_argument("--json", action="store_true", help="emit JSON instead of markdown")
    ap.add_argument("--extract-to", help="directory to extract the archive into (default: a new temp dir)")
    args = ap.parse_args()

    path = os.path.abspath(args.path)
    if not os.path.exists(path):
        die(f"{path} does not exist")
    if os.path.isfile(path):
        dest = args.extract_to or tempfile.mkdtemp(prefix="chdiag_")
        os.makedirs(dest, exist_ok=True)
        safe_extract(path, dest)
        base = find_run_dir(dest)
        extracted_to = dest
    else:
        base = find_run_dir(path)
        extracted_to = None

    result = analyse(base)
    result["path"] = base
    if extracted_to:
        result["extracted_to"] = extracted_to
    if args.json:
        print(json.dumps(result, indent=2, default=str))
    else:
        print(render_md(result))
        if extracted_to:
            print(f"\n(extracted to {extracted_to} — reuse this directory for the SQL recipes)")


if __name__ == "__main__":
    main()
