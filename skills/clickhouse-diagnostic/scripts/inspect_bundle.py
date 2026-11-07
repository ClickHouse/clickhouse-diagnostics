#!/usr/bin/env python3
"""inspect_bundle.py — 对 clickhouse-diagnostic bundle（诊断包）做确定性预检。

用法：
    python3 inspect_bundle.py <clickhouse_diagnostic_*.tar.gz | 已解压目录> [--json] [--extract-to DIR]
    （改名之前采集的 bundle 是 clickhouse_backup_*.tar.gz；两种都能读取）

仅使用标准库。从磁盘读取 bundle，从不写入其中，也从不访问网络。打印覆盖情况说明、
文件清单，以及无需 SQL 即可计算完成的健康检查（完整规则集见 references/health-checks.md，
助手会在此基础上继续应用）。

要求 bundle 以默认的 `-output-format jsonl` 采集；`.native` 或 `.tsv` 的 bundle 会被
拒绝，而不是误读（见 require_jsonl）。

即使存在 finding，退出码也为 0；仅当输入无法读取时退出码为 2（路径错误、不是 bundle、
不安全的归档成员、输出格式错误）。
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
from datetime import datetime, timedelta

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


# ----------------------------------------------------------------------------- 辅助函数

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
    """带引号的 UInt64/Int64 会以字符串形式到达；在不损失精度的前提下转换。"""
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
    """把 bundle 中可能出现的各种时间戳格式解析为 naive datetime。

    接受 ClickHouse DateTime / DateTime64 文本（``2026-08-25 12:00:38``、
    ``2026-08-25 12:00:38.123456``）以及 RFC 3339（``2026-08-25T12:00:38Z``、
    ``2026-08-25T12:00:38.5+02:00``）。小数秒和时区后缀会被丢弃：脚本只会比较
    同一个文件中的值，它们共享同一个时区，因此时间跨度和窗口仍然正确。
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


# 在分隔符修复之前，用 Windows build 采集的 bundle 会把条目命名为
# `clickhouse_backup_…\\system.parts_….jsonl`：tar 名称按定义使用斜杠分隔，
# 但 collector 使用 filepath.Join 构造它们，而该函数使用宿主机的分隔符。
# 在别处解压时，这会变成名称含反斜杠的单个文件而不是目录，于是下面每个 glob
# 都匹配不到，bundle 读起来就是空的。这类 bundle 已经存在于未结工单中，因此
# 在读取时直接规范化分隔符，而不是要求客户重新采集。
WINDOWS_SEPARATOR_NOTE = (
    "bundle 由会向归档中写入反斜杠路径分隔符的 Windows build 打包（已在 collector 中修复）；"
    "解压时已将条目名规范化"
)


def _normalise_separators(tar: "tarfile.TarFile") -> bool:
    """把遗留的 Windows 条目名改写为斜杠。只要改动了任意一个就返回 True。

    只有完全不含 "/" 的名称才是 Windows 形态：那些 build 用反斜杠连接每个组件。
    在斜杠分隔的名称中，反斜杠只是普通文件名字符——configuration/ 和 logs/ 会
    以服务器的真实文件名复制文件，因此 `a\\b.xml` 与 `a/b.xml` 是两个文件，
    改写它会在解压时把二者合并。
    """
    touched = False
    for m in tar.getmembers():
        if "\\" in m.name and "/" not in m.name:
            m.name = m.name.replace("\\", "/")
            touched = True
    return touched


def _extracted_windows_bundle(path: str) -> bool:
    """普通 tar 从遗留 Windows 归档中解压出来的文件夹：平铺文件名为
    `clickhouse_backup_…\\system.version_….jsonl`。仅在 "/" 是分隔符时成立——
    在 Windows 上反斜杠就是目录分隔符。"""
    if os.sep != "/":
        return False
    return any("\\system.version_" in f for f in os.listdir(path))


def safe_extract(archive: str, dest: str) -> bool:
    """解压 bundle。修复过 Windows 分隔符时返回 True。"""
    with tarfile.open(archive, "r:*") as tar:
        repaired = _normalise_separators(tar)
        for m in tar.getmembers():
            name = m.name
            if name.startswith("/") or ".." in name.split("/") or m.issym() or m.islnk():
                die(f"拒绝不安全的归档成员：{name}")
            if not (m.isfile() or m.isdir()):  # 设备、FIFO 以及任何非常规文件
                die(f"拒绝非常规归档成员：{name}（类型 {m.type!r}）")
        try:
            tar.extractall(dest, filter="data")  # Python ≥ 3.12
        except TypeError:
            tar.extractall(dest)
    return repaired


# 运行文件夹和归档名为 clickhouse_diagnostic_<ts>；改名之前采集的 bundle 是
# clickhouse_backup_<ts>，且仍存在于未结工单中，因此两种都要读取。
RUN_NAME_RE = re.compile(r"clickhouse_(?:diagnostic|backup)_(\d{8}_\d{6})")


def find_run_dir(path: str) -> str:
    if glob.glob(os.path.join(path, "system.version_*.*")):
        return path
    kids = [os.path.join(path, k) for k in os.listdir(path)
            if RUN_NAME_RE.fullmatch(k) and os.path.isdir(os.path.join(path, k))]
    # 按名称中的时间戳取最新：若按名称排序，每个 clickhouse_diagnostic_* 都会
    # 排在每个 clickhouse_backup_* 之后。
    kids.sort(key=lambda k: RUN_NAME_RE.fullmatch(os.path.basename(k)).group(1))
    if len(kids) == 1:
        return kids[0]
    if kids:
        return kids[-1]
    die(f"{path} 看起来不是 clickhouse-diagnostic bundle（没有 system.version_* 文件）")
    return path


def require_jsonl(base: str) -> None:
    """拒绝以 -output-format native 或 tsv 采集的 bundle。

    下面的每个读取器都只 glob ``*.jsonl``。换成其他格式时它们都会返回空，
    报告会渲染成 "version: unknown, active parts: 0, Findings (0)"——
    一次虚假的“一切正常”，看起来和健康的服务器一模一样。在解析其他格式之前，
    明确报错是唯一安全的做法。
    """
    if glob.glob(os.path.join(base, "system.version_*.jsonl")):
        return
    exts = {os.path.splitext(p)[1] for p in glob.glob(os.path.join(base, "system.*.*"))}
    exts.discard(".jsonl")
    if not exts:
        return
    found = " / ".join(sorted(e.lstrip(".") for e in exts))
    die(f"bundle 是以 -output-format {found} 采集的；本脚本只读取 .jsonl，在 "
        f"{found} 格式下每个检查都会静默地报告为零。请以 -output-format jsonl "
        f"（collector 的默认值）重新采集后再运行。")


_RUN_TS_SUFFIX = re.compile(r"\d{8}_\d{6}(\.\w+)?$")


def first(pattern: str, base: str):
    """某个 collector 的第一个文件。以 `_*.<ext>` 结尾的模式表示
    "<collector>_<运行时间戳>.<ext>"：* 必须是该时间戳，因此
    system.replicas_*.jsonl 永远不会返回 system.replicas_problem_tables_*——
    而在 replicas collector 自身失败时，过去恰好会这样。"""
    hits = sorted(glob.glob(os.path.join(base, pattern)))
    name = os.path.basename(pattern)
    star = name.find("_*")
    if star >= 0 and name.count("*") == 1:
        prefix = name[:star + 1]
        hits = [h for h in hits if _RUN_TS_SUFFIX.fullmatch(os.path.basename(h)[len(prefix):] or "")]
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


# ----------------------------------------------------------------------------- 分析

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
    """gov：没有 dashboard/crash_log/stack_trace，存在 alerts_summary.json，名称经过哈希。
    cloud：clusters 指向 *.clickhouse.cloud，或 part_log 包含多个 hostname。"""
    # collector 会写下它实际运行的模式（v0.5+ 头部）；一次 onprem 运行如果在
    # SharedMergeTree 集群上切换成了 cloud 采集，那里会写 `mode: cloud`，
    # 而它的 part_log 可能仍只显示一个 host。下面的启发式规则适用于没有头部的 bundle。
    xl = os.path.join(base, "execution_log.txt")
    if os.path.exists(xl):
        with open(xl, encoding="utf-8", errors="replace") as fh:
            for line in fh:
                if line.startswith("Summary"):
                    break
                m = re.match(r"^mode:\s+(cloud|onprem|gov)\b", line)
                if m:
                    return m.group(1)
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


# Keeper 自己的 four-letter command（四字命令）回复，keeper/<host>_<port>.txt
# （v0.7+；isro、lgif、conf 和 dirs 是 collector 后来才学会采集的）。每个成员一个
# dict：它的状态，以及 HC-3.18-3.20 检查读取的数值。
KEEPER_NOT_SERVING = "This instance is not currently serving requests"
# Apache ZooKeeper 的措辞是 "This ZooKeeper instance is not currently serving requests"。
KEEPER_NOT_SERVING_RE = re.compile(r"^This (ZooKeeper )?instance is not currently serving requests$")


def keeper_not_serving(body: str) -> bool:
    return bool(KEEPER_NOT_SERVING_RE.match((body or "").strip()))
INT32_MAX = 2**31 - 1


def read_keeper_members(base: str) -> list:
    members = []
    for path in sorted(glob.glob(os.path.join(base, "keeper", "*.txt"))):
        try:
            text = open(path, encoding="utf-8", errors="replace").read()
        except OSError:
            continue
        m = re.search(r"^# target: (\S+)", text, re.M)
        em = re.search(r"^# ensemble: (\S+)", text, re.M)  # 记录 ensemble 之前不存在
        sec = {}
        for part in re.split(r"\n## ", "\n" + text)[1:]:
            name, _, body = part.partition("\n")
            sec[name.strip()] = body.strip()

        def answered(cmd):
            # 因读取截止时间而提前中断的回复会保留在文件中，后面跟着一行
            # "error: timeout … reply truncated"：它的计数器不完整——
            # 被截断的 last_log_idx 会解析成更小的数字——因此带任何
            # collector 错误行的 section 都不能作为证据。
            b = sec.get(cmd, "")
            if not b or keeper_not_serving(b) or b.startswith(("skipped", "(empty reply")):
                return False
            return not any(line.startswith("error:") for line in b.splitlines())

        kv = {}
        for cmd, sep in (("lgif", "\t"), ("mntr", "\t"), ("conf", "="), ("dirs", ":")):
            if answered(cmd):
                for line in sec[cmd].splitlines():
                    k, found, v = line.partition(sep)
                    if found:
                        kv[(cmd, k.strip())] = v.strip()
        # "not serving" 是 Keeper 自己的回答；"probe failed" 是 collector 的
        # 回答——在 srvr/mntr 应答之前超时或连接被拒绝——它不能说明投票成员
        # 彼此之间的链路状况。
        if any(keeper_not_serving(b) for b in sec.values()):
            state = "not serving"
        elif answered("srvr") or answered("mntr"):
            state = "serving"
        elif any(line.startswith("error:") for b in sec.values() for line in b.splitlines()):
            state = "probe failed"
        else:
            state = "unknown"
        role = kv.get(("mntr", "zk_server_state"))
        if not role and answered("srvr"):
            rm = re.search(r"^Mode: (\S+)", sec["srvr"], re.M)
            role = rm.group(1) if rm else None
        vm = re.search(r"v?(\d+)\.(\d+)", (kv.get(("mntr", "zk_version")) or "")
                       or (re.search(r"^ClickHouse Keeper version: (\S+)", sec.get("srvr", ""), re.M) or [None, ""])[1])
        members.append({"member": m.group(1) if m else os.path.basename(path), "state": state, "role": role,
                        "ensemble": em.group(1) if em else None,
                        "version": (int(vm.group(1)), int(vm.group(2))) if vm else None,
                        "kv": {k: num(v) if num(v) is not None else v for k, v in kv.items()}})
    return members


# ---- system.aggregated_zookeeper_log（26.3+）：谁在给 Keeper 施加负载、何时变慢、
# znode 在何处堆积（HC-3.21-3.23、P-62-P-68）。这三个文件只存在于 >= 26.3 的服务器；
# 文件缺失不能说明 Keeper 的任何情况。
AZL_MIN = "system.aggregated_zookeeper_log_by_minute_1_day"
AZL_HOUR = "system.aggregated_zookeeper_log_by_hour_2_days"
AZL_PATHS = "system.aggregated_zookeeper_log_paths_1_day"
AZL_REMOVES = ("Remove", "TryRemove", "RemoveRecursive")
AZL_DEDUP = ("/blocks", "/async_blocks", "/deduplication_hashes")


def count_h(n) -> str:
    n = num(n) or 0
    for div, unit in ((1e9, "B"), (1e6, "M"), (1e3, "k")):
        if abs(n) >= div:
            return f"{n / div:.1f}{unit}"
    return str(int(n))


def _median(xs):
    xs = sorted(xs)
    if not xs:
        return 0
    m = len(xs) // 2
    return xs[m] if len(xs) % 2 else (xs[m - 1] + xs[m]) / 2


def znode_counts(base: str) -> list:
    """从 keeper/<member>.txt 的 mntr 回复中取得每个 Keeper 成员的 zk_znode_count。"""
    out = []
    for path in sorted(glob.glob(os.path.join(base, "keeper", "*.txt"))):
        try:
            text = open(path, encoding="utf-8", errors="replace").read()
        except OSError:
            continue
        m = re.search(r"^zk_znode_count\s+(\d+)", text, re.M)
        if m:
            out.append({"member": os.path.basename(path)[:-4], "znodes": int(m.group(1))})
    return out


def aggregated_zk_checks(base: str, add) -> None:
    mins = read_jsonl(first(AZL_MIN + "_*.jsonl", base))
    if mins:
        by_host = {}
        for r in mins:
            by_host.setdefault(r.get("hostname") or "", []).append(r)
        where = lambda r: f"（host：{r['hostname']}）" if r.get("hostname") else ""
        slow = [r for r in mins if (num(r.get("ops")) or 0) > 0 and (num(r.get("avg_latency_us")) or 0) >= 100_000]
        if slow:
            w = max(slow, key=lambda r: num(r.get("avg_latency_us")) or 0)
            add("warning", "keeper",
                f"过去一天中有 {len(slow)} 分钟的 Keeper 请求平均延迟 ≥ 100 ms（正常约 10–20 ms）——最差 {round((num(w.get('avg_latency_us')) or 0) / 1000)} ms，出现在 {w.get('time')}{where(w)}",
                f"{AZL_MIN} avg_latency_us（加权，仅独立请求）", "HC-3.21/P-65")
        loss = [r for r in mins if (num(r.get("loss_failures")) or 0) > 0]
        if loss:
            add("warning", "keeper",
                f"Keeper 连接丢失 / 会话过期 / 超时应答出现在 {len(loss)} 分钟：{count_h(sum(num(r.get('loss_failures')) or 0 for r in loss))} 个请求，首次 {loss[0].get('time')}{where(loss[0])}，最后一次 {loss[-1].get('time')}{where(loss[-1])}",
                f"{AZL_MIN} loss_failures——需要与 text_log_keeper_1_day 对照的分钟", "HC-3.21/P-57")
        if len(by_host) >= 2:
            # 在同一批分钟内把每个 replica 与它的 peers 比较：一个只在变慢
            # 期间才存在的 replica，如果当时其他 peers 也同样慢，就不是
            # “慢的那一个”。至少需要 5 个共享分钟；否则不做 replica 级别的归因。
            lat = {}  # 分钟 -> {host: 延迟}
            for h, rs in by_host.items():
                for r in rs:
                    if (num(r.get("ops")) or 0) > 0:
                        lat.setdefault(r.get("time"), {})[h] = num(r.get("avg_latency_us")) or 0
            for h in by_host:
                mine, peers = [], []
                for minute, per in lat.items():
                    if h in per and len(per) >= 2:
                        mine.append(per[h])
                        peers.append(_median([v for k, v in per.items() if k != h]))
                if len(mine) < 5:
                    continue
                m, others = _median(mine), _median(peers)
                if m >= 50_000 and m >= 10 * max(others, 1):
                    add("warning", "keeper",
                        f"某个 replica 的 Keeper 请求偏慢：{h} 在同一 {len(mine)} 分钟内的中位延迟为每分钟 {round(m / 1000)} ms，而其 peers 为 {round(others / 1000, 1)} ms——问题在该 replica 的会话或它所在的 Keeper 成员，而不是整个 Keeper",
                        f"{AZL_MIN} 按 hostname，仅匹配相同的分钟；zookeeper_connection.host 指出成员", "HC-3.21/P-65")
        for h, rs in by_host.items():
            ops = [num(r.get("ops")) or 0 for r in rs]
            base_ops = _median(ops)
            peak = max(rs, key=lambda r: num(r.get("ops")) or 0)
            p = num(peak.get("ops")) or 0
            if p >= 10_000 and p >= 10 * max(base_ops, 1):
                add("info", "keeper",
                    f"Keeper 请求突发：{peak.get('time')}{where(peak)} 这一分钟有 {count_h(p)} 个请求，而每分钟中位数为 {count_h(base_ops)}——可能是一次重启（每张表都会重新读取自己的 part），或多个 replica 同时重新加载",
                    f"{AZL_MIN} ops；重启见 text_log_keeper_1_day / execution_log uptime", "HC-3.21/P-64")
    hours = read_jsonl(first(AZL_HOUR + "_*.jsonl", base))
    if hours:
        comp = Counter()
        comp_hour = {}
        for r in hours:
            n = (num(r.get("ops")) or 0) + (num(r.get("in_multi")) or 0)
            c = r.get("component") or "(无)"
            comp[c] += n
            comp_hour.setdefault(c, Counter())[r.get("time")] += n
        total = sum(comp.values())
        if total:
            add("info", "keeper",
                "过去两天 ClickHouse 各 component 的 Keeper 请求：" + "，".join(f"{c} {100 * n / total:.0f}%" for c, n in comp.most_common(5)) + f"，共 {count_h(total)} 个",
                f"{AZL_HOUR} 按 component 统计的 ops + in_multi", "HC-3.22")
            top, n = comp.most_common(1)[0]
            # 没有该 component 请求的小时不会出现在行里：在文件覆盖的范围内
            # 把它们按 0 计，否则只存在于故障小时的 component 会得到
            # typical == peak。
            seen = sorted({d for d in (parse_dt(r.get("time")) for r in hours) if d})
            if seen:
                span = int((seen[-1] - seen[0]).total_seconds() // 3600) + 1
                by_hour = {parse_dt(k): v for k, v in comp_hour[top].items() if parse_dt(k)}
                series = [by_hour.get(seen[0] + timedelta(hours=i), 0) for i in range(span)]
            else:
                series = list(comp_hour[top].values())
            peak, typical = max(series), _median(series)
            if n >= 0.6 * total and peak >= 100_000 and peak >= 10 * max(typical, 1):
                # 最大的单 replica 贡献（cloud 行会按 replica 求和）：指出某一个
                # replica-小时，而不是谁发送了全部请求。
                cand = [r for r in hours if r.get("component") == top and r.get("busiest_replica")]
                big = max(cand, key=lambda r: num(r.get("busiest_replica_ops")) or 0) if cand else None
                on = (f"；其最大的单 replica 小时：{count_h(big.get('busiest_replica_ops'))} 个 {big.get('operation')} 请求来自 "
                      f"{big.get('busiest_replica')}，时间 {big.get('time')}") if big else ""
                add("warning", "keeper",
                    f"{top} 占全部 Keeper 请求的 {100 * n / total:.0f}%，峰值达到每小时 {count_h(peak)}，而典型值为 {count_h(typical)}{on}——某个后台任务在 Keeper 上循环",
                    f"{AZL_HOUR} 按小时的 component × operation；路径见 {AZL_PATHS}", "HC-3.22/P-68")
        slow = sorted((r for r in hours if (num(r.get("ops")) or 0) >= 100 and (num(r.get("avg_latency_us")) or 0) >= 1_000_000),
                      key=lambda r: -(num(r.get("avg_latency_us")) or 0))
        if slow:
            add("warning", "keeper",
                "一小时内平均耗时 ≥ 1 s 的 Keeper 操作：" + "；".join(f"{r.get('operation')} 来自 {r.get('component')}，{round((num(r.get('avg_latency_us')) or 0) / 1e6, 1)} s，时间 {r.get('time')}" for r in slow[:3]),
                f"{AZL_HOUR} avg_latency_us（加权，仅独立请求）", "HC-3.22/P-66/P-67")
        # errors 映射同时覆盖独立请求和 Multi 内部的子请求
        rem = sum((num(r.get("ops")) or 0) + (num(r.get("in_multi")) or 0) for r in hours if r.get("operation") in AZL_REMOVES)
        rem_nonode = sum(num((r.get("errors") or {}).get("ZNONODE")) or 0 for r in hours if r.get("operation") in AZL_REMOVES)
        if rem >= 1_000_000 and rem_nonode >= 0.5 * rem:
            add("warning", "keeper",
                f"{count_h(rem)} 次 Keeper remove 中有 {100 * rem_nonode / rem:.0f}% 返回 ZNONODE——replicas 在删除相同记录（dedup 清理），每次失败的删除仍会让 Keeper 付出一次提交成本",
                f"{AZL_HOUR} 中 Remove / TryRemove / RemoveRecursive 的 errors", "HC-3.22/P-62")
    paths = read_jsonl(first(AZL_PATHS + "_*.jsonl", base))
    if paths:
        label = lambda r: r.get("path") or r.get("shape") or "?"
        total = sum(num(r.get("ops")) or 0 for r in paths)
        for r in sorted(paths, key=lambda r: -(num(r.get("net_creates")) or 0)):
            net, cr, rm = num(r.get("net_creates")) or 0, num(r.get("creates")) or 0, num(r.get("removes")) or 0
            if net >= 1_000_000 and cr >= 10 * max(rm, 1):
                # 都是成功的请求，但仍是一个请求净增：一次 RemoveRecursive 会删除
                # 一棵子树。当 Keeper 文件带有该数据时，znode 数量是独立证据。
                znodes = [f"{k['member']} {count_h(k['znodes'])}" for k in znode_counts(base)]
                ev = ("keeper/<member>.txt zk_znode_count：" + "，".join(znodes[:4])) if znodes else "用 keeper/<member>.txt 中的 zk_znode_count 确认（本 bundle 中没有）"
                add("warning", "keeper",
                    f"{label(r)} 下成功的 create 远多于 remove：一天内创建 {count_h(cr)}，删除 {count_h(rm)}（净增 {count_h(net)} 个请求）——与 znode 持续堆积一致，这会增长 Keeper 的内存和快照",
                    f"{AZL_PATHS} creates / removes（请求净增：一次 RemoveRecursive 会删除一棵子树）；{ev}", "HC-3.23/P-63")
                break
        if total:
            top = max(paths, key=lambda r: num(r.get("ops")) or 0)
            t = num(top.get("ops")) or 0
            if t >= 0.5 * total and t >= 1_000_000:
                im = num(top.get("in_multi")) or 0
                rd = num(top.get("reads")) or 0
                # 读风暴的判断需要 reads 或子请求，不能只看名称
                objects = "/_objects" in label(top) and (im >= 0.5 * t or rd >= 0.5 * t)
                add("warning", "keeper",
                    f"{label(top)} 占最繁忙 Keeper 路径请求的 {100 * t / total:.0f}%（一天 {count_h(t)} 个请求，其中 {100 * im / max(t, 1):.0f}% 在 Multi 内）"
                    + ("——文件数很多的大量 part 正从对象存储元数据加载" if objects else ""),
                    f"{AZL_PATHS} ops / in_multi / raw_paths", "HC-3.23/P-64" if objects else "HC-3.23")
        rm_all = sum(num(r.get("removes")) or 0 for r in paths)
        rm_dedup = sum(num(r.get("removes")) or 0 for r in paths if label(r).endswith(AZL_DEDUP))
        if rm_all >= 1_000_000 and rm_dedup >= 0.5 * rm_all:
            add("warning", "keeper",
                f"插入去重记录占 Keeper remove 的 {100 * rm_dedup / rm_all:.0f}%（一天 {count_h(rm_dedup)} 次）——许多小插入，每个都留下一个 block hash，随后每个 replica 都会清理它",
                f"{AZL_PATHS} 中 …/blocks、…/async_blocks、…/deduplication_hashes 上的 removes", "HC-3.23/P-62")


def analyse(base: str):
    out = {"bundle": os.path.basename(os.path.normpath(base)), "findings": [], "notes": []}
    findings = out["findings"]

    def add(sev, area, msg, evidence, check=""):
        findings.append({"severity": sev, "area": area, "message": msg, "evidence": evidence, "check": check})

    files = inventory(base)
    out["files"] = files
    out["mode"] = detect_mode(base, files)

    m = RUN_NAME_RE.search(out["bundle"])
    run_ts = datetime.strptime(m.group(1), "%Y%m%d_%H%M%S") if m else None
    out["collected_at"] = run_ts.isoformat(sep=" ") if run_ts else None

    ver_rows = read_jsonl(first("system.version_*.jsonl", base))
    out["version"] = ver_rows[0].get("version") if ver_rows else None

    empty = [f["file"] for f in files if f.get("rows") == 0]
    out["empty_files"] = empty
    trunc = [f for f in files if f.get("truncated")]
    for f in trunc:
        add("info", "logs", "日志文件尾部被截断；第一条幸存的行不是开头", f["truncated"], "HC-0")

    # ---- 来自 query_log_details 的覆盖情况
    ql = read_jsonl(first("system.query_log_details_7_days_*.jsonl", base))
    if ql:
        times = [parse_dt(r.get("time")) for r in ql]
        times = [t for t in times if t]
        if times:
            out["query_log_window"] = {"from": min(times).isoformat(sep=" "), "to": max(times).isoformat(sep=" "),
                                       "hour_buckets": len(set(times))}
    tl = read_jsonl(first("system.text_log_2*.jsonl", base))  # 不是 histogram / keeper-marker 文件
    if tl:
        ts = [parse_dt(r.get("event_time")) for r in tl]
        ts = [t for t in ts if t]
        if ts:
            out["text_log_window"] = {"rows": len(tl), "from": min(ts).isoformat(sep=" "), "to": max(ts).isoformat(sep=" ")}
            if len(tl) >= 2000:
                add("info", "logs", "system.text_log 切片达到了 2000 行上限；它只覆盖 "
                    f"{min(ts)} → {max(ts)}——不要据此断定整天都没有错误", f"{len(tl)} 行", "HC-11.4")

    # ---- 告警
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
            add(a["severity"] or "warning", "alerts", f"alert rule `{a['name']}` 已触发（{a['instances']} 个实例）",
                "dashboard.html DATA.alerts / alerts_summary.json", "HC-1")
        elif a["state"] == "error":
            add("info", "alerts", f"alert rule `{a['name']}` 无法运行（不是 finding——规则错误/授权/版本）", "", "HC-0")

    # ---- crash 日志
    crash = read_jsonl(first("system.crash_log_*.jsonl", base))
    if crash:
        add("critical", "availability", f"{len(crash)} 行 crash_log——服务器收到了致命信号",
            "；".join(f"{r.get('event_time')} signal {r.get('signal')} v{r.get('version')}" for r in crash[:3]), "HC-1.1")

    # ---- 是否为 SharedMergeTree？只读取一次：下面的 replicas 读取、覆盖情况
    # note 以及按 host 的检查都依赖它。cloud_mode = 1 是服务器自身的标志位；
    # 当 bundle 缺少 settings 文件时，Shared* 引擎是后备判断依据。
    st_rows = read_jsonl(first("system.settings_*.jsonl", base))
    cloud_mode = any(r.get("name") == "cloud_mode" and str(r.get("value")) in ("1", "true") for r in st_rows)
    tb = read_jsonl(first("system.tables_*.jsonl", base))
    shared_tables = [r for r in tb if str(r.get("engine", "")).startswith("Shared")]
    is_smt = cloud_mode or bool(shared_tables)

    # ---- replicas / replication queue（复制队列）
    # Cloud：每个请求都以 skip_unavailable_shards = 1 运行，因此下线的 replica
    # 会静默地从每个按 replica 的文件中消失，而不是让这些文件失败。
    # system.replicas_answered 会准确地指出它：回复中的 _shard_num 就是发送该
    # 回复的已配置 host 的位置，因此不涉及 host 名称匹配。
    answered = read_jsonl(first("system.replicas_answered_*.jsonl", base))
    missing = [r.get("host_name") or r.get("host_address") or f"position {r.get('pos')}" for r in answered
               if str(r.get("answered")) in ("0", "false", "False")]
    if missing:
        add("warning", "coverage",
            f"{len(answered)} 个 replica 中有 {len(missing)} 个没有响应本次采集：{', '.join(missing[:8])}"
            + (" …" if len(missing) > 8 else "")
            + "——它们的行会从每个按 replica 的文件中缺失（part_log、query_log、metric_log、replicas 等）；在把任何按 replica 的缺失解读为健康之前，先说明这一点",
            "system.replicas_answered（answered = 0，按集群位置匹配）", "HC-0")
    reps = read_jsonl(first("system.replicas_*.jsonl", base))
    # v0.7 起的 Cloud 采集带有 hostName()：每张表在每个 REPLICA 上一行，
    # 有问题的行在前，每个 host 最多 200 行。
    per_host = bool(reps) and "hostname" in reps[0]

    def rep_host(r):
        return f"（host：{r.get('hostname')}）" if per_host else ""

    ro = [r for r in reps if num(r.get("is_readonly")) == 1]
    if ro:
        # 按 host 的文件会把一张表在每个 replica 上重复一次：分别统计表和行，
        # 以免五个 host × 一张表被读成五张表。
        ro_tables = len({(r.get("database"), r.get("table")) for r in ro})
        add("critical", "replication",
            f"{ro_tables} 张 replicated 表处于只读状态" + (f"（跨 host 共 {len(ro)} 行表记录）" if per_host and len(ro) != ro_tables else ""),
            "；".join(f"{r.get('database')}.{r.get('table')}{rep_host(r)} session_expired={r.get('is_session_expired')}" for r in ro[:5]), "HC-1.4")
    delayed = [r for r in reps if (num(r.get("absolute_delay")) or 0) > 60]
    if delayed:
        worst = max(delayed, key=lambda r: num(r.get("absolute_delay")) or 0)
        if is_smt:
            # 在 SharedMergeTree 上没有可落后的 replication queue：
            # inserts_in_queue 是该 replica 尚未取回的 level-0 part 数量，
            # absolute_delay 是最旧 part 的年龄。当别处的 merge 覆盖了这些
            # part 时计数会下降，因此数字下降并不能证明该 replica 已追上。
            hosts = Counter(r.get("hostname") or "本 replica" for r in delayed)
            behind = sorted(delayed, key=lambda r: -(num(r.get("inserts_in_queue")) or 0))[:3]
            out["smt_lag"] = {"rows": len(delayed), "hosts": len(hosts),
                              "worst": f"{worst.get('database')}.{worst.get('table')}{rep_host(worst)} {worst.get('absolute_delay')} s / {worst.get('inserts_in_queue')} 个 part 落后"}
            add("warning", "replication",
                f"SharedMergeTree 集群上有 {len(delayed)} 行表记录 absolute_delay > 60 s"
                + (f"，涉及 {len(hosts)} 个 host" if per_host else "") + "——该 replica 尚未取回这些 part",
                "inserts_in_queue = 该 replica 尚未取回的 level-0 part 数，absolute_delay = 最旧 part 的年龄（当别处的 merge 覆盖它们时会下降——不能证明已追上）；落后最多："
                + "；".join(f"{r.get('database')}.{r.get('table')}{rep_host(r)} {r.get('inserts_in_queue')} 个 part，{r.get('absolute_delay')} s"
                            + (f"，最旧 {r.get('oldest_part_to_get')} 写于 {r.get('inserts_oldest_time')}" if r.get("oldest_part_to_get") else "") for r in behind)
                + (" · 按 host：" + "，".join(f"{h}={c}" for h, c in hosts.most_common(6)) if per_host else ""),
                "HC-3.1/P-59")
        else:
            add("warning", "replication", f"{len(delayed)} 张表 absolute_delay > 60 s",
                f"最大 {worst.get('absolute_delay')} s，位于 {worst.get('database')}.{worst.get('table')}{rep_host(worst)} queue_size={worst.get('queue_size')}", "HC-3.1")
    if per_host:
        rows_by_host = Counter(r.get("hostname") for r in reps)
        capped = [h for h, c in rows_by_host.items() if c == 200]
        if capped:
            out["notes"].append(f"system.replicas 每个 host 最多保留 200 行（有问题的行在前）；{len(capped)} 个 host 达到了上限，因此上面它们的延迟/排队列表只是最差的 200 行，而不是全部——replicated 表的数量见 system.tables")
    rq = read_jsonl(first("system.replication_queue_*.jsonl", base))
    if rq:
        by_type = Counter(r.get("type") for r in rq)
        with_exc = sum(1 for r in rq if r.get("last_exception"))
        big = {t: c for t, c in by_type.items() if c > 60}
        if with_exc or big:
            add("warning", "replication", f"replication queue（复制队列）：{len(rq)} 个条目，{with_exc} 个带有 last_exception",
                "，".join(f"{t}={c}" for t, c in by_type.most_common(5)), "HC-3.4")

    # ---- 磁盘
    disks = read_jsonl(first("system.disks_*.jsonl", base))
    # 哪些磁盘才真正存放用户数据：用户表所用 storage policy（存储策略）中
    # 指定的磁盘。不在该集合中的磁盘不是用户数据的容量——在托管服务上它是
    # 本地文件系统缓存，规模上就是会被填满的，过去在每个健康 bundle 上都被
    # 读成 critical（实测：一个健康的 Cloud replica 有 14.7% 空闲，而它的
    # 用户表位于对象存储策略上）。判断依据是 bundle 中的证据，而不是采集模式：
    # `-mode cloud` 只是一种查询布局，也可能指向自管理集群，在那里与对象存储
    # 分层并存的本地热数据磁盘仍然必须判为 critical。
    # bundle 中没有策略证据（任一文件缺失，或没有用户表声明策略）时，就评估
    # 每个磁盘——证据缺失不能变成沉默。与 alerts/disk_space_low.yaml 保持一致。
    pol = read_jsonl(first("system.storage_policies_*.jsonl", base))
    user_policies = {r.get("storage_policy") for r in tb
                     if r.get("storage_policy") and r.get("database") not in SYSTEM_DBS}
    data_disks = {d for r in pol if r.get("policy_name") in user_policies
                  for d in (r.get("disks") or []) if d}
    for d in disks:
        pct = num(d.get("free_pct"))
        if pct is None:
            continue
        if data_disks and d.get("name") not in data_disks:
            where = f"空闲 {d.get('free_space')} / 总计 {d.get('total_space')}"
            if pct < 5:
                add("warning", "disk", f"磁盘 `{d.get('name')}` 仅剩 {pct}% 空闲——它不存放用户数据，但如此小的余量仍会带来问题",
                    f"{where}；没有用户表的 storage policy 使用该磁盘，因此它不是数据容量——在托管服务上它是文件系统缓存，同时也存放 merge 和溢写排序的临时文件", "HC-4.1")
            elif pct < 15:
                add("info", "disk", f"磁盘 `{d.get('name')}` 剩余 {pct}% 空闲——不存放用户数据，因此不是容量 finding",
                    f"{where}；没有用户表的 storage policy 使用该磁盘（在托管服务上：文件系统缓存，规模上就是会被填满的）。数据磁盘是策略中列出的那些——请从它们判断容量", "HC-4.1")
            continue
        # 严重级别与 alerts/disk_space_low.yaml 和 HC-4.1 相同：低于 15% 为
        # critical（merge 需要余量，insert 会以 243 失败，replica 会在磁盘真正
        # 写满之前就变为只读）；低于 5% 时 dashboard 也会把该磁盘标红，
        # 因此要在消息中说明。
        if pct < 5:
            add("critical", "disk", f"磁盘 `{d.get('name')}` 仅剩 {pct}% 空闲——严重偏低，insert 可能已经失败", f"空闲 {d.get('free_space')} / 总计 {d.get('total_space')}", "HC-4.1")
        elif pct < 15:
            add("critical", "disk", f"磁盘 `{d.get('name')}` 剩余 {pct}% 空闲（< 15%，告警 disk_space_low）", f"空闲 {d.get('free_space')} / 总计 {d.get('total_space')}", "HC-4.1")

    # ---- part（数据分片）
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
            add(sev, "parts", f"{k[0]}.{k[1]} 分区 '{k[2]}' 有 {c} 个 active part（告警阈值 300）",
                f"level-0 part {l0[k]} 个，平均每 part {int(rows_sum[k] / c)} 行", "HC-2.1")
        elif l0[k] > 100:
            add("warning", "parts", f"{k[0]}.{k[1]} 分区 '{k[2]}' 有 {l0[k]} 个未 merge 的 level-0 part",
                f"共 {c} 个 active part", "HC-2.2")
        if c >= 100 and rows_sum[k] / c < 10000:
            add("warning", "parts", f"{k[0]}.{k[1]} 分区 '{k[2]}'：part 过小（{c} 个 part 平均 {int(rows_sum[k] / c)} 行）——小批量 insert",
                "system.parts rows/active part", "HC-2.3")
    huge = [p for p in active if (num(p.get("bytes_on_disk")) or 0) > 150 * GIB]
    if huge:
        add("warning", "parts", f"{len(huge)} 个 active part 超过 150 GiB（不会再被进一步 merge）",
            "；".join(f"{p.get('database')}.{p.get('table')} {p.get('name')} {human(p.get('bytes_on_disk'))}" for p in huge[:3]), "HC-2.9")
    if bytes_by_db:
        total = sum(bytes_by_db.values())
        ranked = bytes_by_db.most_common(3)
        out["bytes_by_database_top3"] = [{"database": d, "bytes": b, "human": human(b)} for d, b in ranked]
        sysb = bytes_by_db.get("system", 0)
        if total and sysb / total > 0.2 and sysb > 5 * GIB:
            add("warning", "disk", f"`system` 库占用 {human(sysb)}（占 active part 的 {sysb * 100 // total}%）——日志表没有 TTL？",
                "按 database 统计的 system.parts", "HC-4.4")

    # ---- detached part（分离 part）
    det = read_jsonl(first("system.detached_parts_*.jsonl", base))
    if det:
        reasons = Counter(r.get("reason") for r in det)
        broken = sum(c for r, c in reasons.items() if r and "broken" in str(r))
        add("warning" if broken else "info", "parts", f"{len(det)} 个 detached part",
            "，".join(f"{r or '(empty)'}={c}" for r, c in reasons.most_common()), "HC-2.11")

    # ---- merge / mutation（合并 / 变更）
    merges = read_jsonl(first("system.merges_*.jsonl", base))
    for mg in merges:
        el = num(mg.get("elapsed")) or 0
        if el > 3600 and (num(mg.get("progress")) or 0) < 0.5:
            add("warning", "merges", f"{mg.get('database')}.{mg.get('table')} 上的 merge 已运行 {int(el // 60)} 分钟，进度 {round((num(mg.get('progress')) or 0) * 100)}%",
                f"num_parts={mg.get('num_parts')} memory={human(mg.get('memory_usage'))} is_mutation={mg.get('is_mutation')}", "HC-2.5")
    muts = read_jsonl(first("system.mutations_*.jsonl", base))
    if muts:
        # `finished_mutations_to_keep`（默认 100）意味着文件中还保存着已完成的
        # mutation，因此单纯统计行数会仅因保留策略就超过 >100 阈值。只统计仍有
        # 工作要做的行，下面的 HC-8.1 也是这样——`number_of_mutations_to_throw`
        # 统计的也是未完成的 mutation。
        pending = [r for r in muts if (num(r.get("parts_to_do")) or 0) > 0]
        per_table = Counter((r.get("database"), r.get("table")) for r in pending)
        # Python 的循环变量会泄漏到函数作用域，因此这里命名为 mdb/mtbl 而不是
        # db/tb：`tb` 在 analyse() 的其余部分保存着 system.tables，过去一个未完成
        # 的 mutation 会把它重新绑定成表名——此后每次对它调用 r.get(...) 都会
        # 抛出 AttributeError，整个预检就什么也输出不了。
        for (mdb, mtbl), c in per_table.most_common(5):
            if c > 100:
                add("critical" if c >= 900 else "warning", "mutations", f"{mdb}.{mtbl} 有 {c} 个待处理 mutation（达到 number_of_mutations_to_throw 时会抛错，默认 1000）",
                    f"已排除文件中 {len(muts) - len(pending)} 个已完成的 mutation", "HC-8.2")
        if run_ts:
            for r in muts:
                ct = parse_dt(r.get("create_time"))
                todo = num(r.get("parts_to_do")) or 0
                if ct and todo > 0 and (run_ts - ct).total_seconds() > 3 * 3600:
                    add("warning", "mutations", f"{r.get('database')}.{r.get('table')} 上的 mutation {r.get('mutation_id')} 已待处理 {int((run_ts - ct).total_seconds() // 3600)} 小时，parts_to_do={todo}",
                        f"command：{str(r.get('command'))[:120]}", "HC-8.1")

    # ---- 错误
    errs = read_jsonl(first("system.errors_*.jsonl", base))
    errs_sorted = sorted(errs, key=lambda r: -(num(r.get("value")) or 0))
    out["top_errors"] = [{"code": num(r.get("code")), "name": r.get("name"), "count": num(r.get("value")),
                          "last": r.get("last_error_time")} for r in errs_sorted[:10]]
    for r in errs_sorted[:20]:
        code = num(r.get("code"))
        if code in (241, 173, 242, 999, 243, 252, 202, 49, 40, 226, 246):
            add("warning" if code not in (173, 243) else "critical", "errors",
                f"{r.get('name')} ({code}) 自服务器启动以来计到 {r.get('value')} 次",
                f"最后一次在 {r.get('last_error_time')}", "HC-1.5/HC-5.3")

    # ---- query_log 异常（去除 LEFT ARRAY JOIN 造成的重复）+ 按小时时间线
    timeline = {}  # 小时 -> 信号

    def tl(h):
        return timeline.setdefault(h, {"queries": 0, "exceptions": 0, "codes": Counter(), "new_parts": 0,
                                       "merges": 0, "failed_bg": Counter(), "zk_hw": 0, "zk_tx": None,
                                       "mv_failed": 0})

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
            add("warning", "queries", f"{total} 个查询中有 {sum(exc.values())} 个失败（{sum(exc.values()) * 100 // total}%）",
                "，".join(f"{ERROR_NAMES.get(c, c)}={n}" for c, n in exc.most_common(5)), "HC-6.1")
        if exc.get(202, 0) > 10:
            add("warning", "queries", f"TOO_MANY_SIMULTANEOUS_QUERIES (202) × {exc[202]}", "query_log_details", "HC-6.2")
        if exc.get(252, 0):
            add("warning", "inserts", f"TOO_MANY_PARTS (252) × {exc[252]}——insert 被拒绝", "query_log_details", "HC-2.1")
        if exc.get(999, 0) or exc.get(319, 0) or exc.get(571, 0):
            add("warning", "keeper", f"query_log 中依赖 Keeper 的失败：999 × {exc.get(999, 0)}，319 UNKNOWN_STATUS_OF_INSERT × {exc.get(319, 0)}，571 DATABASE_REPLICATION_FAILED × {exc.get(571, 0)}",
                "相关小时见故障时间线", "HC-3.8/P-57")
        if exc.get(107, 0) > 10:
            add("warning", "storage", f"FILE_DOESNT_EXIST (107) × {exc[107]}——part 的文件/blob 无法读取（事故后对象存储上的过期元数据，或文件缺失）",
                "按表统计 query_log_details；检查 system.disks.type 和 blob_storage_log", "P-58")
        if exc.get(57, 0) > 50:
            add("info", "ddl", f"TABLE_ALREADY_EXISTS (57) × {exc[57]}——如果消息在 .tmp.inner_id 表上提到 'UUID collision'，这是 Keeper 会话丢失后 Replicated database 的 DDL 重放",
                "distributed_ddl_queue、query_log 中 user 为空的 Create 行", "P-57")

    # ---- part_log：按小时统计 merge 停滞 / 后台操作失败
    pl_rows = read_jsonl(first("system.part_log_*_days_*.jsonl", base))
    # 23.11 以下的服务器使用根 part_log collector，它按 12 小时桶聚合；
    # 停滞 merge 规则（一个 HOUR 内 NewPart > 100 且 merge 为零）在这些行上
    # 没有意义，因此先检测桶宽度。
    pl_hours = {parse_dt(r.get("time")).hour for r in pl_rows if parse_dt(r.get("time"))}
    ver = tuple(int(x) for x in re.findall(r"\d+", out.get("version") or "")[:2]) if out.get("version") else ()
    old_server = bool(ver) and ver < (23, 11)  # 23.11 以下会选择根 part_log collector（12 小时桶）
    pl_12h = bool(pl_rows) and (old_server or (pl_hours <= {0, 12} and len({parse_dt(r.get("time")) for r in pl_rows if parse_dt(r.get("time"))}) > 1))
    if pl_12h:
        out["notes"].append("part_log 按 12 小时桶聚合（server < 23.11）：跳过按小时的停滞 merge 检查；时间线中的 merges/new-parts 列是 12 小时总量")
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
        stalled = [] if pl_12h else sorted(h for h, t in timeline.items() if t["new_parts"] > 100 and t["merges"] == 0)
        if stalled:
            # HC-2.12：连续三个小时才判 critical，而不是三个孤立小时
            run = best = 1
            for a, b in zip(stalled, stalled[1:]):
                da, db = datetime.strptime(a, "%Y-%m-%d %H:%M"), datetime.strptime(b, "%Y-%m-%d %H:%M")
                run = run + 1 if (db - da).total_seconds() == 3600 else 1
                best = max(best, run)
            add("critical" if best >= 3 else "warning", "merges",
                f"{len(stalled)} 个小时有插入（NewPart > 100）但完成的 merge 为零——merge 没有在运行",
                "小时：" + "，".join(stalled[:8]) + (" …" if len(stalled) > 8 else ""), "HC-2.12/P-57")
        # 与 alerts/background_operation_failures.yaml 相同的规则：同一种
        # (event_type, code) 在 ONE 小时内失败 > 50 次——绝不跨小时求和
        bg_hours = []
        for h in sorted(timeline):
            for (et, err), v in timeline[h]["failed_bg"].items():
                if v > 50:
                    bg_hours.append((h, et, err, v))
        if bg_hours:
            worst = sorted(bg_hours, key=lambda x: -x[3])[:5]
            add("warning", "merges", f"后台操作在 {len({x[0] for x in bg_hours})} 个小时内反复失败（part_log，一小时内同一种 > 50 次）",
                "；".join(f"{h} {et} code {err} {ERROR_NAMES.get(err, '')} × {v}" for h, et, err, v in worst), "HC-2.5")

    # ---- metric_log_coordination：按小时的 Keeper 硬件异常、只读 replica、S3 错误
    mlc = read_jsonl(first("system.metric_log_coordination_3_days_*.jsonl", base))
    mlc_has_zk = bool(mlc) and ZK_HW_COL in mlc[0] and ZK_TX_COL in mlc[0]
    if mlc and not mlc_has_zk:
        out["notes"].append("该版本的 metric_log_coordination 没有 ZooKeeper 计数器列——Keeper 信号改从 metric_log_7_days 获取")
    if mlc:
        cols = set(mlc[0].keys())
        for r in mlc:
            h = hour_key(r.get("time"))
            if not h or not mlc_has_zk:
                continue
            t = tl(h)
            t["zk_hw"] = num(r.get(ZK_HW_COL)) or 0
            t["zk_tx"] = num(r.get(ZK_TX_COL))
        ro_col = "max(CurrentMetric_ReadonlyReplica)"
        if ro_col in cols:
            ro_hours = sorted(hour_key(r.get("time")) for r in mlc if (num(r.get(ro_col)) or 0) > 0)
            if ro_hours:
                add("warning", "replication", f"{len(ro_hours)} 个小时内存在只读 replica（metric_log ReadonlyReplica 最大值 > 0）",
                    f"首次 {ro_hours[0]}，最后一次 {ro_hours[-1]}", "HC-1.4")
        s3_cols = [c for c in cols if c.startswith("sum(ProfileEvent_") and ("S3" in c or "ObjectStorage" in c or "AzureBlobStorage" in c) and c.endswith("Errors)")]
        s3_err = {c: sum(num(r.get(c)) or 0 for r in mlc) for c in s3_cols}
        s3_err = {c: v for c, v in s3_err.items() if v}
        if s3_err:
            add("warning", "storage", "metric_log 中存在对象存储请求错误",
                "，".join(f"{c[len('sum(ProfileEvent_'):-1]}={v}" for c, v in sorted(s3_err.items(), key=lambda kv: -kv[1])[:5]), "HC-4.10")

    if not mlc_has_zk:
        # 较旧的 bundle，或没有 Keeper 列的 coordination 文件：回退到固定列的聚合
        for r in read_jsonl(first("system.metric_log_7_days_*.jsonl", base)):
            h = hour_key(r.get("time"))
            if h:
                tl(h)["zk_hw"] = num(r.get("zk_hw_exceptions")) or 0
                tl(h)["zk_tx"] = num(r.get("zk_transactions"))

    # ---- Keeper 健康检查（HC-3.8）：按小时比较硬件异常 AND 事务量与 7 天中位数
    # 只要 metric_log_7_days 存在，中位数就取自它；3 天的 coordination 文件只
    # 提供按小时的信号。没有 7 天文件时，回退到已知的那些小时。
    #
    # 在 cloud bundle 上该基线是整个集群的：collector 的 metric_log_7_days
    # 查询只按小时分组，因此每个 replica 的小时都被合并在一起。
    # alerts/keeper_health.yaml 按 hostName() 分组，对同一小时会报告不同的
    # 百分比——而且单个 replica 上的故障在这里可能完全不可见。不要把这个结论
    # 表述为按 replica 的结论。
    ml7 = read_jsonl(first("system.metric_log_7_days_*.jsonl", base))
    tx_vals = sorted(num(r.get("zk_transactions")) or 0 for r in ml7) if ml7 else \
        sorted(t["zk_tx"] for t in timeline.values() if t["zk_tx"] is not None)
    tx_med = tx_vals[len(tx_vals) // 2] if tx_vals else 0
    tx_hours = [h for h, t in timeline.items() if t["zk_tx"] is not None]
    edge_hours = {min(tx_hours), max(tx_hours)} if tx_hours else set()  # 窗口两端的不完整桶
    for h, t in timeline.items():
        pct = (100 * t["zk_tx"] / tx_med) if (t["zk_tx"] is not None and tx_med) else None  # 用于判断，不取整
        t["tx_pct"] = round(pct) if pct is not None else None  # 仅用于展示时取整
        if pct is None:
            t["keeper"] = "exceptions, no traffic baseline" if t["zk_hw"] > 1000 else ""
        elif t["zk_hw"] > 1000 and pct < 50:
            t["keeper"] = "UNAVAILABLE"
        elif t["zk_hw"] > 1000:
            t["keeper"] = "blip"
        elif pct < 10 and h not in edge_hours:
            t["keeper"] = "idle/disconnected"
        else:
            t["keeper"] = ""
    unavailable = sorted(h for h, t in timeline.items() if t["keeper"] == "UNAVAILABLE")
    blips = sorted(h for h, t in timeline.items() if t["keeper"] == "blip")
    if unavailable:
        peak = max(unavailable, key=lambda h: timeline[h]["zk_hw"])
        add("critical", "keeper", f"Keeper 在 {len(unavailable)} 个小时内实际不可用：每小时 > 1000 次硬件异常，同时 Keeper 事务量降到 7 天中位数的 50% 以下（{tx_med}/h）；峰值出现在 {peak}，有 {timeline[peak]['zk_hw']} 次异常，流量为 {timeline[peak]['tx_pct']} %",
            "小时：" + "，".join(unavailable[:8]) + (" …" if len(unavailable) > 8 else "") + "——这些小时内每个 999/319/571/252 finding 都是下游后果（告警 keeper_health）", "HC-3.8/P-57")
    if blips:
        add("warning", "keeper", f"Keeper 会话在 {len(blips)} 个小时内丢失并重新建立，而流量保持正常（每小时 > 1000 次硬件异常）",
            "小时：" + "，".join(blips[:8]) + (" …" if len(blips) > 8 else "") + "——查看 text_log_keeper 标记以确定分钟和 Keeper host（告警 keeper_connection_blips）", "HC-3.8")
    tk = read_jsonl(first("system.text_log_keeper_1_day_*.jsonl", base))
    if tk:
        mk = Counter()
        for r in tk:
            mk[r.get("marker")] += num(r.get("count")) or 0
        conn = [r for r in tk if r.get("marker") == "connected"]
        ex = conn[-1].get("example") if conn else ""
        if mk.get("session_expired") or mk.get("reconnecting"):
            add("warning", "keeper", "text_log 中的 Keeper 会话标记（最后一天）：" + "，".join(f"{k}={v}" for k, v in mk.most_common()),
                (f"最后一次重连：{str(ex)[:160]}" if ex else ""), "HC-3.8")
    el = read_jsonl(first("system.error_log_7_days_*.jsonl", base))
    if el:
        by_code = Counter()
        for r in el:
            by_code[num(r.get("code"))] += num(r.get("errors")) or 0
            h = hour_key(r.get("time"))
            if h and num(r.get("code")) in (999, 242, 319, 571):
                tl(h)["codes"][num(r.get("code"))] += 0  # 让该小时在时间线中可见
        top = by_code.most_common(6)
        out["error_log_top"] = [{"code": c, "name": ERROR_NAMES.get(c, "?"), "count": n} for c, n in top]

    # ---- query_views_log：materialized view（物化视图）执行情况
    #
    # 塑造下面每个检查的读取规则（在 26.7 上验证；见 P-36）：
    #   * 默认情况下，抛错的 MV 会让父 INSERT 失败。随后 query_log 会带上该
    #     失败（"… while pushing to view db.mv"），服务器会为管道中的 EVERY
    #     view 写一行 ExceptionWhileProcessing，消息相同——因此行数会打平，
    #     只有消息能说明是哪个 view 抛错。兄弟 view 之间的行数是同一次失败的
    #     INSERT 按 view 重复计数，而不是 N 次失败。
    #   * 在 materialized_views_ignore_errors = 1 下，INSERT 成功，只有抛错的
    #     view 会得到 ExceptionWhileProcessing 行，text_log 会说
    #     "Error is ignored because the setting … is enabled"。
    #   * ExceptionWhileProcessing 行上的 written_rows 统计的是从未落地的行
    #     （insert 已回滚）；只有 QueryFinish 行才是真正的写入。
    #   * 对过滤型 MV 来说，zero_write_executions 是正常的。只有 STOPPED 写入
    #     的 view 才是证据；从未写入的 view 是一种设计（P-35）。
    #   * 可刷新 MV（REFRESH EVERY / AFTER）从不写入 query_views_log。
    qv = read_jsonl(first("system.query_views_log_3_days_*.jsonl", base))
    tbl_rows = tb
    refresh_re = re.compile(r"\bREFRESH\s+(EVERY|AFTER)\b", re.I)
    mv_tables = [r for r in tbl_rows if r.get("engine") == "MaterializedView"]
    # 可刷新 MV 被排除在覆盖检查之外：它们不会在 insert 时触发，因此它们在
    # query_views_log 中缺失并不说明任何问题。Gov 不提供 DDL，因此在那里无法
    # 做该排除，措辞保持留有余地。
    insert_mvs = [r for r in mv_tables if not refresh_re.search(str(r.get("create_table_query") or ""))]
    has_ddl = any(r.get("create_table_query") for r in tbl_rows)

    def view_label(r):
        # gov 会把 view_name 拆成经过哈希的 database/table 两半。
        if r.get("view_name") is not None:
            return str(r.get("view_name"))
        return f"{r.get('view_database', '?')}.{r.get('view_table', '?')}"

    if not qv and insert_mvs:
        refreshable = len(mv_tables) - len(insert_mvs)
        add("warning", "coverage",
            f"存在 {len(insert_mvs)} 个 insert 驱动的 materialized view，但 query_views_log 缺失/为空"
            + (f"（已排除 {refreshable} 个可刷新 MV——它们从不写入该日志）" if refreshable else "")
            + ("——没有 DDL 无法区分 MV 类型（gov）" if not has_ddl else ""),
            "可能原因：窗口内没有 INSERT 命中这些 view、log_query_views = 0、"
            "<query_views_log> 未配置，或 log_queries_min_type / log_queries_min_query_duration_ms "
            "进行了过滤；无论哪种情况，这里“没有 MV finding”并不代表这些 MV 是健康的", "HC-0")
    elif qv:
        views = {}
        last_hour_global = None
        for r in qv:
            # 服务器会为 view 写 QueryFinish / ExceptionWhileProcessing
            # （在 26.7 上验证：没有 QueryStart 行）。仍然像 query_log 那样
            # 跳过它们——start 行不携带资源数据。
            if r.get("status") == "QueryStart":
                continue
            v = views.setdefault(view_label(r), {
                "executions": 0, "zero_write": 0, "read": 0, "written": 0, "failures": 0,
                "codes": Counter(), "max_ms": 0, "hours": {}, "fail_hours": Counter(),
                "culprits": Counter(), "mem": [],
                "target": r.get("view_target") or r.get("target_table") or ""})
            ex = num(r.get("executions")) or 0
            zw = num(r.get("zero_write_executions")) or 0
            rd = num(r.get("read_rows")) or 0
            wr = num(r.get("written_rows")) or 0
            v["executions"] += ex
            v["max_ms"] = max(v["max_ms"], num(r.get("max_view_duration_ms")) or 0)
            code = num(r.get("exception_code")) or 0
            h = hour_key(r.get("time"))
            if h and (last_hour_global is None or h > last_hour_global):
                last_hour_global = h
            if r.get("status") == "ExceptionWhileProcessing":
                # 失败行上的行数、字节数和 zero-write 计数描述的是已回滚的
                # insert：不计为写入。
                v["failures"] += ex
                if code:
                    v["codes"][code] += ex
                if h:
                    v["fail_hours"][h] += ex
                # 每个失败行的消息都会指出抛错的 view；保留 ALL of them——
                # 两个互不相关的坏 view 会指出两个元凶。
                m = re.search(r"while pushing to view (\S+)", str(r.get("exception") or ""))
                if m:
                    v["culprits"][m.group(1).rstrip(".,;:")] += ex
            else:
                v["zero_write"] += zw
                v["read"] += rd
                v["written"] += wr
                # 按小时的有序写入历史，用于区分“停止写入”、“从未写入”
                # 和“开始写入”。
                if h:
                    hb = v["hours"].setdefault(h, {"wrote": False, "read": False})
                    hb["wrote"] = hb["wrote"] or wr > 0
                    hb["read"] = hb["read"] or rd > 0
                mem = num(r.get("median_peak_memory_usage"))
                if h and mem is not None:
                    v["mem"].append((h, mem, wr))

        # ---- 归因：按消息中提到的 view 对失败的 view 分组。
        # 默认设置下每个兄弟 view 都会带上元凶的消息；在 ignore_errors 下
        # 只有元凶有行。无论哪种情况，元凶都是被指名的 view，它自己的行数
        # 就是失败的 INSERT 次数——兄弟 view 只是同一批失败的重复出现。
        groups = {}      # 元凶 -> {"failures", "codes", "siblings"}
        unattributed = []
        for k, v in views.items():
            if not v["failures"]:
                continue
            if v["culprits"]:
                for c, n in v["culprits"].items():
                    g = groups.setdefault(c, {"failures": 0, "codes": Counter(), "self_codes": None, "siblings": set(), "self": False})
                    if c == k:
                        # 元凶自己的行是权威的计数和错误码；兄弟 view
                        # 只是重复它们。
                        g["failures"] = max(g["failures"], v["failures"])
                        g["self_codes"] = Counter(v["codes"])
                        g["self"] = True
                    else:
                        g["siblings"].add(k)
                        if not g["self"]:
                            g["failures"] = max(g["failures"], n)
                            g["codes"].update(v["codes"])
            else:
                unattributed.append((k, v))
        # 时间线中按小时统计的失败 INSERT：取该小时各 view 的最大值
        # （兄弟 view 会重复元凶的计数），绝不用求和。
        for h in set(hh for v in views.values() for hh in v["fail_hours"]):
            tl(h)["mv_failed"] = max(v["fail_hours"].get(h, 0) for v in views.values())

        total_exec = sum(v["executions"] for v in views.values())
        out["query_views_log"] = {
            "views": len(views), "executions": total_exec,
            "written_rows": sum(v["written"] for v in views.values()),
            "views_failing": len(groups) + len(unattributed),
            "views_never_wrote": sum(1 for v in views.values()
                                     if v["written"] == 0 and v["read"] > 0),
        }

        # ---- 按执行次数排名的 top view（用途统计，不是 finding）。`failures` 是
        # 该 view 自己的 ExceptionWhileProcessing 计数；对兄弟 view 而言，这是
        # 再次看到的元凶失败数，下面的 finding 会对此解释。
        out["top_views"] = [{
            "view": k, "executions": v["executions"], "written_rows": v["written"],
            "zero_write_pct": (v["zero_write"] * 100 // v["executions"]) if v["executions"] else 0,
            "failures": v["failures"],
            "median_peak_mem": human(sorted(m[1] for m in v["mem"])[len(v["mem"]) // 2]) if v["mem"] else "",
            "max_ms": v["max_ms"],
        } for k, v in sorted(views.items(), key=lambda kv: -kv[1]["executions"])[:10]]

        # ---- HC-7.7 每个元凶一个 finding
        for c, g in sorted(groups.items(), key=lambda kv: -kv[1]["failures"])[:3]:
            code_src = g["self_codes"] if g["self_codes"] is not None else g["codes"]
            codes = ", ".join(f"{ERROR_NAMES.get(x, x)}({x})={n}" for x, n in code_src.most_common(3))
            sev = "critical" if g["failures"] >= 100 else "warning"
            if g["siblings"]:
                mode = (f"{len(g['siblings'])} 个兄弟 view 带上了它的消息——INSERT 失败了"
                        "（默认设置），因此管道中的每个 view 都被标记")
            else:
                mode = ("没有兄弟 view 带上它的消息——与 materialized_views_ignore_errors = 1 一致，"
                        "此时 INSERT 成功，只记录抛错的 view（text_log："
                        "'Error is ignored because the setting materialized_views_ignore_errors is enabled'）")
            add(sev, "inserts",
                f"materialized view `{c}` 的 {g['failures']} 次 push 失败——基础 part 已提交，而"
                "目标未提交",
                f"{codes}；在异常文本中被指名；{mode}", "HC-7.7/P-36")
        if unattributed:
            total = sum(v["failures"] for _, v in unattributed)
            all_codes = Counter()
            for _, v in unattributed:
                all_codes.update(v["codes"])
            codes = ", ".join(f"{ERROR_NAMES.get(x, x)}({x})={n}" for x, n in all_codes.most_common(3))
            if len(unattributed) == 1:
                k, v = unattributed[0]
                add("critical" if v["failures"] >= 100 else "warning", "inserts",
                    f"materialized view `{k}` 的 {v['failures']} 次 push 失败——基础 part 已提交，而"
                    "目标未提交", f"{codes}；唯一失败的 view（没有异常文本可供确认）", "HC-7.7/P-36")
            else:
                add("critical" if max(v["failures"] for _, v in unattributed) >= 100 else "warning", "inserts",
                    f"{len(unattributed)} 个 materialized view 存在失败的 push，每个最多 {max(v['failures'] for _, v in unattributed)} 次"
                    "——基础 part 已提交，目标未提交；无法归因（没有异常文本"
                    "指出 view——gov，或消息中没有 'while pushing to view'）",
                    f"{codes}；views：" + "，".join(k for k, _ in unattributed[:5]), "HC-7.7/P-36")

        # ---- HC-7.8 停止写入的 view 与从未写入的 view。
        # 按顺序：最后一个写入的小时，然后是一段 LATER 的、读取了行但未写入的
        # 小时。窗口的最后一个桶仍在填充，排除在外。这段长度必须大于该 view
        # 自身此前最大的写入间隔——每六小时写一次的 view 按设计就有五小时
        # 间隔，末尾三个安静小时并不值得报告。
        stopped = []
        for k, v in views.items():
            hours = sorted(v["hours"].items())
            wrote_hours = [h for h, b in hours if b["wrote"]]
            if not wrote_hours:
                continue
            last_write = wrote_hours[-1]
            trailing = [h for h, b in hours
                        if h > last_write and h != last_hour_global and b["read"] and not b["wrote"]]
            max_gap = 0
            for a, b in zip(wrote_hours, wrote_hours[1:]):
                gap = sum(1 for h, hb in hours if a < h < b and hb["read"] and not hb["wrote"])
                max_gap = max(max_gap, gap)
            if len(trailing) >= 2 and len(trailing) > max_gap:
                stopped.append((k, last_write, trailing, len(wrote_hours), max_gap))
        for k, last_write, trailing, nwrote, max_gap in sorted(stopped, key=lambda x: -len(x[2]))[:3]:
            add("warning", "inserts",
                f"materialized view `{k}` 已停止写入：一直写到 {last_write}，随后直到 {trailing[-1]} 的 {len(trailing)} 个小时"
                f"只读取行而不写入",
                f"它不是过滤型 MV——在同一窗口中它曾在 {nwrote} 个小时内写入，此前最长的"
                f"安静间隔为 {max_gap} 小时；检查 as_select 以及它 JOIN 的任何表",
                "HC-7.8/P-35")
        never = [k for k, v in views.items() if v["written"] == 0 and v["read"] > 0]
        if never:
            add("info", "inserts",
                f"{len(never)} 个 materialized view 在窗口内读取了行但从未写入",
                "对过滤型 MV 来说是预期行为——在视为故障之前，请先对照 system.tables.as_select 验证",
                "HC-7.8/P-35")

        # ---- HC-5.9 按 view 的内存阶跃：按数量把小时桶分成两半，
        # 比较前半段与后半段。
        for k, v in views.items():
            if len(v["mem"]) < 6:
                continue
            ms = sorted(v["mem"])
            half = len(ms) // 2
            a = [m[1] for m in ms[:half]]
            b = [m[1] for m in ms[half:]]
            wa = sum(m[2] for m in ms[:half])
            wb = sum(m[2] for m in ms[half:])
            med_a = sorted(a)[len(a) // 2]
            med_b = sorted(b)[len(b) // 2]
            if med_a > 0 and med_b >= 3 * med_a and wa > 0 and 0.8 <= wb / wa <= 1.25:
                add("warning", "memory",
                    f"materialized view `{k}` 的中位峰值内存上升了 {round(med_b / med_a, 1)} 倍"
                    f"（{human(med_a)} → {human(med_b)}），而写入行数保持平稳",
                    f"前半段 {ms[0][0]} → 后半段 {ms[-1][0]}；检查 system.version 是否发生了升级",
                    "HC-5.9/P-54")

    # ---- 故障小时：异常升高、merge 停滞、Keeper 丢失、后台操作失败
    exc_hours = [t["exceptions"] for t in timeline.values() if t["exceptions"]]
    med = sorted(exc_hours)[len(exc_hours) // 2] if exc_hours else 0
    incident = []
    for h in sorted(timeline):
        t = timeline[h]
        flags = []
        if t["exceptions"] >= 50 and (t["exceptions"] >= 0.2 * max(t["queries"], 1) or t["exceptions"] >= 5 * max(med, 1)):
            flags.append("异常")
        if not pl_12h and t["new_parts"] > 100 and t["merges"] == 0:
            flags.append("无 merge")
        if t.get("keeper"):
            flags.append(f"keeper {t['keeper']}")
        if sum(t["failed_bg"].values()) > 50:
            flags.append("后台失败")
        # 阈值与其他 flag 一致：三天里每小时一次失败 push 已经是一个
        # finding（HC-7.7 会报告它），而不是 72 个故障小时，把真正
        # 的 TOO_MANY_PARTS 小时挤出 48 行的表格。
        if t.get("mv_failed", 0) >= 10:
            flags.append(f"mv 失败 {t['mv_failed']}")
        if flags:
            incident.append({"hour": h, "queries": t["queries"], "exceptions": t["exceptions"],
                             "top_codes": ", ".join(f"{ERROR_NAMES.get(c, c)}={n}" for c, n in t["codes"].most_common(3)),
                             "new_parts": t["new_parts"], "merges": t["merges"], "failed_bg": sum(t["failed_bg"].values()),
                             "zk_hw": t["zk_hw"], "tx_pct": t.get("tx_pct"), "flags": ", ".join(flags)})
    out["incident_hours"] = incident[:48]
    if incident:
        out["notes"].append(f"检测到故障小时：{incident[0]['hour']} → {incident[-1]['hour']}（{len(incident)} 个小时）——见时间线表格")

    # ---- 为 Keeper / 对象存储事故补充的快照
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
            who = f"（host：{host}）" if host else ""
            # ZooKeeperSession 在所有 build 中都会导出，任何未配置 Keeper 的
            # 服务器上它都保持为 0，因此单看该指标无法区分“会话丢失”和
            # “从未有过会话”。始终报告它，但只有在独立的证据表明 Keeper
            # 正在使用时才判为 critical：存在 Replicated*/Shared* 表
            # （system.replicas 有行），或窗口内有 Keeper 流量。
            if mm.get("ZooKeeperSession") == 0:
                keeper_in_use = bool(reps) or bool(tx_vals and max(tx_vals) > 0)
                add("critical" if keeper_in_use else "info", "keeper",
                    f"当前没有 Keeper 会话（ZooKeeperSession = 0）{who}"
                    + ("" if keeper_in_use else "——在没有 Keeper 的单节点安装中是预期行为；"
                                                "窗口内没有 replicated 表，也没有 Keeper 流量"),
                    "system.metrics", "HC-3.12")
            if (mm.get("ReadonlyReplica") or 0) > 0:
                add("critical", "replication", f"当前有 {mm['ReadonlyReplica']} 张只读 replica 表{who}", "system.metrics", "HC-1.4")
        cache = {h: mm.get("MetadataFromKeeperCacheObjects") for h, mm in by_host.items() if mm.get("MetadataFromKeeperCacheObjects") is not None}
        if len(cache) >= 2:
            vals = sorted(cache.values())
            median = vals[len(vals) // 2]
            low = [h for h, v in cache.items() if median > 1000 and v < median / 100]
            if low:
                add("warning", "storage", f"{len(low)} 个 replica 的 Keeper 元数据缓存几乎为空（MetadataFromKeeperCacheObjects ≪ peers，中位数 {int(median)}）",
                    "，".join(low[:6]), "HC-3.12/P-58")
    zc = read_jsonl(first("system.zookeeper_connection_*.jsonl", base))
    for r in zc:
        who = f"（host：{r['hostname']}）" if r.get("hostname") else ""
        if num(r.get("is_expired")):
            add("critical", "keeper", f"Keeper 会话当前已过期{who}", f"host {r.get('host')}", "HC-3.9")
        age = num(r.get("session_uptime_elapsed_seconds"))
        if age is not None and uptime and age < uptime / 10 and uptime > 3600:
            add("warning", "keeper", f"Keeper 会话只有 {int(age // 60)} 分钟，而服务器已运行 {int(uptime // 3600)} 小时{who}——会话是在 {r.get('connected_time')} 重新建立的",
                f"host {r.get('host')}", "HC-3.9/P-57")
    dbs = read_jsonl(first("system.databases_*.jsonl", base))
    if dbs:
        eng = Counter(r.get("engine") for r in dbs)
        out["databases"] = dict(eng)
        if eng.get("Replicated", 0) >= 50:
            add("info", "keeper", f"{eng['Replicated']} 个 Replicated database（共 {len(dbs)} 个）——每个 replica 上各有一个 DDLWorker 和一套 Keeper watch",
                "system.databases", "HC-3.13")
    ddl = read_jsonl(first("system.distributed_ddl_queue_*.jsonl", base))
    if ddl:
        ref = run_ts or datetime.now()
        stuck = [r for r in ddl if r.get("status") not in ("Finished", None) and parse_dt(r.get("query_create_time")) and (ref - parse_dt(r.get("query_create_time"))).total_seconds() > 600]
        if stuck:
            hosts = Counter(r.get("host") for r in stuck)
            add("warning", "ddl", f"{len(stuck)} 行 DDL queue 在 10 分钟后仍未 Finished", "hosts：" + "，".join(f"{h}×{n}" for h, n in hosts.most_common(4)), "HC-3.11")
        codes = Counter(num(r.get("exception_code")) for r in ddl if num(r.get("exception_code")))
        if codes:
            add("info", "ddl", "DDL queue 异常", "，".join(f"{ERROR_NAMES.get(c, c)}={n}" for c, n in codes.most_common(5)), "HC-3.11")
    bsl = read_jsonl(first("system.blob_storage_log_7_days_*.jsonl", base))
    if bsl:
        failed = sum(num(r.get("count")) or 0 for r in bsl if num(r.get("failed")))
        if failed:
            ex = next((r.get("example_error") for r in bsl if num(r.get("failed")) and r.get("example_error")), "")
            add("warning", "storage", f"blob_storage_log 中有 {failed} 次失败的对象存储操作", str(ex)[:200], "HC-4.9/P-58")
    # ---- Keeper 自身的视角：可用性、落后于保留日志的成员、快照传输
    # （HC-3.18-3.20、P-61）。只依据成员实际回答的内容——绝不臆测 Keeper
    # 未报告的值——且跨成员比较只在一个 ensemble 内进行：这些文件保存着默认
    # ensemble 和每个辅助 ZooKeeper，而没有 "# ensemble:" 行的 bundle 无法
    # 区分它们。
    kms = read_keeper_members(base)
    if kms:
        groups = {}
        for k in kms:
            # 未知 ensemble：每个成员单独处理
            groups.setdefault(k["ensemble"] or ("?", k["member"]), []).append(k)
        for key, grp in groups.items():
            known = not isinstance(key, tuple)
            label = f"ensemble {key}" if known and key != "default" else "Keeper"
            serving = [k for k in grp if k["state"] == "serving"]
            self_down = [k for k in grp if k["state"] == "not serving"]
            probe = [k for k in grp if k["state"] == "probe failed"]
            if self_down or probe:
                # 这些文件保存的是 THIS server 知道的成员，而不是经过证实的
                # 投票成员集合，因此只有成员自己这样说时才判定 quorum 丢失：
                # 两个或更多成员回答 "not serving"，且没有成员正在服务。
                if known and len(self_down) >= 2 and not serving:
                    add("critical", "keeper",
                        f"{label}：本 bundle 中没有任何成员正在服务——{len(self_down)} 个回答了 '… not currently serving requests'（Keeper 或 ZooKeeper 自己的回复）：没有 leader，经由它的写入会失败，除非 bundle 之外的成员仍构成 quorum："
                        + "，".join(k["member"] for k in self_down),
                        "keeper/<member>.txt 的 srvr/mntr 回复", "HC-3.18/P-57")
                else:
                    add("warning", "keeper",
                        f"{label}：" + "，".join(f"{k['member']} {k['state']}" for k in self_down + probe)
                        + f"（本 bundle 中有 {len(serving)} 个在服务）——不从这些文件判断 quorum：它们保存的是本服务器知道的成员，而 collector 未能完成的探测不能说明投票成员彼此之间的链路",
                        "keeper/<member>.txt——'not serving' 是 Keeper 的回答，'probe failed' 是 collector 的超时或拒绝", "HC-3.18/P-57")
            if known:
                # 只有 leader 自己的 first_log_idx 能说明它保留了什么：
                # follower 独立压缩日志，因此没有观测到 leader 时，不能宣布
                # 任何成员落后于它。
                # 必须恰好一个：选举期间两个成员都可能报告 leader，
                # 此时两者都不能说明“leader”保留了什么。
                leaders = [k for k in grp if k["role"] == "leader" and k["state"] == "serving"]
                leader = leaders[0] if len(leaders) == 1 else None
                ref = leader["kv"].get(("lgif", "first_log_idx")) if leader else None
                if isinstance(ref, (int, float)):
                    for k in grp:
                        last = k["kv"].get(("lgif", "last_log_idx"))
                        if k is leader or not isinstance(last, (int, float)) or last >= ref:
                            continue
                        snap = max((x["kv"].get(("mntr", "zk_latest_snapshot_size")) or 0 for x in grp), default=0)
                        add("critical", "keeper",
                            f"{label}：成员 {k['member']} 落后于 leader 保留的日志（它的 last_log_idx {int(last)} < first_log_idx {int(ref)}，差 {int(ref - last)} 条）——它只能通过完整快照重新加入"
                            + (f"（{human(snap)}）" if snap else "") + "；在正在服务的成员上设置 snapshot_transfer_chunk_size 之前，请保持它停止",
                            "keeper/<member>.txt lgif", "HC-3.19/P-61")
            # 由 leader 发送快照，而任何成员都可能成为 leader：
            # 用 ensemble 中最大的快照对比每个成员自己的 chunk 设置——
            # 绝不跨 ensemble 比较。
            snap = max((k["kv"].get(("mntr", "zk_latest_snapshot_size")) or 0 for k in grp), default=0)
            chunk0 = [k["member"] for k in grp if k["kv"].get(("conf", "snapshot_transfer_chunk_size")) == 0]
            if chunk0 and snap > 2**30:
                add("critical" if snap > INT32_MAX else "warning", "keeper",
                    f"{label}：快照为 {human(snap)}，而 {', '.join(chunk0)} 上的 snapshot_transfer_chunk_size = 0：快照会作为一个对象发送给落后的成员，超过 2 GiB 时其大小会令 32 位求和溢出，传输失败（std::bad_alloc）——逐个 leader 发生，最终使 ensemble 失去 leader"
                    + ("" if snap > INT32_MAX else "——尚未发生，但仍在增长")
                    + "。请在成员落后之前，在每个成员上设置 snapshot_transfer_chunk_size（例如 100000000）和 nuraft_snapshot_sync_ctx_timeout_ms（例如 900000）",
                    "keeper/<member>.txt mntr zk_latest_snapshot_size、conf snapshot_transfer_chunk_size", "HC-3.20/P-61")
        # 快照格式，按每个成员对照 IT 自己的版本：V7 把 seq_num
        # 从 int32 扩展到 int64；26.7 及之前的默认值是 6，26.8 起为 8。
        fmt_groups = {}  # (severity, why, format) -> members：每个结论一个 finding
        for k in kms:
            v = k["kv"].get(("conf", "write_snapshot_version"))
            if not isinstance(v, (int, float)) or v >= 7:
                continue
            ver = k["version"]
            if ver and ver >= (26, 8):
                sev, why = "warning", f"被固定在 {ver[0]}.{ver[1]} 上的默认值 8 之下"
            elif ver:
                sev, why = "info", f"{ver[0]}.{ver[1]} 上的默认值（26.8 写入 8）"
            else:
                sev, why = "info", "未报告 Keeper 版本，因此无法判断它是否为默认值"
            fmt_groups.setdefault((sev, why, int(v)), []).append(k["member"])
        for (sev, why, v), names in fmt_groups.items():
            add(sev, "keeper",
                f"Keeper 成员 {', '.join(names)} 使用快照格式 {v}（write_snapshot_version < 7）：其中的顺序节点计数器是 32 位的，计数器超过 2^31 后就会停止写入快照（'overflows int32, upgrade to snapshot version >= V7'）——{why}；只有在每个成员都运行能读取新格式的版本之后，才应提高该值",
                "keeper/<member>.txt conf write_snapshot_version、srvr version", "HC-3.20")
        out["keeper_members"] = [{"member": k["member"], "ensemble": k["ensemble"], "state": k["state"], "role": k["role"]} for k in kms]
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
            add("warning", "keeper", "zookeeper_log：带丢失错误的失败 Keeper 请求——" + "，".join(f"{e}={n}" for e, n in bad.items()),
                "小时：" + "，".join(sorted(loss_hours)[:8]), "HC-3.10")
    aggregated_zk_checks(base, add)
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
                add("warning", "logs", f"Error 级别日志量在 {peak_h} 这一小时达到峰值 {peak_n} 行（中位小时为 {quiet}）",
                    "top classes：" + "，".join(f"{c}={n}" for c, n in cls.most_common(4)), "HC-11.5")

    # ---- execution_log.txt：哪些 collector 运行了、失败了，以及耗时
    xl_path = os.path.join(base, "execution_log.txt")
    if os.path.exists(xl_path):
        rows = []
        # 头部回答“这是哪个节点，它是否刚重启过”——这两个问题曾让一次
        # fetch 延迟升级不得不两次靠线程 ID 和 uptime 手工确认。
        # 在 Summary 之前是 key:   value 行。
        run = {}
        with open(xl_path, encoding="utf-8", errors="replace") as fh:
            for line in fh:
                if line.startswith("Summary"):
                    break
                m = re.match(r"^([a-z][a-z-]*):\s+(.*\S)\s*$", line)
                if m and m.group(1) not in ("started", "finished"):
                    run[m.group(1)] = m.group(2)
        if run:
            out["run"] = run
            fanned = out["mode"] == "cloud"
            if run.get("node"):
                if fanned:
                    out["notes"].append(f"身份探测由节点 {run['node']} 应答（通过 {run.get('target', '?')} 采集）；按 replica 的 system 表"
                                        "（query_log、part_log、errors、metric_log、text_log 等）会分发到每个 replica，共享表（parts、tables、columns、"
                                        "databases、replicas、replication_queue、mutations、detached_parts）只从一个 replica 读取——host facts、配置和"
                                        "日志文件（如果存在）属于 collector 所在机器：仅当头部没有 mixed-host 警告时，才属于该节点")
                else:
                    out["notes"].append(f"本 bundle 描述的是节点 {run['node']}（通过 {run.get('target', '?')} 采集）")
            up = re.match(r"^(\d+)\s*s\b", run.get("uptime-seconds", ""))
            if up and int(up.group(1)) < 24 * 3600:
                who = f"应答身份探测的 replica（{run.get('node', '?')}）发生过重启" if fanned else "服务器发生过重启"
                scope = ("它自己的 parts、pools 和 Keeper 会话是重启后的状态；分发到其他 replica 的表中的行不受影响"
                         if fanned else "parts、replicas、metrics、zookeeper_connection 和 pools 描述的是重启后的节点；重启之前的 part_log / metric_log / text_log 仍在窗口内")
                add("info", "coverage", f"{who}，采集前已运行 {run.get('uptime', up.group(1) + ' s')}——{scope}",
                    "重启只能暂时清空 parts 传播积压；在一次重启之后采集的节点看起来会比整个集群更健康", "HC-0")
            # 采集了多少内容。essential collection（服务器状态不佳，或使用了
            # -collect essential）只有 1 天窗口，并跳过了按表的 collector：
            # 这些文件按设计就是缺失的，而采用 essential 的原因本身就是
            # 关于服务器的一个 finding。
            collect = run.get("collect", "")
            if collect.startswith("essential") or "switched to essential" in collect:
                add("warning", "coverage", f"essential collection（精简采集）——{collect}",
                    "execution_log.txt 头部 `collect:`；被跳过的 collector 以 status skipped 列出——它们缺失的文件不能说明健康状况，"
                    "且窗口只有 1 天；等节点恢复健康后，请再请求一次 `-collect full` 采集，以获取 parts、tables、detached parts 和 3–7 天的历史", "HC-0")
            elif collect:
                out["notes"].append(f"采集：{collect}")
            if run.get("warnings"):
                # collector 会给每行加前缀："Warning:" 表示会削弱 bundle 的问题，
                # "Note:" 表示读者仍需知道的预期情况。负载均衡端点
                # 在自管理集群上是 warning，而在云服务上是 note——后者中它是
                # 唯一的连接方式，采集也是为此设计的——因此这里由前缀决定
                # 严重级别，而不是仅仅因为存在该行。
                for line in [w.strip() for w in str(run["warnings"]).split("|")]:
                    if not line:
                        continue
                    if line.startswith("Note:"):
                        add("info", "coverage", f"collector 在运行时记录：{line}", "execution_log.txt 头部", "HC-0")
                    else:
                        add("warning", "coverage", f"collector 在运行时警告：{line}", "execution_log.txt 头部", "HC-0")
        with open(xl_path, encoding="utf-8", errors="replace") as fh:
            for line in fh:
                if not line.startswith("| ") or line.startswith("| # |") or line.startswith("|---"):
                    continue
                cells = [c.strip() for c in line.strip().strip("|").split("|")]
                if len(cells) < 10:
                    continue
                rows.append({"stage": cells[1], "name": cells[2], "source": cells[3], "status": cells[4],
                             "ms": num(cells[5]) or 0, "bytes": num(cells[6]) or 0, "rows": cells[7], "note": cells[8], "error": cells[9]})
        coll = [r for r in rows if r["stage"] == "collector"]
        if coll:
            ok = [r for r in coll if r["status"] == "ok"]
            failed = [r for r in coll if r["status"] == "failed"]
            slow = sorted(coll, key=lambda r: -r["ms"])[:3]
            out["execution_log"] = {"collectors": len(coll), "ok": len(ok), "failed": [f"{r['name']} ({r['source']}): {r['error'][:90]}" for r in failed],
                                    "slowest": [f"{r['name']} {r['ms'] / 1000:.1f} s" for r in slow], "total_s": round(sum(r["ms"] for r in coll) / 1000, 1)}
            timeouts = [r for r in failed if "Code: 159" in r["error"] or "TIMEOUT_EXCEEDED" in r["error"]]
            if timeouts:
                add("warning", "coverage", f"{len(timeouts)} 个 collector 触发了工具自身的 -query-timeout（code 159）——服务器对这些 system 表来说太慢了；它们的文件缺失",
                    "，".join(r["name"] for r in timeouts), "HC-0")
            grants = [r for r in failed if "Code: 497" in r["error"] or "ACCESS_DENIED" in r["error"]]
            if grants:
                add("warning", "coverage", f"{len(grants)} 个 collector 因授权（497）失败——bundle 的范围被限制为 collector 用户可读的内容",
                    "，".join(r["name"] for r in grants), "HC-0")
            absent = [r for r in failed if r not in timeouts and r not in grants
                      and ("Code: 60" in r["error"] or "UNKNOWN_TABLE" in r["error"] or "Code: 139" in r["error"] or "NO_ELEMENTS_IN_CONFIG" in r["error"] or "Code: 81" in r["error"])]
            if absent:
                add("info", "coverage", f"{len(absent)} 个 collector 因该服务器上不存在对应的表或配置而没有运行——它们的文件是缺失，而不是为空",
                    "；".join(f"{r['name']}: {r['error'][:70]}" for r in absent[:6]), "HC-0")
            other = [r for r in failed if r not in timeouts and r not in grants and r not in absent]
            if other:
                add("warning", "coverage", f"{len(other)} 个 collector 因其他原因失败（语法、未知列、网络、写入错误）——证据是缺失了，而不是本来就不存在",
                    "；".join(f"{r['name']}: {r['error'][:90]}" for r in other[:6]), "HC-0")
            heavy = [r for r in coll if r["ms"] >= 60000]
            if heavy:
                add("info", "coverage", f"{len(heavy)} 个 collector 耗时超过一分钟——可考虑在这台服务器上使用更短的窗口",
                    "，".join(f"{r['name']} {r['ms'] / 1000:.0f} s" for r in heavy), "HC-0")

    # ---- 是 SharedMergeTree 集群中的一个节点吗？（is_smt 已在上面计算）
    if is_smt and out["mode"] == "onprem":
        cl = read_jsonl(first("system.clusters_*.jsonl", base))
        n = len({(r.get("host_name"), r.get("port")) for r in cl if r.get("cluster") == "default"}) or "N"
        out["notes"].append(f"SharedMergeTree 集群以 onprem 模式采集：本 bundle 只描述 {n} 个 replica 中的一个（parts、errors、part_log、query_log、text_log 都是按 replica 的）——建议改用 -mode cloud 获取集群视图")

    # ---- dictionary（字典）
    dicts = read_jsonl(first("system.dictionaries_*.jsonl", base))
    bad = [d for d in dicts if str(d.get("status", "")).startswith("FAILED") or d.get("last_exception")]
    if bad:
        add("warning", "dictionaries", f"{len(bad)} 个 dictionary 加载失败",
            "；".join(f"{d.get('database')}.{d.get('name')} {d.get('status')}" for d in bad[:5]), "HC-9.1")

    # ---- metric_log 内存 / pools
    # 先取 pool 大小（下面的集群级检查和其后的按 host 文件都会用到）：
    # 23.3 起使用 system.server_settings；只有 BELOW 23.3 才使用 system.settings——
    # 在更新的服务器上，它列出的 background_*_pool_size 仍是旧的会话默认值
    # （8 / 128），而不是服务器的真实大小，因此没有 server_settings 文件的
    # bundle 宁可不给出大小，也不给出错误的大小。
    sizes = {}
    vm = re.match(r"^(\d+)\.(\d+)", str(out.get("version") or ""))
    pre_233 = bool(vm) and (int(vm.group(1)), int(vm.group(2))) < (23, 3)
    size_files = ["system.server_settings_*.jsonl"] + (["system.settings_*.jsonl"] if pre_233 else [])
    for f in size_files:
        for r in read_jsonl(first(f, base)) or []:
            n = r.get("name")
            if n in ("background_fetches_pool_size", "background_schedule_pool_size", "background_pool_size",
                     "background_common_pool_size") and n not in sizes:
                sizes[n] = num(r.get("value"))
    pools = {}
    ml = read_jsonl(first("system.metric_log_7_days_*.jsonl", base))
    if ml:
        mem = [num(r.get("avg_memory_tracking_bytes")) or 0 for r in ml]
        pool = [num(r.get("max_merge_pool_tasks")) or 0 for r in ml]
        zk = sum(num(r.get("zk_hw_exceptions")) or 0 for r in ml)
        out["metric_log"] = {"hours": len(ml), "max_avg_memory_tracking": human(max(mem)), "max_merge_pool_tasks": max(pool),
                             "hours_pool_at_max": sum(1 for p in pool if p == max(pool)) if max(pool) else 0, "zk_hw_exceptions_total": zk}

        # ---- 后台 pool 饱和（HC-2.13 / HC-2.14）。大小来自
        # system.server_settings（23.3+）或 system.settings（更旧版本）；指标是
        # pool 中任务数的每小时平均值。按小时而非按窗口：过去 24 小时内有 3 个
        # 饱和小时就是一个 finding。
        # “过去 24 小时”以采集时间（运行开始时间，取自文件夹名）为锚点，
        # 与实时告警的 now() - 24 h 一致——而不是以最新样本为锚点，否则几天前
        # 就停止更新的 metric_log 会把最新样本放在过去，让过期桶变成当前 finding。
        last_hour = max((r.get("time") or "" for r in ml), default="")
        anchor = run_ts or (parse_dt(last_hour) if last_hour else None)
        recent = [r for r in ml if r.get("time") and parse_dt(r["time"]) and anchor and
                  0 <= (anchor - parse_dt(r["time"])).total_seconds() <= 25 * 3600] if anchor else []
        if ml and anchor and not recent and last_hour and parse_dt(last_hour):
            gap_h = (anchor - parse_dt(last_hour)).total_seconds() / 3600
            out["notes"].append(f"metric_log_7_days 在采集前 {gap_h:.0f} 小时就结束了——后台 pool 检查（HC-2.7/2.13/2.14）没有可判断的近期小时，已跳过")
        # 每个 pool 的失败方式不同，因此后果和模式也各不相同。
        IMPACT = {
            "fetch": ("找不到空闲槽位的 fetch 会被丢弃，并在下一轮被重新选中，且没有提示——在 SharedMergeTree 上这就是 parts 传播延迟；"
                      "请提高 background_fetches_pool_size（IncreaseOnly：配置 reload 即可在线生效，无需重启；绝不要假设是 16——读取实时大小），"
                      "或减少 insert/part/表数量", "P-59"),
            "schedule": ("周期任务不会丢失但会延迟——parts-set 更新、fetch 调度轮次、leader 选举和清理都在等待槽位，因此新 part "
                         "会滞后处理（schedule pool 被占满会让 fetch pool 空闲）；请提高 background_schedule_pool_size（IncreaseOnly：配置 "
                         "reload 即可在线生效——只有按类型的任务上限要等下一次重启，因此请在追赶完成后计划滚动重启），提高 "
                         "shared_merge_tree_leader_update_period_seconds，或删除空表", "P-59"),
            "merge": ("merge 排队等待，part 持续累积——即 too-many-parts 的堆积过程；请提高 background_pool_size（配置 reload 时生效），或降低 "
                      "insert 速率 / 批量 insert", "P-01"),
        }
        for label, col, setting, pct, hc in (("fetch", "avg_fetch_pool_tasks", "background_fetches_pool_size", 0.9, "HC-2.13"),
                                             ("schedule", "avg_schedule_pool_tasks", "background_schedule_pool_size", 0.95, "HC-2.14"),
                                             ("merge", "avg_merge_pool_tasks", "background_pool_size", 0.9, "HC-2.7")):
            # Cloud 文件携带各 replica 每小时平均值的最大值（v0.7+）：
            # 在空闲的兄弟节点中，一个 16/16 的节点不能因为平均而被压到阈值以下。
            rcol = "max_replica_" + col
            vals = [num(r.get(rcol) if r.get(rcol) is not None else r.get(col)) for r in recent if r.get(rcol) is not None or r.get(col) is not None]
            if not vals:
                continue
            size = sizes.get(setting)
            info = {"max_avg": round(max(vals), 1), "size": size}
            if size:
                sat = sum(1 for v in vals if v >= pct * size)
                info["hours_saturated_24h"] = sat
                if sat >= 3:
                    # bundle 的 settings 文件只从一个 replica 读取（cloud collector
                    # 不会分发 settings）；告警会在集群上按每个 replica 自己的
                    # 大小进行比较。
                    scope = "（大小来自应答 settings 查询的 replica；告警按每个 replica 比较）" if out["mode"] == "cloud" else ""
                    impact, pattern = IMPACT[label]
                    add("warning", "merges" if label == "merge" else "replication",
                        f"后台 {label} pool 饱和：在采集前的最后 {len(vals)} 个小时中有 {sat} 个小时的每小时平均值 ≥ {setting} = {size} 的 {int(pct * 100)}%{scope}（峰值平均 {info['max_avg']}）",
                        impact, f"{hc}/{pattern}")
            pools[label] = info
        if pools:
            out["pools"] = pools
        if zk and not any(f["check"].startswith("HC-3.8") for f in findings):
            add("warning", "keeper", f"{len(ml)} 小时内共 {zk} 次 ZooKeeper/Keeper 硬件异常", "metric_log.zk_hw_exceptions", "HC-3.5")

    # ---- 按 host 的 pools、实时 pool 大小、SharedMergeTree fetch 计数器、
    # leader 选举和 Keeper 延迟（system.metric_log_by_host_3_days，
    # v0.7 起的 cloud 采集）。metric_log_7_days 会把 replica 折叠在一起；这个
    # 文件说明 WHICH host、在 WHAT 实时大小下，以及 fetch executor 是否
    # 在丢弃工作。在缺少计数器的版本上，正则选出的列可能不存在——
    # 下面每次读取都能容忍键缺失。
    bh = read_jsonl(first("system.metric_log_by_host_3_days_*.jsonl", base))
    smt_fetch = {}
    bh_saturated = {}
    if bh:
        SIZE_COL = {"fetch": "max(CurrentMetric_BackgroundFetchesPoolSize)",
                    "schedule": "max(CurrentMetric_BackgroundSchedulePoolSize)",
                    "merge": "max(CurrentMetric_BackgroundMergesAndMutationsPoolSize)"}
        TASK_COL = {"fetch": "avg_fetch_pool_tasks", "schedule": "avg_schedule_pool_tasks", "merge": "avg_merge_pool_tasks"}
        SETTING = {"fetch": "background_fetches_pool_size", "schedule": "background_schedule_pool_size", "merge": "background_pool_size"}
        PCT = {"fetch": 0.9, "schedule": 0.95, "merge": 0.9}
        by_host = {}
        for r in bh:
            by_host.setdefault(r.get("hostname") or "?", []).append(r)
        for rows in by_host.values():
            rows.sort(key=lambda r: r.get("time") or "")
        hosts = sorted(by_host)
        last_bh = max((r.get("time") or "" for r in bh), default="")
        anchor_bh = run_ts or (parse_dt(last_bh) if last_bh else None)

        def recent_rows(rows):
            return [r for r in rows if r.get("time") and parse_dt(r["time"]) and anchor_bh
                    and 0 <= (anchor_bh - parse_dt(r["time"])).total_seconds() <= 25 * 3600]

        summary = {"hosts": len(hosts), "saturated": {}, "size_steps": []}
        for label in ("fetch", "schedule", "merge"):
            sat_hosts = {}
            for h, rows in by_host.items():
                rec = recent_rows(rows)
                hours, size_sat = 0, None
                for r in rec:
                    size = num(r.get(SIZE_COL[label])) or sizes.get(SETTING[label])
                    tasks = num(r.get(TASK_COL[label]))
                    if size and tasks is not None and tasks >= PCT[label] * size:
                        hours += 1
                        size_sat = size  # pool 当时被顶到的那个大小（窗口后期的大小提升不能掩盖它）
                if hours >= 3:
                    sat_hosts[h] = (hours, size_sat)
                # 实时大小的阶跃是该 host 上的一次设置 TRANSITION。
                # metric_log 跨越重启，因此增大可能与在线 reload 或带新配置的
                # 重启一致；减小只可能是重启（或 host 被替换）——IncreaseOnly
                # 的 pool 无法在线缩小。finding 会说明是哪种，绝不直接说 "reload"。
                prev = None
                for r in rows:
                    sz = num(r.get(SIZE_COL[label]))
                    if sz is None:
                        continue
                    if prev is not None and sz != prev:
                        summary["size_steps"].append({"pool": label, "host": h, "hour": r.get("time"), "from": prev, "to": sz})
                    prev = sz
            if sat_hosts:
                summary["saturated"][label] = len(sat_hosts)
                bh_saturated[label] = sat_hosts
                shown = "，".join(f"{h} 在大小 {v[1]} 下饱和 {v[0]} h" for h, v in sorted(sat_hosts.items(), key=lambda kv: -kv[1][0])[:5])
                add("warning", "merges" if label == "merge" else "replication",
                    f"后台 {label} pool 在 {len(hosts)} 个 host 中的 {len(sat_hosts)} 个上饱和：过去 24 小时中有 ≥ 3 个小时的每小时平均值 ≥ 该 host 实时 pool 大小的 {int(PCT[label] * 100)}%",
                    f"system.metric_log_by_host_3_days——{shown}" + (" …" if len(sat_hosts) > 5 else ""),
                    f"HC-2.15/{'P-01' if label == 'merge' else 'P-59'}")
        if summary["size_steps"]:
            by_change = Counter((st["pool"], st["from"], st["to"]) for st in summary["size_steps"])
            for (pool, a, b), n in by_change.most_common(3):
                first_hour = min(st["hour"] for st in summary["size_steps"] if (st["pool"], st["from"], st["to"]) == (pool, a, b))
                if (b or 0) > (a or 0):
                    how = ("增大——与在线配置 reload 或带新配置的重启一致（metric_log 跨越重启）；"
                           "如果 pool 在此之前已被顶满，那么随后的小时是追赶过程，而不是基线")
                else:
                    how = ("减小——这些 pool 是 IncreaseOnly，因此这只能是带更小值的重启或 host 被替换；"
                           "应把前后的小时看作一次重启，而不是调优步骤")
                add("info", "merges" if pool == "merge" else "replication",
                    f"{pool} pool 大小在 {n} 个 host 上发生 {a} → {b} 的变更（首次在 {first_hour}）：{how}",
                    "system.metric_log_by_host_3_days max(CurrentMetric_Background*PoolSize)", "HC-2.15/P-59")
        # Fetch executor：被选中待 fetch 的 part 与实际尝试的 fetch。
        # 该 executor 没有队列——找不到空闲槽位的 part 会在下一轮被重新
        # 选中——因此 attempted ≪ selected 就是丢弃。
        SEL = ("sum(ProfileEvent_SharedMergeTreeSelectPartsForRendezvousFetchParts)",
               "sum(ProfileEvent_SharedMergeTreeSelectPartsForCoordinatedFetchParts)")
        ATT = "sum(ProfileEvent_SharedMergeTreeDataPartsFetchAttempt)"
        ratios, att_per_h = {}, {}
        for h, rows in by_host.items():
            sel = sum((num(r.get(c)) or 0) for r in rows for c in SEL)
            # 列缺失 = 该版本不存在这个计数器（由正则选出）；存在但为 0 =
            # 选中了 part 但 NOTHING 被尝试——最强的丢弃信号，比值 0。
            att_present = any(ATT in r for r in rows)
            att = sum((num(r.get(ATT)) or 0) for r in rows)
            if sel > 0 and att_present:
                ratios[h] = att / sel
            rec = [v for v in ((num(r.get(ATT)) or 0) for r in recent_rows(rows)) if v > 0]
            if len(rec) >= 6:
                mean = sum(rec) / len(rec)
                att_per_h[h] = (round(mean), round(100 * (max(rec) - min(rec)) / mean) if mean else 0)
        if ratios:
            worst_h = min(ratios, key=ratios.get)
            smt_fetch = {"min_ratio": round(ratios[worst_h], 2), "host": worst_h,
                         "hosts_below_half": sum(1 for v in ratios.values() if v < 0.5)}
            if smt_fetch["hosts_below_half"]:
                add("warning", "replication",
                    f"fetch executor 在 {len(ratios)} 个 host 中的 {smt_fetch['hosts_below_half']} 个上丢弃工作：尝试的 fetch / 被选中待 fetch 的 part = 最差 {smt_fetch['min_ratio']}（{worst_h}，3 天）",
                    "system.metric_log_by_host_3_days sum(ProfileEvent_SharedMergeTreeDataPartsFetchAttempt) 对比 sum(ProfileEvent_SharedMergeTreeSelectPartsFor*FetchParts)——没有队列：找不到空闲槽位的 part 会在下一轮被重新选中",
                    "HC-2.15/P-59")
        if att_per_h and "fetch" in bh_saturated:
            flat = [h for h, (mean, spread) in att_per_h.items() if spread <= 20 and h in bh_saturated["fetch"]]
            if flat:
                means = [att_per_h[h][0] for h in flat]
                rng = f"{min(means)}" if min(means) == max(means) else f"{min(means)}–{max(means)}"
                out["notes"].append(f"{len(flat)} 个饱和 host 的 fetch 吞吐稳定在约 {rng} 次 fetch 尝试/小时（过去 24 小时波动 ≤ 20%）——说明 pool 正在满负荷工作，而不是停滞；上限 ≈ pool 大小 × 3600 / 每次 fetch 秒数，因此增加槽位可以提高上限")
        # Leader 选举：每个周期、每张表一个 schedule-pool 任务。
        ELE = ("sum(ProfileEvent_SharedMergeTreeVirtualPartsUpdatesLeaderSuccessfulElection)",
               "sum(ProfileEvent_SharedMergeTreeVirtualPartsUpdatesLeaderFailedElection)")
        el_rates = {}
        for h, rows in by_host.items():
            rec = recent_rows(rows)
            tot = sum((num(r.get(c)) or 0) for r in rec for c in ELE)
            if rec and tot:
                el_rates[h] = tot / (3600 * len(rec))
        if el_rates:
            peak_h = max(el_rates, key=el_rates.get)
            summary["elections_per_s_max"] = round(el_rates[peak_h], 1)
            if el_rates[peak_h] >= 100:
                add("info", "replication",
                    f"SharedMergeTree leader 选举在 {peak_h} 上约为 {round(el_rates[peak_h])}/s（过去 24 小时）——每次选举都是一个 schedule-pool 任务；该速率等于 Shared* 表数量 / shared_merge_tree_leader_update_period_seconds（默认 30 s，在数万张表的规模上验证过 300）",
                    "system.metric_log_by_host_3_days sum(ProfileEvent_SharedMergeTreeVirtualPartsUpdatesLeader*Election)", "HC-2.16/P-59")
        # 按 host 对比 Keeper 延迟与整个集群的中位数。
        lat = {}
        for h, rows in by_host.items():
            tx = sum((num(r.get("zk_transactions")) or 0) for r in rows)
            wait = sum((num(r.get("zk_wait_us")) or 0) for r in rows)
            if tx > 0:
                lat[h] = wait / tx / 1000.0
        if len(lat) >= 3:
            med = sorted(lat.values())[len(lat) // 2]
            slow = {h: v for h, v in lat.items() if v > 2 * med and v > 5}
            if slow:
                add("warning", "keeper",
                    f"{len(slow)} 个 host 上的 Keeper 请求延迟超过整个集群中位数的 2 倍（{med:.1f} ms/tx）："
                    + "，".join(f"{h} {v:.0f} ms" for h, v in sorted(slow.items(), key=lambda kv: -kv[1])[:5]),
                    "system.metric_log_by_host_3_days zk_wait_us / zk_transactions——问题在该 replica 的网络或其会话所在的 Keeper 成员（zookeeper_connection.host），而不是 Keeper 全局事故",
                    "HC-3.17")
        out["pools_by_host"] = summary

    # ---- 空的和极小的 Shared* 表（system.tables.total_rows，v0.6 起采集）。
    # 每一张仍会运行其周期性 schedule-pool 任务，并在每个周期进行一次 leader
    # 选举，因此它们是没有数据的负载。
    known = [r for r in shared_tables if num(r.get("total_rows")) is not None]
    if known:
        empty = sum(1 for r in known if num(r.get("total_rows")) == 0)
        tiny = sum(1 for r in known if 0 < num(r.get("total_rows")) < 100)
        out["shared_tables"] = {"total": len(shared_tables), "empty": empty, "under_100_rows": tiny}
        if len(known) >= 100 and (empty + tiny) / len(known) > 0.2:
            add("info", "schema",
                f"{len(known)} 张 Shared*MergeTree 表中有 {empty} 张为空，另有 {tiny} 张少于 100 行（{round(100 * (empty + tiny) / len(known))}%）——每一张仍会运行 schedule-pool 任务和 leader 选举；删除或合并它们可以降低占满 schedule pool 的负载",
                "system.tables total_rows", "HC-2.16/P-59")

    # ---- 服务器级 MergeTree 设置（system.merge_tree_settings，v0.7）
    mts = read_jsonl(first("system.merge_tree_settings_*.jsonl", base))
    if mts:
        out["merge_tree_settings_changed"] = [{"name": r.get("name"), "value": r.get("value")} for r in mts if num(r.get("changed")) == 1]
        if shared_tables:
            byname = {r.get("name"): r for r in mts}
            batch = byname.get("shared_merge_tree_parts_load_batch_size", {})
            period = byname.get("shared_merge_tree_leader_update_period_seconds", {})
            per = num(period.get("value"))
            out["smt_settings"] = {"parts_load_batch_size": batch.get("value"), "leader_update_period_seconds": period.get("value"),
                                   "estimated_elections_per_s": round(len(shared_tables) / per, 1) if per else None}
            if len(shared_tables) >= 5000 and per and num(batch.get("changed")) == 0 and num(period.get("changed")) == 0:
                add("info", "settings",
                    f"该 replica 上有 {len(shared_tables)} 张 Shared* 表，而 SharedMergeTree 设置仍为默认值：shared_merge_tree_parts_load_batch_size = {batch.get('value')}（每轮调度一张表的 fetch），shared_merge_tree_leader_update_period_seconds = {period.get('value')}（每个 replica 约 {round(len(shared_tables) / per)} 次 leader 选举/s）",
                    "system.merge_tree_settings——64 和 300 已在此规模上验证；两者都是 MergeTree 设置（服务器 <merge_tree> 块，或按表 ALTER … MODIFY SETTING）", "HC-2.16/P-59")

    # ---- P-59 结论：pool 连续数小时顶在其大小上限 AND part 明显落后
    sat = sorted({l for l, i in pools.items() if (i.get("hours_saturated_24h") or 0) >= 3 and l != "merge"}
                 | {l for l in bh_saturated if l != "merge"})
    lag = out.get("smt_lag")
    if is_smt and sat and (lag or smt_fetch.get("hosts_below_half")):
        where = (f"{lag['rows']} 行表记录在 {lag['hosts']} 个 host 上落后（最差 {lag['worst']}）" if lag
                 else f"尝试的 fetch / 被选中的 part = {smt_fetch['min_ratio']}")
        # 容量：给出本 bundle 显示的大小，并把已验证的步进仅作为
        # 相对默认值的参考点引用。
        cur = {l: (pools.get(l) or {}).get("size") for l in ("fetch", "schedule")}
        cur_txt = "，".join(f"{l} {cur[l]}" for l in ("fetch", "schedule") if cur.get(l))
        capacity = ("提高 background_fetches_pool_size 和 background_schedule_pool_size（两者都是 IncreaseOnly——配置 reload 即可在线生效"
                    + (f"；当前 {cur_txt}" if cur_txt else "")
                    + "；从默认值看，16 → 128 和 512 → 1024 曾在数万张表的规模上于一小时内清掉积压——请按 fetch 尝试数/小时和表数量来确定大小，而不是照搬这些数字），"
                    "一次改几个 replica，同时观察对象存储延迟和 Keeper ms/tx，然后计划滚动重启，让 schedule pool 的按类型上限采用新大小")
        # 需求：精确的 32 → 64 / 30 → 300 变更仅在两个 SMT 设置
        # 都仍为默认值时适用；否则报告当前已设置的值，把方向交给容量部分。
        ss = out.get("smt_settings") or {}
        b_val, p_val = ss.get("parts_load_batch_size"), ss.get("leader_update_period_seconds")
        if b_val is not None and p_val is not None and str(b_val) == "32" and str(p_val) == "30":
            demand = "把 shared_merge_tree_parts_load_batch_size 从 32 提高到 64，把 shared_merge_tree_leader_update_period_seconds 从 30 提高到 300（这里两者都是默认值）"
        elif b_val is not None or p_val is not None:
            demand = (f"这里已设置 shared_merge_tree_parts_load_batch_size = {b_val}、shared_merge_tree_leader_update_period_seconds = {p_val}——"
                      "请结合 fetch 尝试数/小时和选举次数/s 重新审视它们（更大的批次每轮 fetch 更多，更长的周期降低 schedule-pool 负载），而不是套用固定值")
        else:
            demand = ("先检查 shared_merge_tree_parts_load_batch_size 和 shared_merge_tree_leader_update_period_seconds（默认值 32 和 30 s；64 和 300 已在数万张表的规模上验证；"
                      "本 bundle 中没有 system.merge_tree_settings）")
        add("warning", "replication",
            f"与 P-59 一致——后台 pool 饱和导致 SharedMergeTree parts 传播延迟：{' 和 '.join(sat)} pool 连续数小时顶在其大小上限，同时 {where}",
            f"补救——容量：{capacity}。需求：{demand}；然后减少插入次数并增大批次，同时删除空表。在此期间，对必须保持一致的读取使用 select_sequential_consistency = 1——重启只能批量加载一次 parts。"
            "之后用相同的文件验证：每个 host 的实时 pool 大小都已增加，fetch 尝试先激增后回落，每个 host 的 inserts_in_queue → 0",
            "P-59")

    # ---- host facts（主机信息）
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
            add("warning", "host", "transparent_hugepages = always（ClickHouse 启动时会警告）", "host_info.json", "HC-10.1")
        if str(t.get("vm_overcommit_memory", "")).strip() == "2":
            add("warning", "host", "vm.overcommit_memory = 2（ClickHouse 启动时会警告）", "host_info.json", "HC-10.2")
        if memi.get("available") and (num(memi.get("available_bytes")) or 0) and num(memi.get("available_bytes")) < 2 * GIB:
            add("warning", "host", f"可用内存 {human(memi.get('available_bytes'))} < 2 GiB", "host_info.json", "HC-10.3")
        cg = num(t.get("cgroup_memory_limit_bytes"))
        tot = num(memi.get("total_bytes"))
        if cg and tot and cg < tot * 0.9:
            add("info", "host", f"cgroup 内存限制 {human(cg)} 低于宿主机 RAM {human(tot)}——cgroup 才是真正的上限", "host_info.json", "HC-10.4")
        nofile = num(t.get("clickhouse_open_files_soft"))
        if nofile and nofile < 500000:
            add("warning", "host", f"open files 软限制 {nofile}（< 500000）", "host_info.json", "HC-10.5")
        swap_used = (num(memi.get("swap_total_bytes")) or 0) - (num(memi.get("swap_free_bytes")) or 0)
        if swap_used > 0:
            add("info", "host", f"正在使用 swap：{human(swap_used)}", "host_info.json", "HC-5.6")
        cpus = num(hi.get("cpu", {}).get("logical_cpus"))
        load = hi.get("cpu", {}).get("load_avg_1_5_15") or []
        if cpus and load and num(load[0]) and num(load[0]) > 2 * cpus:
            add("warning", "host", f"{cpus} 个 CPU 上的 load average 为 {load[0]}", "host_info.json", "HC-5.5")
        if not hi.get("os", {}).get("available") or not hi.get("cpu", {}).get("available"):
            add("info", "host", "host_info.json 已降级（部分 section 不可用）——很可能是在服务器之外采集的；请忽略 host facts", str(hi.get("notes")), "HC-0")
    elif out["mode"] != "gov":
        out["notes"].append("host_info.json 缺失（cloud 默认行为，或使用了 -host-info off）")

    # ---- 已变更的设置（2026 年 9 月起采集；server_settings 需要 >= 23.4）
    for fname, key in (("system.settings_*.jsonl", "settings_changed"), ("system.server_settings_*.jsonl", "server_settings_changed"),
                       ("system.merge_tree_settings_*.jsonl", "merge_tree_settings_changed")):
        rows = read_jsonl(first(fname, base))
        if rows:
            changed = [r for r in rows if num(r.get("changed")) == 1]
            out[key] = [{"name": r.get("name"), "value": r.get("value"), "default": r.get("default")} for r in changed]
            watch = {"max_server_memory_usage_to_ram_ratio", "max_memory_usage", "max_concurrent_queries", "background_pool_size",
                     "background_fetches_pool_size", "background_schedule_pool_size", "number_of_free_entries_in_pool_to_execute_mutation", "compatibility",
                     "wait_for_async_insert", "parallel_distributed_insert_select", "mark_cache_size", "uncompressed_cache_size",
                     "parts_to_throw_insert", "parts_to_delay_insert", "shared_merge_tree_parts_load_batch_size", "shared_merge_tree_leader_update_period_seconds"}
            hits = [f"{r.get('name')}={r.get('value')}" for r in changed if r.get("name") in watch]
            if hits:
                kind = "server " if "server" in key else ("MergeTree " if "merge_tree" in key else "")
                add("info", "settings", f"{len(changed)} 项已变更的 {kind}设置；值得关注：" + "，".join(hits[:8]), fname, "HC-10.8")

    # ---- query_analysis/（仅在 --query-id / --normalized-query-hash 时使用）
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
            qa["shape"] = ("分布式语句的 worker 子查询" if qa["is_initial_query"] == 0 else
                           "table function 查询（没有 MergeTree part）" if table_function else "MergeTree 读取")
            if qa["is_initial_query"] == 0:
                add("info", "query-analysis", "被分析的 hash 是一个 WORKER 子查询（is_initial_query = 0）：它的各次执行是同一条分布式语句的不同 shard——请比较各 replica 的占比，而不是慢/快的运行；可通过 text_log_full 中的 initial_query_id 找到父查询", f"tables={tables}", "step 3")
            if table_function:
                qa["empty_not_applicable"] += [f for f in qa["files"] if f.startswith(("text_log_parts", "tables_for_query")) and qa["files"][f] == 0]
        pe = read_jsonl(first("profile_events_*.jsonl", qa_dir))
        if pe:
            ev = {r.get("metric"): num(r.get("value")) or 0 for r in pe}
            # wall = query_duration_ms；RealTimeMicroseconds 是跨线程求和的，会夸大它
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
                add("warning", "query-analysis", f"CPU 等待约占已用 CPU 的 {round(100 * wait / cpu)}%——执行受到了 CPU 限流/争用", "profile_events OSCPUWaitMicroseconds 对比 OSCPUVirtualTimeMicroseconds", "P-22")
            ns = ev.get("NetworkSendElapsedMicroseconds") or 0
            if wall and ns > 0.5 * wall:
                add("warning", "query-analysis", f"网络发送占 wall time 的 {round(100 * ns / wall)}%——行正在被送往发起方/客户端（distributed INSERT SELECT？可考虑 parallel_distributed_insert_select = 2）", f"NetworkSendBytes={human(ev.get('NetworkSendBytes'))}", "P-56")
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
                qa["runs_or_shards"] = "shards（一小时内在每个 host 上一个——是数据倾斜，不是性能回退）"
            else:
                qa["runs_or_shards"] = "runs（多次运行）"
        for f, rows in qa["files"].items():
            if rows == 0 and f.startswith("failed_"):
                qa["empty_not_applicable"].append(f)
        out["query_analysis"] = qa

    if out["mode"] == "gov":
        out["notes"].append("gov 模式：标识符是 SHA-256 哈希；文本、配置、host facts 和日志按设计被隐去")

    sev_rank = {"critical": 0, "warning": 1, "info": 2}
    findings.sort(key=lambda f: sev_rank.get(f["severity"], 3))
    return out


# ----------------------------------------------------------------------------- 渲染

def render_md(o) -> str:
    L = []
    L.append(f"# 诊断包检查 — {o['bundle']}")
    L.append("")
    L.append(f"- ClickHouse 版本：**{o.get('version') or 'unknown'}** · mode：**{o['mode']}** · 采集时间：{o.get('collected_at') or '?'}（collector 本地时间）")
    if o.get("run"):
        r = o["run"]
        bits = []
        if r.get("node"): bits.append(f"节点 **{r['node']}**")
        if r.get("target"): bits.append(f"通过 {r['target']} 采集")
        if r.get("uptime"): bits.append(f"采集时服务器已运行 {r['uptime']}")
        if r.get("collection"): bits.append(f"采集：{r['collection']}")
        if bits: L.append("- " + " · ".join(bits))
    if o.get("pools"):
        L.append("- 后台 pools（过去 24 h，每小时平均值对比大小）：" + " · ".join(
            f"{k} {v['max_avg']}/{v['size'] if v.get('size') else '?'}" + (f"（{v['hours_saturated_24h']} h 饱和）" if v.get("hours_saturated_24h") else "")
            for k, v in o["pools"].items()))
    if o.get("pools_by_host"):
        p = o["pools_by_host"]
        bits = [f"{p['hosts']} 个 host"]
        if p.get("saturated"):
            bits.append("饱和于：" + "，".join(f"{k} {v}" for k, v in p["saturated"].items()))
        if p.get("size_steps"):
            bits.append(f"{len(p['size_steps'])} 次实时 pool 大小阶跃")
        if p.get("elections_per_s_max") is not None:
            bits.append(f"leader 选举 ≤ {p['elections_per_s_max']}/s")
        L.append("- 按 host 的 pools（3 天，metric_log_by_host）：" + " · ".join(bits))
    if o.get("shared_tables"):
        st = o["shared_tables"]
        line = f"- Shared*MergeTree 表：{st['total']} · 空表 {st['empty']} · 少于 100 行 {st['under_100_rows']}"
        if o.get("smt_settings"):
            ss = o["smt_settings"]
            line += f" · parts_load_batch_size {ss['parts_load_batch_size']} · leader_update_period {ss['leader_update_period_seconds']} s（≈ {ss['estimated_elections_per_s']} 次选举/s）"
        L.append(line)
    qw = o.get("query_log_window")
    if qw:
        L.append(f"- query_log 窗口：{qw['from']} → {qw['to']}（{qw['hour_buckets']} 个小时桶）")
    tw = o.get("text_log_window")
    if tw:
        L.append(f"- text_log 切片：{tw['rows']} 行，{tw['from']} → {tw['to']}")
    if o.get("host"):
        h = o["host"]
        if h.get("cpus") is not None:
            L.append(f"- host：{h.get('hostname')} · {h.get('distro')} · 内核 {h.get('kernel')} · {h.get('cpus')} 个 CPU，load {h.get('load')} · RAM {h.get('ram')}（可用 {h.get('available')}）")
        else:
            L.append(f"- host：{h.get('hostname')}——host_info.json 已降级（{'; '.join(h.get('notes') or []) or 'sections 不可用'}）")
    if o.get("active_parts") is not None:
        L.append(f"- active part：{o['active_parts']}" + (" · 最大的库：" + "，".join(f"{d['database']}={d['human']}" for d in o.get("bytes_by_database_top3", [])) if o.get("bytes_by_database_top3") else ""))
    if o.get("metric_log"):
        m = o["metric_log"]
        L.append(f"- metric_log：{m['hours']} h · 每小时平均内存跟踪峰值 {m['max_avg_memory_tracking']} · merge pool 最大值 {m['max_merge_pool_tasks']}（{m['hours_pool_at_max']} h 处于最大值）· zk_hw_exceptions {m['zk_hw_exceptions_total']}")
    if o.get("query_log"):
        q = o["query_log"]
        codes = "，".join(f"{c['name']}({c['code']})={c['count']}" for c in q["top_exception_codes"]) or "无"
        L.append(f"- query_log：窗口内 {q['queries']} 个已完成/失败查询 · {q['exceptions']} 个异常 · top codes：{codes}")
    if o.get("query_views_log"):
        v = o["query_views_log"]
        L.append(f"- query_views_log：{v['views']} 个 materialized view · {v['executions']} 次执行 · "
                 f"{v['written_rows']} 行已写入 · {v['views_failing']} 个 view 存在失败的 push · "
                 f"{v['views_never_wrote']} 个 view 读取了但从未写入")
    if o.get("execution_log"):
        x = o["execution_log"]
        L.append(f"- collectors：{x['ok']}/{x['collectors']} 成功，查询耗时共 {x['total_s']} s · 最慢：{', '.join(x['slowest'])}"
                 + (f" · 失败：{'; '.join(x['failed'])}" if x['failed'] else ""))
    if o.get("uptime_seconds") is not None or o.get("databases"):
        bits = []
        if o.get("uptime_seconds") is not None:
            bits.append(f"服务器 uptime {round(o['uptime_seconds'] / 3600, 1)} h（system.errors / system.events 的分母）")
        if o.get("databases"):
            bits.append("按引擎统计的 database：" + "，".join(f"{k}={v}" for k, v in sorted(o["databases"].items(), key=lambda kv: -kv[1])))
        L.append("- " + " · ".join(bits))
    if o.get("error_log_top"):
        L.append("- error_log（7 天，含后台的所有线程）：" + "，".join(f"{e['name']}({e['code']})={e['count']}" for e in o["error_log_top"]))
    if o.get("text_log_histogram"):
        t = o["text_log_histogram"]
        L.append(f"- text_log 直方图：Error 级别日志出现在 {t['error_hours']} 个小时 · 峰值 {t['peak_errors']} 行，时间 {t['peak_hour']}（中位小时 {t['median_errors']} 行）· top classes：{', '.join(t['top_classes'])}")
    if o.get("empty_files"):
        L.append(f"- 空文件：{', '.join(o['empty_files'])}")
    for key, label in (("settings_changed", "已变更的 query/profile 设置"), ("server_settings_changed", "已变更的 server 设置"),
                       ("merge_tree_settings_changed", "已变更的 MergeTree 设置")):
        if o.get(key) is not None:
            names = "，".join(f"{r['name']}={r['value']}" for r in o[key][:12])
            L.append(f"- {label}：{len(o[key])}" + (f"——{names}" + (" …" if len(o[key]) > 12 else "") if o[key] else ""))
    if o.get("query_analysis"):
        qa = o["query_analysis"]
        L.append("")
        L.append("## 查询分析")
        L.append(f"- 焦点：`{qa.get('query_id')}` · {qa.get('query_kind')} · user {qa.get('user')} · 形态：**{qa.get('shape')}** · tables {qa.get('tables')}")
        L.append(f"- wall {qa.get('wall_seconds')} s（线程求和的实际时间 {qa.get('real_time_threads_seconds')} s）· read_rows {qa.get('read_rows')} · memory {qa.get('memory')} · SelectedParts {qa.get('selected_parts')} · SelectedMarks {qa.get('selected_marks')} · NetworkSendBytes {human(qa.get('network_send_bytes')) if qa.get('network_send_bytes') else '?'}")
        if qa.get("time_budget"):
            L.append("- 时间预算：" + "，".join(f"{k.replace('Microseconds','')} {v['seconds']} s（{v['pct_of_wall']}%）" for k, v in qa["time_budget"].items()))
        if qa.get("executions"):
            L.append(f"- 窗口内执行次数：{qa['executions']}，分布在 {qa['hosts']} 个 host 上，跨度 {qa.get('span_minutes')} 分钟 → {qa.get('runs_or_shards')}")
        if qa.get("empty_not_applicable"):
            L.append(f"- 因不适用于该形态而为空：{', '.join(qa['empty_not_applicable'])}")

    for n in o.get("notes", []):
        L.append(f"- note：{n}")
    L.append("")
    if o.get("alerts") is not None:
        fired = [a for a in o["alerts"] if a["state"] == "fired"]
        errored = [a for a in o["alerts"] if a["state"] == "error"]
        skipped = [a for a in o["alerts"] if a["state"] == "skipped"]
        L.append(f"## 告警 — {len(fired)} 个已触发，{len(errored)} 个无法运行，{len(skipped)} 个不适用，共 {len(o['alerts'])} 个")
        for a in fired:
            L.append(f"- **{a['severity']}** `{a['name']}` — {a['instances']} 个实例")
        L.append("")
    L.append(f"## Findings（{len(o['findings'])}）")
    if not o["findings"]:
        L.append("- 确定性检查没有发现问题——请继续执行 health-checks.md 的 HC-1…HC-11")
    for f in o["findings"]:
        ev = f" — {f['evidence']}" if f["evidence"] else ""
        chk = f" [{f['check']}]" if f["check"] else ""
        L.append(f"- **{f['severity']}** ({f['area']}) {f['message']}{ev}{chk}")
    L.append("")
    if o.get("incident_hours"):
        L.append("## 故障时间线 — 失败升高、merge 停滞或 Keeper 丢失的小时")
        L.append("| 小时 | 查询数 | 异常数 | top codes | 新 part | merge | 失败的后台操作 | zk 硬件异常 | zk 事务占中位数 % | 标记 |")
        L.append("|---|---|---|---|---|---|---|---|---|---|")
        for t in o["incident_hours"]:
            pct = "" if t.get("tx_pct") is None else f"{t['tx_pct']}%"
            L.append(f"| {t['hour']} | {t['queries']} | {t['exceptions']} | {t['top_codes']} | {t['new_parts']} | {t['merges']} | {t['failed_bg']} | {t['zk_hw']} | {pct} | {t['flags']} |")
        L.append("")
    if o.get("top_views"):
        L.append("## 按执行次数排序的 materialized view")
        L.append("| view | 执行次数 | 写入行数 | zero-write % | 失败的 push | 中位峰值内存 | 最大 ms |")
        L.append("|---|---|---|---|---|---|---|")
        for t in o["top_views"]:
            L.append(f"| {t['view']} | {t['executions']} | {t['written_rows']} | {t['zero_write_pct']}% | "
                     f"{t['failures']} | {t['median_peak_mem']} | {t['max_ms']} |")
        L.append("")
    if o.get("top_partitions"):
        L.append("## 按 active part 排序的 top 分区")
        L.append("| database | table | 分区 | parts | level-0 | 平均行数/part |")
        L.append("|---|---|---|---|---|---|")
        for t in o["top_partitions"]:
            L.append(f"| {t['database']} | {t['table']} | {t['partition_id']} | {t['parts']} | {t['l0_parts']} | {t['avg_rows']} |")
        L.append("")
    if o.get("top_errors"):
        L.append("## system.errors（自服务器启动以来累计）")
        L.append("| code | name | 次数 | 最后出现 |")
        L.append("|---|---|---|---|")
        for e in o["top_errors"]:
            L.append(f"| {e['code']} | {e['name']} | {e['count']} | {e['last']} |")
        L.append("")
    L.append("## 文件")
    L.append("| 文件 | 字节数 | 行数 |")
    L.append("|---|---|---|")
    for f in o["files"]:
        L.append(f"| {f['file']} | {f['bytes']} | {f.get('rows', '')} |")
    return "\n".join(L)


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("path", help="clickhouse_diagnostic_*.tar.gz（或更旧的 clickhouse_backup_*.tar.gz），或已解压的 bundle 目录")
    ap.add_argument("--json", action="store_true", help="输出 JSON 而不是 Markdown")
    ap.add_argument("--extract-to", help="解压归档的目标目录（默认：新建一个临时目录）")
    args = ap.parse_args()

    path = os.path.abspath(args.path)
    if not os.path.exists(path):
        die(f"{path} 不存在")
    if os.path.isfile(path):
        dest = args.extract_to or tempfile.mkdtemp(prefix="chdiag_")
        os.makedirs(dest, exist_ok=True)
        repaired_separators = safe_extract(path, dest)
        base = find_run_dir(dest)
        extracted_to = dest
    else:
        # 重命名文件虽然能修复它们——但也会修改证据。可以在解压到新目录的
        # 同时修复原始归档，因此改为指向那里。
        if _extracted_windows_bundle(path):
            die(f"{path} 是从 collector 的 Windows build 打包的归档中解压出来的："
                "其文件名类似 clickhouse_backup_<ts>\\system.version_<ts>.jsonl，若不重命名证据就无法"
                "就地读取。请改为对原始 .tar.gz 运行预检——它会在解压到新目录的同时修复名称。")
        repaired_separators = False
        base = find_run_dir(path)
        extracted_to = None

    require_jsonl(base)
    result = analyse(base)
    if repaired_separators:
        result["notes"].insert(0, WINDOWS_SEPARATOR_NOTE)
    result["path"] = base
    if extracted_to:
        result["extracted_to"] = extracted_to
    if args.json:
        print(json.dumps(result, indent=2, default=str))
    else:
        print(render_md(result))
        if extracted_to:
            print(f"\n（已解压到 {extracted_to}——可复用该目录来运行 SQL 配方）")


if __name__ == "__main__":
    main()
