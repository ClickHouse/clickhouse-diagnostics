# ClickHouse Diagnostic Tool

## What this is for

When a ClickHouse server misbehaves — inserts rejected, queries running out of memory, a replica read-only, a disk filling up, Keeper slow — the answers are in its `system` tables, its logs and the host it runs on. `clickhouse-diagnostic` collects that evidence **once, read-only, in one command**, and packs it into a single `clickhouse_diagnostic_<timestamp>.tar.gz`: something to keep, share with support, open as a dashboard, or hand to an AI assistant that knows how to read it.

It is built to run **on a server that is already in trouble**. Before collecting, a one-second probe looks at the server and picks how much to collect:

| `-collect` | What it does | When |
|---|---|---|
| `auto` *(default)* | Full collection on a healthy server, essential on a struggling one; switches to essential mid-run if queries start timing out | Almost always |
| `full` | Everything, with the normal look-back windows (7, 3 or 1 day per table) | A healthy server, when you want the complete picture |
| `essential` | Only the cheap, decisive tables, 1-day windows, short timeouts, a 3-minute budget, gentle query settings | A node that is overloaded, out of memory or has lost Keeper |

**How it works.** Every query is a read-only `SELECT` against a `system` table — customer rows are never read. The results are written as files, then the alert rules and the dashboard are built **from those files**: nothing more is asked of the server after collection. Configuration files are copied with credentials removed, and `gov` mode hashes every identifier when the bundle must leave the organisation.

## Quick start

Download the archive for your platform from [Releases](https://github.com/ClickHouse/clickhouse-diagnostics/releases/latest), extract it, and run from the extracted folder (it holds the binary and the `queries.*` and `alerts/` folders it reads). Create the read-only user first — see [Required grants](#required-grants).

```bash
export CLICKHOUSE_PASSWORD='…'     # read when -password is absent; stays out of ps and shell history

# A self-managed node: run it on the server, so host facts, logs and configs are collected too
./clickhouse-diagnostic -mode onprem -host localhost -user sys_read_only

# A ClickHouse Cloud service
./clickhouse-diagnostic -mode cloud -host <service>.<region>.aws.clickhouse.cloud -port 8443 -protocol https -user sys_read_only

# Identifiers hashed, for a bundle that must leave your organisation
./clickhouse-diagnostic -mode gov -host ch-01 -user sys_read_only -salt <YourPrivateSalt>
```

Each run leaves `clickhouse_diagnostic_<ts>.tar.gz` in `clickhouse_results/`, next to the unpacked folder. Open **`dashboard.html`** from that folder first, then read the bundle with the [AI skill](#troubleshoot-with-the-ai-skill).

## Required grants

```sql
CREATE USER sys_read_only IDENTIFIED WITH sha256_password BY '<password>';
GRANT SHOW DATABASES, SHOW TABLES, SHOW COLUMNS ON *.* TO sys_read_only;
GRANT SELECT ON system.* TO sys_read_only;
```

**Cloud mode** also needs `GRANT REMOTE, CREATE TEMPORARY TABLE ON *.* TO sys_read_only` (its queries read every replica through `clusterAllReplicas`).

<details>
<summary>Why these grants, and what a missing one looks like</summary>

- `SELECT ON system.*` — every query reads a `system` table; nothing else is ever selected.
- `SHOW …` — ClickHouse lists only the objects the user has *some* privilege on. Without these the run still succeeds, but the bundle silently describes a fraction of the server. If `system.databases` holds only `system`, the grant is missing.
- Do not grant `SELECT` on the data databases instead: it collects the same bundle but gives the diagnostic user read access to customer data.
- Configs, host facts and logs are read from the local filesystem, so they need OS read access, not grants.

Details and measurements: [docs/running.md](docs/running.md#required-grants).
</details>

## Examples

```bash
export CLICKHOUSE_PASSWORD='…'          # read when -password is absent; stays out of ps and shell history

# ── How much to collect ─────────────────────────────────────────────────────
# auto (default): full on a healthy server, essential on a struggling one.
# The run prints what it chose and why, e.g.
#   "essential collection, chosen automatically: load average 26.6 on 8 cores"
./clickhouse-diagnostic -mode onprem -host localhost -user diag

# Force everything, whatever the load (once the node is healthy again)
./clickhouse-diagnostic -mode onprem -host localhost -user diag -collect full

# Force the light version: cheap tables, 1-day windows, 30 s per query, 3 min in total
./clickhouse-diagnostic -mode onprem -host localhost -user diag -collect essential

# A node in trouble, every second counts: tighter budget and timeout
./clickhouse-diagnostic -mode onprem -host localhost -user diag -collect essential -time-budget 90s -query-timeout 20s

# ── An incident at a known time ─────────────────────────────────────────────
# -from / -to replace every table's default look-back; the alerts and the
# dashboard then judge that window. A few hours is also the cheapest run there is.
./clickhouse-diagnostic -mode onprem -host ch-01 -user diag -from 2026-08-14T09:00:00Z -to 2026-08-14T13:00:00Z

# Widen every window instead: everything since 21 July
./clickhouse-diagnostic -mode onprem -host ch-01 -user diag -from 2026-07-21

# ── A whole cluster ─────────────────────────────────────────────────────────
# Asks one node for the hosts, shows the plan, then collects each host in turn.
# One bundle per host in cluster_bundles_<ts>/; each host picks its own mode.
./clickhouse-diagnostic cluster -user diag
./clickhouse-diagnostic cluster -user diag -yes                       # no confirmation prompt
./clickhouse-diagnostic cluster -user diag -collect-from each-node    # print the command to run ON each host
                                                                      # (adds its server logs and configs)

# ── One slow or failing query ───────────────────────────────────────────────
# A query_id from a ticket: its details, ProfileEvents and log lines, compared
# with the fast and slow runs of the same query shape (48 h around it)
./clickhouse-diagnostic -mode onprem -host ch-01 -user diag -query-id 3f2c9a6e-5b1d-4c8a-9e7f-2a4b6c8d0e1f

# A query shape (normalized_query_hash) that regressed this week: the slowest
# execution is picked automatically and compared with the rest
./clickhouse-diagnostic -mode onprem -host ch-01 -user diag -normalized-query-hash 7769688026807387533 -from 2026-10-26

# Both, over an explicit window
./clickhouse-diagnostic -mode onprem -host ch-01 -user diag -query-id 3f2c9a6e-5b1d-4c8a-9e7f-2a4b6c8d0e1f \
  -normalized-query-hash 7769688026807387533 -from 2026-10-30T08:00:00Z -to 2026-10-30T12:00:00Z

# ── Cloud and gov ───────────────────────────────────────────────────────────
./clickhouse-diagnostic -mode cloud -host <service>.<region>.aws.clickhouse.cloud -port 8443 -protocol https -user diag
./clickhouse-diagnostic -mode gov -host ch-01 -user diag -salt <YourPrivateSalt>    # names hashed; no dashboard

# ── Before and after ────────────────────────────────────────────────────────
./clickhouse-diagnostic -mode onprem -host ch-01 -user diag -dry-run    # list every SELECT and its EXPLAIN ESTIMATE; collect nothing
./clickhouse-diagnostic -mode onprem -host ch-01 -user diag -airgap     # a dashboard that opens without internet
```

Each run prints the archive it wrote (`clickhouse_results/clickhouse_diagnostic_<ts>.tar.gz`). The essential choice is also the `collect:` line of `execution_log.txt` and a banner in `dashboard.html`, which lists what was skipped. More: [docs/running.md](docs/running.md#examples), [`cluster-collect/`](cluster-collect/README.md), [Query analysis](#query-analysis), [`airgap/`](airgap/README.md).

## Troubleshoot with the AI skill

`skills/clickhouse-diagnostic/` teaches an AI assistant — Claude Code, Codex CLI, or any assistant that reads `SKILL.md` / `AGENTS.md` — to read a bundle the way a ClickHouse support engineer would. It checks what the bundle covers, runs the health checks, matches the findings against a catalogue of recurring ClickHouse failure patterns taken from real escalations, and writes a short summary where **every finding cites `file → column → value`**, with the recommended fix, and the exact re-collection command when the bundle cannot answer the question.

**Start it.** Open this repository in Claude Code (the skill is discovered automatically) and run:

```
/clickhouse-diagnostic ~/Downloads/clickhouse_diagnostic_20261102_160031.tar.gz
```

Codex finds it through `AGENTS.md`. From another folder, copy or symlink `skills/clickhouse-diagnostic` into your assistant's skills directory.

**Ask it things like:**
- *"Analyse this bundle — why are inserts failing with TOO_MANY_PARTS?"*
- *"Is Keeper the bottleneck? Which replica, which component, since when?"*
- *"This replica is read-only — what happened, and what should the customer do?"*
- *"What should I re-collect to confirm this?"* — it answers with the command.

**It knows the failures we have seen.** The skill carries a catalogue of nearly 50 recurring ClickHouse failures, each with what you see in the bundle, what it usually means, how to verify it and the fix that worked — anonymised from real escalations. When the evidence matches, the summary names the pattern. For example, on a bundle where every replica went read-only:

> **Keeper has no leader, so every replicated table is read-only** — resembles P-61.
> `keeper/keeper2_9181.txt` → `mntr` `zk_latest_snapshot_size` = 2.4 GiB with `conf` `snapshot_transfer_chunk_size=0`; its `lgif` `last_log_idx` is below the leader's `first_log_idx`; all three members answer *"not currently serving requests"*.
> The member that fell behind can only catch up through a snapshot, and a snapshot over 2 GiB fails to transfer in one piece, so each new leader loses leadership within seconds.
> **Fix:** keep the lagging member stopped and restart the up-to-date members together, so they form a quorum from their own logs. Then set `snapshot_transfer_chunk_size` and `nuraft_snapshot_sync_ctx_timeout_ms` on every member — both need a restart, so restart the serving members first, with writers paused — and only then start the lagging member.

Other Keeper patterns it recognises: the session-expiry cascade after a quorum loss, one replica's Keeper requests slow while its siblings are fine, disk-metadata znodes piling up until Keeper runs out of memory, a background task looping on Keeper. The same holds for parts, merges, memory, mutations, disks and inserts.

The skill works offline and never modifies or uploads the bundle; it applies a privacy check before anything leaves the machine ([`privacy.md`](skills/clickhouse-diagnostic/references/privacy.md)).

## What it collects and why

<details>
<summary>Show</summary>

About 60 collectors, all `system` tables or host facts:

- **Parts, merges and mutations** — part counts per partition and table, merge and mutation backlogs, three days of part events.
- **Replication and Keeper** — replica health, the replication queue, Keeper sessions and request counters, what each Keeper member reports about itself, and (26.3+) who sends Keeper which requests, when and on which paths.
- **Queries** — seven days of activity per hour and per query pattern, failures by error code, the slowest and heaviest patterns.
- **Errors and logs** — error counters and their history, a severity-first slice of `text_log`, the server log files (on-prem).
- **Resources** — background pools, memory and CPU per hour; disks, detached parts, object storage.
- **Schema and settings** — tables, columns, dictionaries, materialized views, clusters; the settings that differ from their defaults; configuration files with credentials removed.
- **The host** (on-prem) — OS, CPU, memory, disks, and the kernel settings ClickHouse warns about.

Every file, the question it answers and why it is in the bundle: [docs/collectors.md](docs/collectors.md).
</details>

## Modes (`-mode onprem | cloud | gov`)

<details>
<summary>Show</summary>

- **`onprem`** — one self-managed server; run it on the server to also collect host facts, logs, configs and Keeper's own answers. A SharedMergeTree cluster is detected and collected across its replicas.
- **`cloud`** — a ClickHouse Cloud service; every per-replica table is read on all replicas (`clusterAllReplicas`).
- **`gov`** — like `onprem`, with database, table, user and host names hashed with your private salt; no dashboard, query text, configs, host facts or logs. The salt and the name-mapping file never leave your machine.

Details: [docs/modes.md](docs/modes.md).
</details>

## Command-line flags

<details>
<summary>The flags you will use most</summary>

| Flag | What it does |
|---|---|
| `-mode onprem\|cloud\|gov` | Which kind of server (see [Modes](#modes--mode-onprem--cloud--gov)) |
| `-host`, `-port`, `-protocol`, `-user` | Where to connect; the password comes from `CLICKHOUSE_PASSWORD` or a prompt |
| `-ca-cert <file>`, `-insecure` | An https server with a self-signed or internal-CA certificate: trust that CA, or skip verification ([details](docs/running.md#self-signed-certificates-https)) |
| `-collect auto\|full\|essential` | How much to collect (see [What this is for](#what-this-is-for)) |
| `-from`, `-to` | Collect this window instead of each table's default look-back |
| `-time-budget`, `-query-timeout` | Cap the whole run and each query |
| `-query-id`, `-normalized-query-hash` | Add a focused analysis of one query |
| `-dry-run` | List what would run; collect nothing |
| `-output-dir` | Where results go (default `./clickhouse_results`) |
| `-salt` | Gov-mode hashing salt |
| `-airgap` | A dashboard that opens without internet |
| `-skip-config`, `-skip-alerts`, `-skip-dashboard`, `-skip-archive` | Leave a step out |

Every flag: `./clickhouse-diagnostic -help`, or [docs/running.md](docs/running.md#command-line-flags).
</details>

## Collection modes in detail

<details>
<summary>How auto decides, and what essential changes</summary>

`auto` runs a short probe (in-memory tables only) and collects essential when `SELECT 1` takes over 1 s, `system.metrics` over 2 s, running queries reach 80 % of `max_concurrent_queries`, memory reaches 90 % of the server limit, the load average passes twice the cores, or replicated tables are read-only. A full run also switches to essential mid-run when a query times out or two in a row take more than 30 s.

Essential runs only the collectors marked `-- tier: essential`, caps every 3- and 7-day window to 1 day, sets a 30 s per-query timeout and a 3-minute budget, and runs with `max_threads = 2`, `max_memory_usage = 1 GiB` and the lowest priority. With Keeper lost, the queries that would wait for a session are skipped.

Details: [docs/running.md](docs/running.md#essential-or-full-collection--collect).
</details>

## Collection window

<details>
<summary>Show</summary>

Each collector has its own default look-back: 7 days for `query_log`, `metric_log` and most log tables, 3 days for `part_log` and a few wide tables, 1 day for `text_log` and the Keeper logs. `-from` / `-to` replace all of them at once; either may be given alone. Alert rules keep their own look-back (for example "in the last hour"), measured within the collected files.

Every collector's window: [docs/running.md](docs/running.md#collection-window).
</details>

## Schema graph

![The schema graph: one database section with its tables, materialized views, dictionary and Distributed table, edges following the data flow, columns listed per node with key columns in red](docs/images/schema-graph.png)

Every `onprem` and `cloud` run also writes **`schema_graph.html`**: an interactive map of how data flows through the schema — tables, materialized views, dictionaries and Distributed tables, with edges from source to target. Click a node for its keys, columns, neighbours and `CREATE` statement; search, filter and drag. It works from disk with no server or network, and opens from the dashboard's *Schema Graph* tab. Details: [docs/dashboard.md](docs/dashboard.md#schema-graph).

## Dry-run

<details>
<summary>Show</summary>

`-dry-run` prints every `SELECT` the tool would send, the system tables each one touches and a read-only `EXPLAIN ESTIMATE` (the rows and parts it would scan), then exits without collecting or writing anything. Details: [docs/running.md](docs/running.md#dry-run-mode).
</details>

## Alerts

<details>
<summary>Show</summary>

19 read-only rules in `alerts/` give the headline before anyone reads a file — too many parts, replicas read-only, Keeper unavailable or flapping, saturated background pools, failing merges, low disk, crashes, long-running mutations, exception spikes and more. Each rule is evaluated over the collected files; its SQL is shown in the dashboard for the customer to run on their own cluster. A rule whose data was not collected says *not applicable*, never clean.

Rules, outcomes and how to add one: [docs/alerts.md](docs/alerts.md).
</details>

## Dashboard

<details>
<summary>Show</summary>

`dashboard.html` is one self-contained page: alerts, storage, query activity, replication, Keeper health, background pools, disks, logs, host facts and (when requested) query analysis. It is built from the bundle's files, says which kind of collection it was and what is missing, and opens from disk. Gov mode does not produce it.

Sections and previews: [docs/dashboard.md](docs/dashboard.md).
</details>

## Query analysis

<details>
<summary>Show</summary>

`-query-id` or `-normalized-query-hash` adds 12 focused collectors for one query: its details and ProfileEvents, its log lines, the tables it read, how it compares with its fast and slow siblings, and its failures over time. Not available in gov mode.

Details: [docs/query-analysis.md](docs/query-analysis.md).
</details>

## Host facts, Keeper and server logs

<details>
<summary>Show</summary>

On-prem runs on the server also collect `host_info.json` (OS, kernel, CPU, memory, disks, limits), the server log files (tail-capped), and what each Keeper member reports about itself through its read-only four-letter commands. Cloud runs skip them; gov keeps Keeper's numbers only.

Details: [docs/bundle.md](docs/bundle.md#host-facts-and-server-logs).
</details>

## Configuration collection

<details>
<summary>Show</summary>

On-prem, the configuration the server actually runs — the merged `config.xml` and `users.xml` from `preprocessed_configs/` — is collected along with the source files in `/etc/clickhouse-server/`, all with passwords, keys and tokens replaced (hostnames, IPs and topology stay, so review it before sharing). `merge_report.txt` lists the settings set differently in more than one file and which value runs, files a failed reload did not pick up, and restart-only settings changed since the last start. Cloud and gov runs do not collect it.

Details: [docs/bundle.md](docs/bundle.md#configuration-collection).
</details>

## Output layout

<details>
<summary>Show</summary>

One folder per run in `clickhouse_results/`, plus its `.tar.gz`: a `<collector>_<ts>.jsonl` per collector, `execution_log.txt` (every query's outcome, time and size), `dashboard.html`, `schema_graph.html`, and the `configuration/`, `logs/`, `keeper/` and `host_info.json` extras.

Details: [docs/bundle.md](docs/bundle.md#output-layout).
</details>

## Supported versions

<details>
<summary>Show</summary>

ClickHouse **22.8 and newer** on-prem and in gov mode; every ClickHouse Cloud version. Newer columns and tables live in version subdirectories (`queries.<mode>/MAJOR.MINOR.PATCH.BUILD/`), picked for the connected server.

Details: [docs/versions.md](docs/versions.md).
</details>

## Building from source

<details>
<summary>Show</summary>

```bash
make build      # → ./bin/clickhouse-diagnostic
make test
make release    # every platform, static binaries, into ./bin/
```

Go 1.23+. Details: [docs/building.md](docs/building.md).
</details>

## Troubleshooting

<details>
<summary>Show</summary>

The most common surprises: a bundle that looks complete but misses the customer's tables (a `SHOW` grant is missing), `Code: 497` in cloud mode (`REMOTE` / `CREATE TEMPORARY TABLE` missing), `x509: certificate signed by unknown authority` over https (`-ca-cert` or `-insecure`), and a collector that failed — every failure is listed with the server's error in `execution_log.txt`.

Details: [docs/troubleshooting.md](docs/troubleshooting.md).
</details>

## Output format, bounds and sizes

<details>
<summary>Show</summary>

Results are JSONEachRow by default (`-output-format jsonl`; `native` and `tsv` exist too, and with them the alerts and dashboard query the server). Every query is bounded on the server, 64-bit integers are written as strings so they are never rounded, and a bundle is typically a few tens of MB — more when server logs are included (each file capped at 50 MiB).

Details: [docs/bundle.md](docs/bundle.md#output-format).
</details>

## Documentation

| Page | What is in it |
|---|---|
| [docs/collectors.md](docs/collectors.md) | Every collected file: the question it answers and why it is in the bundle |
| [docs/running.md](docs/running.md) | Grants, flags, examples, collection modes, the collection window, dry-run |
| [docs/modes.md](docs/modes.md) | onprem, cloud and gov; what gov hashes and withholds; what leaves your machine |
| [docs/alerts.md](docs/alerts.md) | The alert rules, their outcomes, the YAML format, adding a rule |
| [docs/dashboard.md](docs/dashboard.md) | Dashboard sections, the schema graph, previews |
| [docs/query-analysis.md](docs/query-analysis.md) | Analysing one query |
| [docs/bundle.md](docs/bundle.md) | Output format and layout, host facts, Keeper, logs, configuration |
| [docs/versions.md](docs/versions.md) | Supported versions and version-specific queries |
| [docs/building.md](docs/building.md) | Building and releasing |
| [docs/troubleshooting.md](docs/troubleshooting.md) | When a run or a bundle looks wrong |
| [cluster-collect/](cluster-collect/README.md) | Collecting every host of a cluster |
| [airgap/](airgap/README.md) | A dashboard that opens offline |
| [skills/clickhouse-diagnostic/](skills/clickhouse-diagnostic/SKILL.md) | The AI skill that reads a bundle |
