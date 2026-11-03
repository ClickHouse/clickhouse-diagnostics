# Modes: onprem, cloud and gov

*Moved from the main [README](../README.md), which keeps a short version of each section.*

## What leaves your machine

Before sending an archive to anyone:

- The tool **never selects customer rows** — every query reads a `system` table. Free-text columns can still carry customer values (query text, exception messages, log lines); see [Gov mode and hashed names](#gov-mode-and-hashed-names) for what is withheld or hashed when that matters.
- `configuration/` is **credential-stripped** (passwords, keys, tokens, PEM blocks) but keeps hostnames, IPs, cluster topology and table names — review it before sharing. See [Configuration Collection](bundle.md#configuration-collection).
- `-dry-run` prints every SELECT the tool would run, with `EXPLAIN ESTIMATE`, and collects nothing — use it for a security review first. See [Dry-run mode](running.md#dry-run-mode).
- `gov` mode hashes database/table/user/host names with your private salt and withholds the dashboard, query text, configs, host facts and logs. **The salt and the local `*_gov_name_mapping.csv` never leave your machine.**
- `alerts_summary.json` (when written) contains rule names and counts only, never matched rows.
- `keeper/` holds Keeper's own read-only four-letter replies (`ruok`, `isro`, `lgif`, `srvr`, `mntr`, `conf`, `dirs`): version, Raft log position, settings, latency, outstanding requests, node counts; in gov `conf` keeps only numbers, booleans and plain words — paths, hosts and customer-chosen names say REMOVED. The commands that list client addresses (`stat`, `cons`) are never sent; gov hashes the member's host. See [Keeper facts](bundle.md#keeper--what-each-keeper-member-says-about-itself).

---

*The sections below are the full reference.*

## Modes and Query Layout

**SharedMergeTree auto-switch.** A `-mode onprem` run against a server with `cloud_mode = 1` (a self-hosted SharedMergeTree cluster) switches to the `queries.cloud/` set on its own — every per-replica system table (`query_log`, `part_log`, `errors`, `metric_log`, `text_log` …) fans out over the `default` cluster, while the shared tables (`parts`, `tables`, `columns`, `databases`, `replicas`, `replication_queue`, `mutations`, `detached_parts`) are read from one replica as in any cloud collection — while still collecting host facts, configuration and log files from the machine running the tool (this node's when you run it there — the `warnings:` header line says when `-host` pointed elsewhere), which cloud mode alone never does. The run says so, and `execution_log.txt` records `collection: onprem → cloud: SharedMergeTree detected`. It needs `REMOTE` and `CREATE TEMPORARY TABLE ON *.*`; without them the run stays on one node and records why. `-single-node` disables the switch. The header also records `node:` (`hostName()`, `FQDN()`) and `uptime:`, and the tool warns when repeated probes answer with different hostnames (a load balancer) or when `-host` is remote while local files are being collected — the two ways a bundle ends up describing the wrong machine.

`-mode` selects which top-level query directory the tool reads from:

| Mode | Query directory | Notes |
|---|---|---|
| `cloud` | `queries.cloud/` | Uses `clusterAllReplicas(...)` to fan out across replicas |
| `onprem` | `queries.onprem/` | Single-node `system.*` references |
| `gov` | `queries.gov/` | On-prem shape with PII columns hashed; omits `crash_log`/`stack_trace` (may contain sensitive symbols) |

> ⚠️ **The `queries.<mode>/` directory MUST exist in the current working directory when you run the binary.** The tool resolves the queries folder as a path *relative to your CWD* (e.g. `./queries.onprem`), not relative to the binary itself. So if you ship the binary to another host, you must ship the matching `queries.<mode>/` folder alongside it and `cd` into the directory containing both before running.
>
> Recommended bundling pattern:
> ```bash
> # Build + bundle locally
> make release
> tar czf clickhouse-diagnostic.tgz \
>   bin/clickhouse-diagnostic-linux-amd64 \
>   queries.cloud queries.onprem queries.gov
>
> # On the target host
> scp clickhouse-diagnostic.tgz host:/tmp/
> ssh host
> mkdir -p /opt/ch-diag && cd /opt/ch-diag && tar xzf /tmp/clickhouse-diagnostic.tgz
> ./bin/clickhouse-diagnostic-linux-amd64 -mode onprem ...   # CWD now has the queries.* folders
> ```
>
> Running from `/`, `~`, or any other directory without `queries.<mode>/` as a sibling will fail with `Error: Queries folder './queries.<mode>' does not exist`.

Alert queries use the same mode — see [Alerts](alerts.md#alerts).

> **Self-hosted SharedMergeTree clusters** (`cloud_mode = 1` in `system.settings`, table engines `Shared*MergeTree`, an `s3_with_keeper` disk): every replica keeps its own `system.*` tables, so `-mode onprem` describes **one node of N** — its parts, errors, part_log, query_log and text_log only. The tool prints a warning when it detects `cloud_mode = 1`. Run `-mode cloud` for the cluster-wide view (fans out over the `default` cluster with `clusterAllReplicas`; needs `GRANT REMOTE` and `CREATE TEMPORARY TABLE`) and keep an `onprem` run from one node for host facts, configuration and log files.

### Gov mode and hashed names

In `gov` mode, every database and table name written to the support-bound output is replaced with `hex(SHA256(name + salt))`. The salt is supplied by **you** at runtime (via `-salt` or the interactive prompt) — the tool does **not** ship with a default. This is what makes the hashes meaningful: without a per-customer salt, anyone with the source could pre-compute hashes for common names like `users`, `events`, or `orders` and reverse the obfuscation.

**The settings collectors hash nothing** — neither setting names nor values. A setting value is a server tuning knob rather than a customer identifier, and the mapping CSV below is built from `system.tables`, so a hashed setting value would be unreversible noise rather than protection. `system.settings` therefore emits every value as the server reports it. In `system.server_settings`, the few values that name customer infrastructure — anything path- or URL-shaped, plus `default_database`, `default_replica_name`, `interserver_http_host`, `default_profile`, `merge_workload` and `mutation_workload` — are **removed** instead of salted, replaced by `REMOVED` (the same sentinel the config sanitiser uses). The setting name, its ClickHouse default and `changed` all survive, so the archive still shows *that* a path was customised without showing what it is. On a 26.7 server that is 21 of 439 rows removed and 418 readable.

**Salt requirements:**

- 8–64 ASCII alphanumeric characters (`A–Z`, `a–z`, `0–9`)
- No spaces, no punctuation, no quotes — keeps the value safe inside SQL string literals
- Pick something **not guessable from public information** (avoid your company name, deployment ID, or anything in your support tickets)
- **Re-use the same salt across runs** if you want hashes to be comparable over time

**What the tool produces in gov mode:**

```
clickhouse_results/
├── clickhouse_diagnostic_YYYYMMDD_HHMMSS/                   # → goes into the archive
│   └── *.jsonl                                              # hashed names inside
├── clickhouse_diagnostic_YYYYMMDD_HHMMSS.tar.gz            # → the archive, send this
└── clickhouse_diagnostic_YYYYMMDD_HHMMSS_gov_name_mapping.csv   # → stays LOCAL
```

The mapping CSV (real_name → hashed_name) is written **outside** the timestamped backup folder, so it is **never** picked up by `tar` when the archive is built. Keep it on your machine for your own correlation work — and confirm before sending the archive that the salt and the CSV did not accidentally land inside it.

**What to share with support:**

| File | Share with support? |
|---|---|
| `clickhouse_diagnostic_*.tar.gz` | Yes |
| `clickhouse_diagnostic_*_gov_name_mapping.csv` | **No — keep local** |
| The salt value itself | **No — keep local** |

If you lose the salt, the hashes in past archives are no longer reversible (you can still run a new diagnostic with a fresh salt — but old and new hashes won't compare).

### What gov mode does not produce

Several outputs are deliberately withheld in gov mode, because each is part of the support-bound archive and none can be hashed meaningfully:

| Output | Why |
|---|---|
| `dashboard.html` | Its panels are built in Go and select raw identifiers — database/table names for up to 2000 tables, disk paths, users — plus server-generated text (`last_exception`, `last_error_message`). Hashing every panel is the follow-up that would restore it. |
| Query analysis (`--query-id` / `--normalized-query-hash`) | The bundle embeds raw query text, exception messages and full DDL. Freeform SQL text can't be hashed without destroying its diagnostic value, so the flags are rejected outright. |
| **Query text in `system.query_log_details_7_days`** | The onprem/cloud collectors archive the first 500 characters of one sampled query per `normalized_query_hash`, so a hash in that file can still be mapped back to SQL once the server is gone. Query text embeds customer literals, so the gov variant omits the column entirely — it is the only mode where that mapping is unavailable. |
| **Configuration files** (`configuration/`) | The XML embeds raw hostnames — `<macros>` shard/replica names, `<remote_servers>`, `<zookeeper>` hosts. Sanitisation strips credentials, not identifiers, and an XML tree can't be hashed while remaining mergeable. |
| **Host facts** (`host_info.json`) | Hostname, mount paths and process command lines are exactly the identifiers gov hashing protects — and a command line can't be hashed while staying useful. |
| **Server log files** (`logs/`) | Log lines carry raw queries, table names and paths as free text. Hashing a log destroys the reason to collect it. |
| **`--collect-text-log`** | Same reasoning as the log files: `system.text_log` messages embed raw SQL and identifiers. The flag is rejected in gov mode. |

Because the dashboard is the only other consumer of alert results, gov runs instead write **[`alerts_summary.json`](alerts.md#alerts_summaryjson)** into the archive — so you still get *"`too_many_parts` fired, 12 instances"* without the table names.

Everything else — the per-mode result files — is hashed as described above.


