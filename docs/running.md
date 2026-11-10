# Running the tool

*Moved from the main [README](../README.md), which keeps a short version of each section.*

## Usage

> These examples use `./clickhouse-diagnostic`. If you built via `make` the binary is at `./bin/clickhouse-diagnostic` — invoke that path or symlink it. See [Installation](building.md#installation) if you don't have a binary yet.

### Required grants

The tool is read-only and never reads customer tables — every query reads a `system` table. That bounds what it *queries*, not what reaches the bundle: some `system` tables describe customer data in free text (query text, exception messages, log lines), so see [Gov mode and hashed names](modes.md#gov-mode-and-hashed-names) for what is withheld or hashed when that matters. Two grants are enough for `onprem` and `gov` mode:

```sql
CREATE USER sys_read_only IDENTIFIED WITH sha256_password BY '<password>';

GRANT SHOW DATABASES, SHOW TABLES, SHOW COLUMNS ON *.* TO sys_read_only;
GRANT SELECT ON system.*                               TO sys_read_only;
```

```
┌─GRANTS FOR sys_read_only────────────────────────────────────────────────┐
│ GRANT SHOW DATABASES, SHOW TABLES, SHOW COLUMNS ON *.* TO sys_read_only │
│ GRANT SELECT ON system.* TO sys_read_only                               │
└─────────────────────────────────────────────────────────────────────────┘
```

| Grant | Why it is needed |
|---|---|
| `SELECT ON system.*` | Every diagnostic query, alert rule and dashboard panel reads a `system` table. Nothing outside `system` is ever selected. |
| `SHOW DATABASES, SHOW TABLES, SHOW COLUMNS ON *.*` | ClickHouse filters the object-listing system tables down to what the user holds *some* privilege on. Without `SHOW DATABASES`/`SHOW TABLES` the tool sees only part of the cluster; without `SHOW COLUMNS`, `system.columns` comes back with **no rows for your databases** while every other file looks normal. |

> **Do not substitute `SELECT` on the data databases.** `GRANT SELECT ON <db>.*` also populates the listing tables, and collects exactly the same bundle — measured on 26.2.19.43, `system.tables` 2, `system.columns` 5, `system.databases` 2, `system.parts` 1 under either grant set — but it hands the diagnostic user read access to customer data, which the `SHOW` grants do not (a `SELECT` against a user table is refused with 497 under them).

> **A missing `SHOW` grant does not produce an error — it silently truncates the results.** Every query still reports success; the bundle just describes a fraction of the server. Measured on a test instance with one user database:
>
> | Table | With the grant | Without it |
> |---|---|---|
> | `system.tables` | 141 | 112 |
> | `system.databases` | 5 | 1 |
> | `system.columns` | 3258 | 2520 |
> | `system.parts` | 102 | 101 |
>
> This is the failure mode to watch for: a bundle that looks complete but is missing the customer's own tables. If `system.databases` contains only `system`, the `SHOW` grant is missing — the tool now prints a warning at startup when it can see no database outside `system`, but an older binary will not.
>
> With **neither** grant the run still completes: the protected system tables fail with `Code: 497 … Not enough privileges` (visible in `execution_log.txt`) while `system.tables`, `system.columns` and `system.databases` come back as **empty files** rather than errors, so the archive is a few hundred KB that describes nothing. Check the archive size against another node before sending it.

Neither grant exposes customer data: `SHOW` reveals object *names* and metadata only, and `SELECT` is scoped to `system`.

#### Cloud mode needs two more

`cloud` mode wraps its queries in `clusterAllReplicas(...)` so results span every replica rather than only the node you connected to. Every request runs with `skip_unavailable_shards = 1`: a replica that is down is left out instead of failing every per-replica collector, and `system.replicas_answered` in the bundle says which replicas did not answer. Distributed execution needs two further grants:

```sql
GRANT REMOTE                ON *.* TO sys_read_only;
GRANT CREATE TEMPORARY TABLE ON *.* TO sys_read_only;
```

So the full set for cloud is:

```
┌─GRANTS FOR sys_read_only────────────────────────────────────────────────────────────────────────────────┐
│ GRANT SHOW DATABASES, SHOW TABLES, SHOW COLUMNS, CREATE TEMPORARY TABLE, REMOTE ON *.* TO sys_read_only │
│ GRANT SELECT ON system.* TO sys_read_only                                                               │
└─────────────────────────────────────────────────────────────────────────────────────────────────────────┘
```

Unlike the `SHOW` grant, these fail loudly rather than truncating:

```
Code: 497. DB::Exception: Not enough privileges. To execute this query,
it's necessary to have the grant REMOTE ON *.*. (ACCESS_DENIED)

Code: 497. DB::Exception: Not enough privileges. To execute this query,
it's necessary to have the grant CREATE TEMPORARY TABLE ON *.*. (ACCESS_DENIED)
```

`onprem` and `gov` need neither — verified by revoking both and re-running (21/21 and 19/19 queries, 0 errors).

#### What grants do *not* cover

Config collection, `host_info.json` and `logs/` read the **local filesystem**, not the database, so they depend on OS permissions instead: read access to `/etc/clickhouse-server/`, `/var/log/clickhouse-server/` and `/proc`. See [Host facts and server logs](bundle.md#host-facts-and-server-logs).

### Interactive run

```bash
./clickhouse-diagnostic
```

You will be prompted for any value not supplied on the command line:

| Prompt | Default |
|---|---|
| Protocol (http/https) | `http` |
| ClickHouse host | `localhost` |
| ClickHouse port | `8123` (http) / `8443` (https) |
| Username | _empty_ |
| Password (hidden) | _empty_ |
| Mode (cloud/onprem/gov) | `onprem` — only when `-mode` is absent entirely; see below |
| Config directory | `/etc/clickhouse-server/` — `onprem` only; `cloud` and `gov` do not ask (see below) |
| Gov-mode salt (hidden, `gov` mode only) | _empty_ |

#### How `-mode` is resolved

`onprem` is the least protective of the three modes, so the tool never *guesses* it. A `-mode` value it cannot parse is a hard error with a non-zero exit — it is not rounded down to `onprem`, because a mistyped `-mode gov` would then collect unhashed database and table names from a run that asked for hashing, and in a scripted collection nobody reads the warning:

```
$ clickhouse-diagnostic -mode govv ...
Error: invalid mode "govv" — choose cloud, onprem, gov; did you mean "gov"?
$ echo $?
1
```

The `onprem` default applies only when `-mode` is absent altogether. On a terminal you are prompted and Enter accepts it; with stdin closed the default is used and announced:

```
No -mode given and stdin is not a terminal; using onprem.
```

Values are trimmed and lower-cased, and these spellings are accepted:

| Mode | Also accepted |
|---|---|
| `cloud` | `ch-cloud`, `clickhouse-cloud` |
| `onprem` | `on-prem`, `on_prem`, `on-premise(s)`, `onpremise(s)`, `self-hosted`, `selfhosted`, `self_hosted` |
| `gov` | *nothing* — the protective mode takes no alias, so a near-miss is rejected rather than resolved by guesswork |

### Command-line flags

Run `./clickhouse-diagnostic -help` to see the full list. Current flags:

```
-host string           ClickHouse Host
-port string           ClickHouse Port
-user string           Username
-password string       Password (not recommended for security reasons)
-protocol string       Protocol (http or https)
-mode string           Query mode (cloud, onprem, gov) (default "onprem")
-output-dir string     Directory for results output (default "./clickhouse_results")
-config-dir string     ClickHouse configuration directory to collect: config.xml,
                       users.xml and their *.d/ overrides (default:
                       /etc/clickhouse-server/). The merged configuration the
                       server runs is collected from <path>/preprocessed_configs/.
-alerts-dir string     Directory containing alert YAML rule files (default "./alerts")
-salt string           Gov-mode hashing salt (8–64 alphanumeric chars;
                       prompts interactively if empty; gov mode only)
-query-id string       Run query analysis focused on this query_id (UUID).
                       See "Query analysis mode" below.
-normalized-query-hash string
                       Run query analysis focused on this normalized_query_hash
                       (UInt64). Can be combined with --query-id, or used alone.
-from string           Time-window start for query analysis (RFC3339 or YYYY-MM-DD).
                       Defaults to "last 3 days" when --query-id is set, or
                       "last 7 days" when only --normalized-query-hash is set.
-to string             Time-window end for query analysis (default: now).
-analysis-dir string   Directory containing query-analysis SQL files
                       (default "./queries.query_analysis")
-output-format string  Serialisation format for query results:
                       jsonl|native|tsv (default "jsonl").
                       See "Output format" below.
-host-info string      Collect host OS/CPU/memory/disk/process facts:
                       auto|on|off (default "auto" — on for onprem only).
                       See "Host facts and server logs" below.
-logs string           Collect ClickHouse server log files from disk:
                       auto|on|off (default "auto" — on for onprem only).
-logs-dir string       Directory holding server logs (default: discovered
                       from the server config, then /var/log/clickhouse-server)
-logs-max-mb int       Per-file cap for collected logs, in MiB (default 50).
                       Larger files are tail-truncated.
-logs-include-archives Also collect rotated logs (*.gz, *.zst). Off by default.
-keeper-mntr string    Ask every Keeper member in system.zookeeper_connection
                       for its read-only four-letter replies and write keeper/<host>_<port>.txt:
                       auto|on|off (default "auto" — on for onprem and gov,
                       off for cloud). See "Keeper facts" below.
-collect-text-log      Collect a time-bounded slice of system.text_log.
                       Requires --from and --to; rejected in gov mode.
-text-log-level string Minimum severity for --collect-text-log
                       (Fatal|Critical|Error|Warning|Notice|Information|
                       Debug|Trace). Default: all levels.
-text-log-limit int    Row cap for --collect-text-log (default 500000)
-dry-run               List every query that would be executed (with the
                       system tables each touches and a read-only
                       EXPLAIN ESTIMATE per SELECT) and exit. No results,
                       no archive. See "Dry-run mode" below.
-skip-config           Skip collecting configuration files
-skip-alerts           Skip evaluating alert rules
-single-node           On a SharedMergeTree cluster (cloud_mode = 1), keep -mode onprem
                       collecting THIS node only instead of switching to cloud
                       collection over clusterAllReplicas(default, ...)
-skip-dashboard        Skip generating HTML dashboard
-skip-archive          Skip creating archive of results and configuration
```

Any flag left empty on the command line is prompted for interactively (except the `-skip-*` toggles and `-output-dir` / `-alerts-dir`, which use their defaults silently).

### Examples

Run against a Cloud cluster (configuration files are skipped automatically in `cloud` mode: the server's config directory is not on the machine running the tool; pass `-config-dir` to collect one anyway):

```bash
./clickhouse-diagnostic -mode cloud -host my-service.us-east-1.aws.clickhouse.cloud \
  -port 8443 -protocol https -user default
```

Run against an on-prem node, write everything to a custom directory:

```bash
./clickhouse-diagnostic -mode onprem -host prod-ch-01 \
  -output-dir ./diagnostics-2026-05-13
```

Run a quick check with only queries (no alerts, no dashboard, no archive):

```bash
./clickhouse-diagnostic -mode onprem -host localhost \
  -skip-alerts -skip-dashboard -skip-archive
```

Government / hashed-PII mode with a non-default alerts directory:

```bash
# Salt is prompted interactively (hidden input). To pass it explicitly:
./clickhouse-diagnostic -mode gov -host gov-ch-01 \
  -alerts-dir ./alerts.gov -salt myDeployment2026
```

> **Gov mode requires a salt** — see [Gov mode and hashed names](modes.md#gov-mode-and-hashed-names) for what it does and why you need it.

Fully non-interactive (CI / automation):

```bash
./clickhouse-diagnostic -mode cloud \
  -protocol https -host $CH_HOST -port 8443 \
  -user $CH_USER -password $CH_PASS \
  -skip-config
```

> **Security:** prefer the interactive password prompt or an environment variable over `-password` — flags appear in `ps`/shell history.

## Essential or full collection (`-collect`)

A server in trouble is exactly when a diagnostic is needed, and exactly when a full collection costs the most. Before collecting, the tool runs a short load probe (about a second, in-memory tables only) and picks how much to collect:

| `-collect` | What happens |
|---|---|
| `auto` (default) | **Full** collection, unless the probe sees a struggling server — `SELECT 1` slower than 1 s, `system.metrics` slower than 2 s, running queries at 80 % of `max_concurrent_queries`, memory at 90 % of the server limit, load average above 2× the cores, or read-only replicated tables. Then **essential**. A full run also switches to essential **mid-run** when a query times out or two in a row take more than 30 s. |
| `essential` | Always essential. |
| `full` | Always full — no switch, whatever the probe says. |

An **essential** collection runs only the collectors marked `-- tier: essential` (version, metrics, processes, errors, replicas and replication queue, merges, mutations, Keeper session, disks, the last day of `query_log` / `part_log` / `metric_log` / `error_log` / `text_log`), with:

- every 3- and 7-day window reduced to **1 day** (an explicit `-from` / `-to` still wins);
- a **30 s** per-query timeout (unless `-query-timeout` was given) and a **3 min** budget for the whole run (`-time-budget` to change it) — what has not run by then is skipped and listed;
- `max_threads = 2`, `max_memory_usage = 1 GiB` and the lowest `priority`, each checked first and left out if the user's settings profile refuses it;
- the dashboard panels that read every table left out; an alert rule whose source file was not collected is reported as *not applicable*, naming the skipped collector.

When the probe sees **no Keeper session while replicated tables are read-only**, the queries that wait for a session (`system.zookeeper_connection`, `system.database_replicas`, `system.replicas_problem_tables`, the dashboard's Keeper panel — and with them the alerts that read those files) are skipped instead of waiting for their timeout, and the Keeper four-letter commands take their members from the server configuration.

On a **large SharedMergeTree** server (10 000+ tables), an `auto` run lists detached parts only for non-shared tables and the shared tables named in broken-part log lines; `-collect full` lists every table.

The decision and its reasons are the `collect:` line of `execution_log.txt`, a banner at the top of `dashboard.html`, and the first finding of the analysis skill's pre-pass. Every skipped collector is in the execution log with status `skipped` and why.

## Collection window

Most collection queries look back over a fixed period. Each declares its **own** default, so a query whose scan is unusually expensive (or unusually cheap) can say so without changing the rest:

| Query | Default look-back |
|---|---|
| `system.query_log_details_7_days` | 7 days |
| `system.query_log_exceptions_1_day` | 1 day |
| `system.query_log_by_hour_7_days` | 7 days |
| `system.query_log_by_pattern_7_days` | 7 days |
| `system.part_log_3_days` | 3 days |
| `system.query_views_log_3_days` | 3 days |
| `system.metric_log_7_days` | 7 days |
| `system.asynchronous_insert_log_7_days` | 7 days |
| `system.metric_log_coordination_3_days` | 3 days |
| `system.metric_log_by_host_3_days` | 3 days |
| `system.blob_storage_log_7_days` | 7 days |
| `system.error_log_7_days` | 7 days |
| `system.distributed_ddl_queue` | 7 days |
| `system.text_log` | 1 day |
| `system.text_log_histogram_1_day` | 1 day |
| `system.text_log_keeper_1_day` | 1 day |
| `system.zookeeper_log_errors_1_day` | 1 day |
| `system.aggregated_zookeeper_log_by_hour_2_days` | 2 days |
| `system.aggregated_zookeeper_log_by_minute_1_day` | 1 day |
| `system.aggregated_zookeeper_log_paths_1_day` | 1 day |

> `system.text_log` and `system.zookeeper_log` stop at 1 day: they are by far the highest-volume tables here (a busy cluster writes millions of Keeper log rows an hour), and a 7-day slice is too large to be useful in a support bundle. Use `-from` when you need more.

`-from` and `-to` override **every** window at once:

```bash
# Everything from a specific incident window
./clickhouse-diagnostic -mode onprem -host prod-ch-01 \
  -from 2026-08-14T09:00:00Z -to 2026-08-14T13:00:00Z

# Widen the look-back to 30 days instead of each query's default
./clickhouse-diagnostic -mode onprem -host prod-ch-01 -from 2026-07-21
```

Either end may be given alone: `-from` with no `-to` runs to now, and `-to` with no `-from` leaves each query's own default start. The resolved window is printed at the start of the run. Times are RFC3339 or `YYYY-MM-DD`, interpreted as UTC.

The same flags also drive [query analysis](query-analysis.md#query-analysis-mode) when `-query-id` / `-normalized-query-hash` is set.

[Alert rules](alerts.md#alerts) keep their own look-back (e.g. *"exception spike in the last hour"*) — it is part of what the rule means. They read the collected files, so with `-to` that look-back ends at `-to`, where the files end; a `-from` narrower than a rule's look-back gives it only the hours collected.

> **A narrower window is the first thing to try on a slow or heavy collection.** The defaults are tuned for a general health check; on a busy cluster `-from` with a few hours is dramatically cheaper.

### For query authors

Windows are template placeholders, not literals, so they stay overridable:

```sql
WHERE event_time >  {from:7d}      -- --from if given, else now() - INTERVAL 7 DAY
  AND event_time <= {to:now}       -- --to   if given, else now()
```

Accepted defaults are `now`, and `<N>d` / `<N>h` / `<N>m`. A malformed spec (`{from:7dd}`) is **not** substituted — the query is refused rather than silently run unbounded. A test asserts no shipped query hard-codes an interval, so a new one that does will fail CI rather than quietly ignore the flags.

## Dry-run mode

Security-conscious customers can pass `-dry-run` to see exactly which queries the tool would execute, against which tables, **without** any actual data collection:

```bash
./clickhouse-diagnostic -mode cloud -host my-service.region.aws.clickhouse.cloud \
  -port 8443 -protocol https -user default \
  -dry-run
```

What `-dry-run` does:

- Lists every SELECT that would be sent (file-based, query-analysis bundle, and any alert rule without a `check:`; the shipped alert rules and the dashboard read the collected files and send none)
- Tags each query with the `system.*` tables it touches
- Adds a read-only `EXPLAIN ESTIMATE` block under each SELECT — reports the rows/marks/parts the query **would** scan but reads no data parts. See [the EXPLAIN ESTIMATE docs](https://clickhouse.com/docs/sql-reference/statements/explain#explain-estimate).
- Renders empty estimates as `this table is empty` (the planner's confirmation that nothing matches the predicate)
- Skips `-skip-config` and `-skip-archive` automatically (no side-effect files)

What still reaches the server in dry-run:

| Call | Why |
|---|---|
| `SELECT version()` | Picks the right query variant for the server version |
| Pre-flight for `--query-id` / `--normalized-query-hash` | Derives the hash + event_time (or the slowest query_id) so the printed analysis SQL has real values, not unbound `{query_id}` markers |
| `SELECT DISTINCT host, port FROM system.zookeeper_connection` (`-keeper-mntr`) | Lists the Keeper members so the dry run can say which it would contact; printed when it runs |
| `EXPLAIN ESTIMATE <query>` per SELECT | Read-only metadata only |

Combine with the query-analysis flags to dry-run the focused bundle too:

```bash
./clickhouse-diagnostic -mode cloud -host ... \
  -dry-run \
  -normalized-query-hash 15477159632099527852
```

Sample block of the output:

```
[28]
    Tables: system.query_log
    SQL:
      SELECT ts, query_id, query_duration_ms, ...
      FROM system.query_log
      WHERE query_id = '30df7836-fe07-4f42-9d52-832a69abbb8b'
        AND event_time >= '2026-05-28 09:06:21'
        AND event_time <= '2026-06-04 09:06:21'
    EXPLAIN ESTIMATE:
      ┌─database─┬─table─────┬─parts─┬─rows─┬─marks─┐
      │ system   │ query_log │     2 │   14 │     2 │
      └──────────┴───────────┴───────┴──────┴───────┘
```
