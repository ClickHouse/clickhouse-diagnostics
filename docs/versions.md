# Supported versions and version-specific queries

*Moved from the main [README](../README.md), which keeps a short version of each section.*

## Version-specific queries

Inside each mode directory, you can override a query for a specific ClickHouse version by placing it in a subdirectory named `MAJOR.MINOR.PATCH.BUILD`. The tool picks the highest version ≤ the connected server.

```
queries.onprem/
├── system.detached_parts.sql     # default (works from 22.8)
├── 22.11.1.0/
│   └── system.detached_parts.sql # adds bytes_on_disk/path (servers ≥ 22.11)
└── 23.11.1.0/
    └── system.detached_parts.sql # also adds modification_time (servers ≥ 23.11)
```

Directories that don't parse as a version are skipped.

The same convention applies to `queries.query_analysis/` (`.sql` files) and `alerts/` (`.yaml` rules): place a file with the same name in a `MAJOR.MINOR.PATCH.BUILD` subdirectory to override the root version for servers at or above that version.

```
alerts/
├── detached_parts_exist.yaml     # default rule (works from 22.8)
└── 22.11.1.0/
    └── detached_parts_exist.yaml # adds bytes_on_disk (column added in 22.11)
```

A file that exists **only** in a version subdirectory (no root counterpart) is simply skipped on older servers — use this for queries against system tables that don't exist yet (e.g. `system.asynchronous_insert_log`, added in 22.10).

> **Overrides match by base filename, case-sensitively.** A file in a version subdirectory overrides a root file only when their names are identical, including case. If you rename the file in the version directory — or just change its case (`Foo.yaml` vs `foo.yaml`) — it is treated as a *separate* query/rule (both run), not an override. Keep the filename byte-for-byte identical to the root when you intend to override it.

## Supported ClickHouse versions

The tool targets **ClickHouse 22.8 and newer** for on-prem servers. Root-level query and alert files stick to columns and syntax available in 22.8; anything newer lives behind a version subdirectory. Current gates (verified against the ClickHouse changelogs):

| Feature | Added in | Gated at |
|---|---|---|
| `ARRAY JOIN` over a `Map` (`ProfileEvents`) | after 22.8 | roots use `mapKeys()`/`mapValues()` (all versions) |
| `system.asynchronous_insert_log` table | 22.10 | `queries.{onprem,gov}/22.10.1.0/` |
| `system.disks.unreserved_space` | 22.10 | `queries.{onprem,gov}/22.10.1.0/` |
| `system.detached_parts.bytes_on_disk`, `path` | 22.11 | `queries.{onprem,gov}/22.11.1.0/`, `alerts/22.11.1.0/` (gov gates `bytes_on_disk` only — it never collects `path`/`disk`) |
| `GROUP BY ALL` syntax | 22.12 | root files use explicit key lists |
| `dateDiff('millisecond', …)` sub-second unit | after 22.12 | async latency uses float subtraction of `*_microseconds` |
| `system.text_log.message_format_string` | 23.1 | `queries.query_analysis/23.1.1.0/` |
| `system.server_settings` table | 23.3 | `queries.{onprem,gov}/23.3.1.0/` (no root file — skipped below 23.3; cloud carries it at root) |
| `system.asynchronous_insert_log.rows` | 23.4 | `queries.{onprem,gov}/23.4.1.0/` (22.10–23.3 report `bytes`) |
| `system.settings.default` | 23.4 | `queries.{onprem,gov}/23.4.1.0/` (`default` is the only column their roots omit; the cloud root has it) |
| `system.clusters` replicated-db columns (`database_shard_name`, `database_replica_name`, `name`; `is_active` deliberately not selected) | 23.5 | `queries.*/23.5.1.0/` |
| `system.query_log.query_cache_usage` | 23.8 | `queries.query_analysis/23.8.1.0/` |
| `system.zookeeper_connection` table | 23.8 | `queries.*/23.8.1.0/` (no root file — skipped below 23.8 in every mode) |
| `system.zookeeper_connection.xid`, `last_zxid_seen`, `availability_zone` | 24.3 | `queries.*/24.3.1.0/`; `alerts/24.3.1.0/keeper_xid_renewal_due.yaml` (no root rule — not applicable below 24.3) |
| `system.query_log.peak_threads_usage` | 23.9 | `queries.query_analysis/23.9.1.0/` |
| `hostname` column in system log tables | 23.11 | `queries.*/23.11.1.0/` (roots use `hostName()`) |
| `system.blob_storage_log` table (needs `<blob_storage_log>` config) | 23.11 | `queries.*/23.11.1.0/` (no root file — skipped below 23.11 in every mode) |
| `system.tables.total_bytes_uncompressed` | 23.12 | `queries.query_analysis/23.12.1.0/` |
| `system.view_refreshes` table | 23.12 | `queries.*/23.12.1.0/` (no root file — skipped below 23.12 in every mode) |
| `system.mutations.is_killed` | 24.1 | `alerts/24.1.1.0/` (root omits the filter) |
| `background_*_pool_size` moved from `system.settings` to `system.server_settings` | 23.3 | `alerts/23.3.1.0/` (`fetch_pool_saturated`, `schedule_pool_saturated`; `system.settings` still lists the names on newer servers with the old session defaults, so the root rule would compare against the wrong size there) |
| `system.tables.metadata_version` | 24.2 | `queries.*/24.2.1.0/` |
| `system.error_log` table | 24.8 | `queries.*/24.8.1.0/` (no root file — skipped below 24.8 in every mode) |
| `system.database_replicas` table | 25.9 | `queries.*/25.9.1.0/` (no root file — skipped below 25.9 in every mode) |
| `system.aggregated_zookeeper_log` with `component` (26.2) and `is_subrequest` (26.3) | 26.3 | `queries.*/26.3.1.0/` (no root file — skipped below 26.3 in every mode: the table exists from 25.9, but before 26.3 sub-requests share buckets with standalone requests and carry the whole Multi's latency) |
| `system.zookeeper_log.duration_microseconds` (replaces `duration_ms`) | 24.3 | `queries.*/24.3.1.0/` (roots use `duration_ms`; output stays in ms on every rung) |
| `system.tables.parameterized_view_parameters` | 25.4 | `queries.{onprem,cloud}/25.4.1.0/` (gov: not collected) |
| `system.tables.target_database`, `target_table` (an MV's write target, including the implicit `.inner_id.*` table) | 26.6 | `queries.*/26.6.1.0/` (gov hashes both) |

The dashboard (`internal/dashboard/generator.go`) builds its SQL dynamically, so instead of version directories it probes the live schema at runtime (`hasColumn`/`hasTable`) and adapts each panel — covering the same columns (`error_count`, `is_killed`, `bytes_on_disk`, the async table/`rows`, `crash_log`) plus optional tables that may be disabled by config.

`queries.cloud/` targets managed ClickHouse Cloud, which always runs recent versions — it carries the recent rungs (`23.5.1.0/`, `23.11.1.0/`, `24.2.1.0/`, `25.4.1.0/`) but none of the sub-23.5 gating, because Cloud never runs that old. The **22.8 floor applies to `queries.onprem/` and `queries.gov/`** (both are self-hosted — gov is on-prem with hashed PII, and neither uses `clusterAllReplicas`) as well as the shared `queries.query_analysis/` + `alerts/` directories. Note gov's `system.tables` ladder deliberately tops out at `24.2.1.0/` — it does not collect `parameterized_view_parameters` (identifier-like values gov would otherwise have to hash).

Overrides are matched **only** at the top level of each directory; a version subdirectory nested deeper (e.g. `alerts/foo/25.4.1.0/`) is not treated as a nested override of `alerts/foo/`.
