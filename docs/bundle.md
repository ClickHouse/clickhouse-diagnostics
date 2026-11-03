# The bundle: output format, layout, host facts and configuration

*Moved from the main [README](../README.md), which keeps a short version of each section.*

## Output format

Query results are written one file per query, in the format chosen by `-output-format`. The default is **`jsonl`**.

| Value | ClickHouse format | Extension | |
|---|---|---|---|
| `jsonl` *(default)* | `JSONEachRow` | `.jsonl` | One self-describing JSON object per line. `grep`- and `jq`-able, and every row carries its column names — which matters because `system.query_log` has ~60 columns and positional formats leave you counting fields back to a header. |
| `native` | `Native` | `.native` | The ClickHouse binary format used before v0.3.0. Column-oriented, exactly typed, reloadable with no schema — but unreadable without a ClickHouse to load it into. |
| `tsv` | `TSVWithNamesAndTypes` | `.tsv` | Names on line 1, **types on line 2**. The most faithful text format for reload, but positional, so it reads poorly for wide tables. |

### Queries are bounded on the server

The only bound used to be client-side: a 5-minute HTTP timeout. That timeout and the server had no relationship — when it fired, Go closed the connection and the tool moved on while the server kept executing. `cancel_http_readonly_queries_on_client_close` defaults to `0` on self-managed, so on a slow cluster you accumulate abandoned queries, each still burning CPU and memory, while the collector stacks more on top. Cloud sets it to `1`, which is why this was invisible in Cloud testing and only bit the self-managed audience the tool is mainly for.

Every request now carries `max_execution_time` (default 240s, set with `-query-timeout`). The server enforces it, so a slow query fails with a clean `Code: 159` that lands in the customer's `query_log` and can be diagnosed, instead of vanishing into a silent disconnect. The HTTP client waits 60s longer than whatever `-query-timeout` says, so the server always wins the race. `-query-timeout=0` disables both.

Two more settings ride along. `cancel_http_readonly_queries_on_client_close=1` tells the server to drop the query when we disconnect — it only applies under `readonly>0`, and note it does **not** fire over HTTPS today: on a graceful close the server's liveness peek sees the TLS `close_notify` bytes and concludes the peer is still there ([ClickHouse#96737](https://github.com/ClickHouse/ClickHouse/issues/96737)). It works on plain `http`, which is why `max_execution_time` is the limit to rely on. `enable_http_compression=1` lets the server gzip the response, which matters on bastion hosts over a slow link; Go's transport requests and decompresses it transparently, and will stop doing so if anyone ever sets `Accept-Encoding` by hand.

### Large integers are never rounded

JSON numbers are IEEE-754 doubles in most parsers, so an unquoted `UInt64` above 2^53 is silently corrupted on read — JavaScript turns `18446744073709551615` into `18446744073709552000`, and so does any `jq` expression that does arithmetic on it. This is not hypothetical: `normalized_query_hash` and every `cityHash64` value in `system.query_log` are full-width `UInt64`.

The tool therefore pins `output_format_json_quote_64bit_integers=1` on every request, so 64-bit integers are emitted as **quoted strings** and survive every parser exactly. The setting is writable under `readonly=1`, so a server-side profile could otherwise turn it off — pinning it is what makes the guarantee hold rather than depend on the server's configuration.

`native` and `tsv` are exact by construction. Cast on read when you reload:

```bash
clickhouse-local -q "SELECT toUInt64(normalized_query_hash) FROM file('x.jsonl','JSONEachRow')"
```

### Size

The shipped artefact is a `tar.gz`, and JSON's repeated keys compress away almost entirely. Measured on a real bundle (21 queries, ClickHouse 24.8):

| Format | Uncompressed | **Archive (`tar.gz`)** |
|---|---|---|
| `jsonl` | 2.20 MB | **225 KB** |
| `native` | 1.83 MB | 240 KB |
| `tsv` | 2.12 MB | 272 KB |

`jsonl` is 20% larger on disk but produces the **smallest archive** of the three, so the readable default costs nothing in what you actually send.

## Host facts and server logs

Beyond what ClickHouse reports about itself, the tool collects the surrounding context that usually explains it: the host it runs on, and the log files on that host's disk.

### When these run

Both collectors read the **machine executing the tool**, so `-host-info` and `-logs` take `auto` (the default), `on` or `off` rather than a plain skip flag:

| Mode | `auto` resolves to | Why |
|---|---|---|
| `onprem` | **on** | The tool runs on the ClickHouse server, so `/proc` and `/var/log/clickhouse-server` describe that server. |
| `cloud` | **off** | ClickHouse Cloud is reached over the network. Local host facts and log files would describe your laptop or bastion — confidently wrong data in a support bundle is worse than none. Pass `-host-info=on` / `-logs=on` if you are pointing cloud mode at a self-managed node. |
| `gov` | **off**, unconditionally | Hostnames, mount paths, process command lines and log bodies are exactly what gov hashing protects, and none of them can be hashed while staying useful. `-host-info=on` is **rejected**, not ignored. |

Both are also skipped under `--dry-run`, which promises to write nothing. The mode matrix is pinned by tests in `cmd/local_collector_test.go`.

### `keeper/` — what each Keeper member says about itself

`-keeper-mntr auto|on|off` (default `auto`) sends seven read-only four-letter commands to the Keeper members and writes one `keeper/<host>_<port>.txt` per member. The member list is the union of two sources: `system.zookeeper_connection`, which names the member each configured connection is **on right now** (one row per connection — the default `<zookeeper>` block and every auxiliary one — not one per ensemble member), and every `<zookeeper><node>` in the server configuration (`-config-dir` and the adjacent `config.xml`) when the tool runs on the server. The second is what reaches the followers this server is *not* connected to — the member a Keeper incident is usually about. A run without access to the configuration (remote `-host`, cloud, gov without `-config-dir`) probes the connected member only, and says so in the execution log.

| Command | What it answers |
|---|---|
| `ruok` | liveness (`imok`) |
| `srvr` | Keeper version, latency min/avg/max, received/sent, connections, **outstanding requests**, zxid, **mode** (leader / follower / standalone), node count |
| `mntr` | the same as `zk_*` counters, plus followers and synced followers (on the leader), znode / watch / ephemeral counts, memory, latest snapshot size |
| `isro` | `ro` for a read-only member or observer, `rw` otherwise |
| `lgif` | the member's Raft log: first / last index, committed vs leader-committed, last snapshot index (absent before 23.x) |
| `conf` | the coordination settings in force — `snapshot_distance`, `reserved_log_items`, `snapshot_transfer_chunk_size`, `write_snapshot_version` … (in gov only numbers, booleans and plain words survive) |
| `dirs` | bytes on disk in the snapshot and log directories |

`ruok`, `isro` and `lgif` are sent first because they answer even on a member that is **not serving** (no quorum, still loading its snapshot); the others then reply `This instance is not currently serving requests`, which is recorded as the member's status. Every command is read-only, goes nowhere near Raft and does not grow with the data size: ~0.3 ms each on a healthy member. The members are asked **in parallel** and a member gets nothing more after its first network failure, so one or several hung members cost one read timeout (10 s) in total, not one per command. Commands with side effects (`csnp`, `rqld`, `ydld`, `rcvr`, `srst`, `crst`, `rclc`, `clrs`) are never sent. Each file names its ensemble (`# ensemble: default`, or the auxiliary ZooKeeper's name — hashed in gov), so members of different ensembles are never compared; an auxiliary member's file is `<ensemble>__<host>_<port>.txt`, so a default and an auxiliary connection to one host:port never overwrite each other.

These are the server-side facts a Keeper finding is settled with: `metric_log` says how long *this server* waited for Keeper, `zookeeper_connection` says which member it talks to, and only Keeper itself says whether that member is the leader, how many requests it has queued and what it runs. Each command opens its own TCP connection (3 s dial, 10 s read) because Keeper closes after one reply; a member that is down or refuses four-letter words is recorded as such in its file and in `execution_log.txt`, never a failed run. `stat` and `cons` are **never sent**: they list client addresses.

| Mode | `auto` resolves to | Why |
|---|---|---|
| `onprem` | **on** | The tool runs beside the cluster, so the Keeper ports are reachable. |
| `gov` | **on**, host hashed | The counters are numbers; the only identifier is the member's host, which is written as `hex(SHA256(host ‖ salt))` in the file name and header — the same form `system.zookeeper_connection` carries in a gov bundle, so the two join. Any reply line carrying an `ip:port` is dropped as a safeguard. |
| `cloud` | **off** | A managed service's Keeper is not reachable from outside. `-keeper-mntr=on` is honoured with a warning for a self-managed cluster collected in cloud mode. |

Under `--dry-run` the members are listed and nothing is contacted; the one-row-per-connection `SELECT` on `system.zookeeper_connection` that builds the list is a metadata pre-flight read, executed for real and printed like the version probe. Per-member statuses in `execution_log.txt`: `ok` (all three commands answered), `partial` (some did — usually a word missing from `four_letter_word_white_list`), `refused`, `timeout` (including a reply cut short by the 10 s deadline, kept in the file and marked truncated), `failed` (every command failed for another reason, or the file could not be written). Servers before 23.8 have no `system.zookeeper_connection`, so the step is skipped and says so. Keeper must allow the three words in `four_letter_word_white_list` (the default `*` does); an empty reply is annotated accordingly.

### `host_info.json` — OS, kernel and hardware

Read from `/proc`, `/sys` and `/etc` with no external dependencies, so it only yields data when the tool runs **on the ClickHouse host** — which is why `auto` restricts it to onprem. Forced on elsewhere, each section is marked unavailable with a note rather than silently empty.

| Section | Contents |
|---|---|
| `os` | hostname, **distro + version** (`/etc/os-release`), kernel version and full banner, arch, uptime |
| `cpu` | logical CPUs, model, vendor, load average, and the vector flags ClickHouse dispatches on (`avx2`, `avx512f`, … / `asimd`, `sve` on ARM) |
| `memory` | total / free / available / buffers / cached / swap, in bytes |
| `disks` | every real mount: device, mount point, fs type, total, free, used % (pseudo- and file-bind mounts filtered out) |
| `top_processes_by_rss` | top 25 by RSS — pid, ppid, state, threads, RSS, command |
| `clickhouse_relevant_tunables` | **transparent hugepages** + defrag, `vm.swappiness`, `vm.overcommit_memory`, `vm.max_map_count`, `fs.nr_open`, `fs.file-max`, **cgroup memory/CPU limits**, and the running server's own `LimitNOFILE` |

That last row is the point of the collector: THP set to `always`, a cgroup memory cap far below `MemTotal`, or a low `LimitNOFILE` explain a large share of ClickHouse incidents and are invisible from inside the database.

### `logs/` — server log files

```bash
./clickhouse-diagnostic -mode onprem -host ch-01           # discovers the log directory
./clickhouse-diagnostic -mode onprem -logs-dir /data/ch/logs
```

The directory is resolved in this order:

1. `-logs-dir`, used verbatim (a bad path is reported, never silently replaced)
2. **`<log>` / `<errorlog>` paths read from the server configuration** — operators relocate logs more often than they change anything else, and this is what stops a bundle from quietly containing none
3. `/var/log/clickhouse-server`

Both 2 and 3 are collected when they differ. `*.log` is copied by default; `-logs-include-archives` adds rotated `*.gz`/`*.zst`. Files above `-logs-max-mb` (default 50) are **tail**-truncated — the recent end is kept, with a header recording what was dropped so the first surviving line isn't mistaken for the start of the log.

> At `<level>debug</level>` a busy server writes 50 MB in **seconds**, so the tail of `clickhouse-server.log` rarely reaches back to an incident that ended hours earlier (the `.err.log` tail usually covers a few hours). For the incident timeline rely on `system.text_log_histogram_1_day` and the hourly aggregates, and raise `-logs-max-mb` (or ship the rotated file with `-logs-include-archives`) only when a specific log passage is needed.

### `--collect-text-log` — a bounded slice of `system.text_log`

**Off by default and requires an explicit window**, because `text_log` is the highest-volume table ClickHouse writes: an unbounded dump would be enormous and would itself load the server.

```bash
./clickhouse-diagnostic -mode onprem -host ch-01 \
  -collect-text-log --from 2026-08-20T14:00:00Z --to 2026-08-20T15:00:00Z \
  -text-log-level Warning
```

| Flag | Meaning |
|---|---|
| `-collect-text-log` | Enable it. **Requires `--from` and `--to`** — the run fails fast otherwise. |
| `-text-log-level` | Minimum severity (`Fatal`…`Trace`). Omit for all levels. |
| `-text-log-limit` | Row cap (default 500 000). |

Written as `text_log_<timestamp>.<ext>`, in the [configured output format](#output-format). In cloud mode it fans out across replicas; `message_format_string` is only selected on 23.1+. If `system.text_log` is disabled (the ClickHouse default) the tool says so and explains how to enable it, rather than reporting a bare error.

## Configuration Collection

When `-skip-config` is not set, the tool reads files from `-config-dir` (default `/etc/clickhouse-server/config.d/`; in `cloud` mode collection is skipped unless `-config-dir` is given explicitly, and `gov` mode never collects) and writes sanitised copies into the run's `configuration/` directory (inside `clickhouse_diagnostic_<timestamp>/`), mirroring the source tree — `config.d/storage.xml` and `users.d/storage.xml` stay distinct, and the directory a file came from (which determines ClickHouse's merge order) is preserved.

Sanitisation runs in two layers — proper XML / YAML parsing first, then a heuristic byte-pattern pass over the result. If a file cannot be parsed, the tool **fails closed**: a warning is logged and the file is skipped rather than shipped un-sanitised.

### What gets sanitised

**Structural** (driven by tag / attribute / YAML-key name, case-insensitive):

- Any name matching the exact list: `client_id`, `tenant_id`, `account_name`, `account_key`, `storage_account_url`, `role_arn`, `role_session_name`, `session_token`, `connection_string`
- Any name **containing** one of: `password`, `passwd`, `secret`, `credential`, `token`, `private_key`, `api_key`, `access_key`, `service_account` — so future or custom tags like `<my_db_password>`, `<gcp_service_account_credentials>`, `<legacy_api_token>` are caught without code changes
- XML attribute values with the same naming rules: `password="..."`, `aws_access_key_id="..."`, etc.
- Multi-line values inside the above tags (the previous regex-only implementation missed these)

**Heuristic** (byte-shape signatures, applied to the whole file including comments and free text):

- PEM-encapsulated private keys (`-----BEGIN ... PRIVATE KEY-----` blocks)
- AWS access key IDs (`AKIA…`, `ASIA…`)
- JWT tokens (`eyJ…` three-segment shape)
- Bcrypt password hashes (`$2[abxy]$…`)
- Long hex strings (40+ chars — SHA-1, SHA-256, longer)
- Long base64 blobs (128+ chars — GCP service-account payloads etc.)
- Credentials embedded in URLs (`proto://user:pw@host` — only the password is redacted, host stays)
- `keyword: value` / `keyword = value` / `keyword "value"` disclosures in prose, where keyword contains `password`, `secret`, `token`, `api_key`, `access_key`, `private_key`, or `credential` — catches values pasted into XML comments or YAML `# notes`

### What does **not** get sanitised

- Hostnames, IPs, cluster topology
- Database and table names
- Performance / resource settings
- Non-security configuration
- Prose mentioning credential keywords without an actual value (e.g. `password requirements are documented elsewhere`)

The heuristic intentionally favours over-redaction over under-redaction — a stray flagged hash in the output is harmless, a leaked credential is not. Always review the contents of the run's `configuration/` directory before sharing the archive.

## Output Layout

A single run produces:

```
clickhouse_results/
├── clickhouse_diagnostic_YYYYMMDD_HHMMSS/                   # → archived
│   ├── system.parts_YYYYMMDD_HHMMSS.jsonl                   #   one file per query
│   ├── system.mutations_YYYYMMDD_HHMMSS.jsonl
│   ├── ...
│   ├── configuration/                                       #   sanitised configs, tree preserved (unless -skip-config; never in gov)
│   │   ├── config.xml
│   │   ├── config.d/…
│   │   └── users.d/…
│   ├── query_analysis/                                      #   only with --query-id / --hash
│   ├── keeper/<host>_<port>.txt                             #   four-letter replies per Keeper member (-keeper-mntr; gov hashes the host; not in cloud by default)
│   ├── dashboard.html                                       #   unless -skip-dashboard or gov
│   ├── execution_log.txt                                    #   every collector: outcome, wall time, size; alerts; phases
│   └── alerts_summary.json                                  #   when alerts ran but dashboard.html is absent
├── clickhouse_diagnostic_YYYYMMDD_HHMMSS.tar.gz             # → the archive, unless -skip-archive
└── clickhouse_diagnostic_YYYYMMDD_HHMMSS_gov_name_mapping.csv   # → LOCAL only (gov mode)
```

- **Query results**: one file per query, in the format chosen by [`-output-format`](#output-format) (default `jsonl`)
- **Dashboard**: standalone `dashboard.html`, loads Chart.js from CDN
- **Execution log**: `execution_log.txt` — one line per collector query with its version directory, outcome (`ok` / `failed` / `empty`), wall time, result bytes and rows, plus every alert rule with its outcome and duration, one `keeper` entry per Keeper member probed (`ok` / `partial` / `refused` / `timeout`), and the wall time of each phase (collectors, keeper facts, host facts, logs, config, alerts, dashboard). The *Most expensive collectors* list is what to read before adapting a window in `queries.<mode>/`; the *Failed collectors* list is what separates "the table was empty" from "the query never ran". Contains file names, timings and ClickHouse error text only — no result data.
- **Archive**: `tar.gz` containing the per-run results directory — `configuration/` now lives *inside* it, tree intact, so a bundle can only ever contain this run's configs. (Before v0.3.0 it was a flat, process-wide `./configuration` beside the run directory; anything parsing bundles by that path needs updating.)
- **Gov-mode mapping CSV** (gov mode only): sits next to the backup folder, **not inside it** — never goes into the archive. See [Gov mode and hashed names](modes.md#gov-mode-and-hashed-names).
