# Privacy — what a bundle reveals and what may leave the machine

A diagnostic bundle is **evidence about someone's production system**. The collector never reads customer *rows*, but several `system` tables describe customer data in free text. Treat the extracted bundle and everything derived from it as confidential until the owner says otherwise.

## What is in the bundle even after sanitisation (cloud / onprem modes)

| Identifier class | Where it appears |
|---|---|
| Hostnames, IPs, ports, cluster topology, replica/shard macros | `system.clusters`, `system.replication_queue.replica_name`, `configuration/` (`<remote_servers>`, `<zookeeper>`, `<macros>`, `<interserver_http_host>`), `host_info.os.hostname`, `logs/` |
| Database, table, column, dictionary, disk and storage-policy names | almost every file; `system.tables.create_table_query` carries the full DDL including comments and defaults |
| SQL text (500 chars per query pattern) and full SQL in `query_analysis/` | `system.query_log_details_7_days.query`, `query_details`, `failed_queries.sample_query`, `executions_timeline` |
| Exception messages — often embed literal values, paths, hostnames | `query_log_details.exception`, `system.errors.last_error_message`, `replication_queue.last_exception`, `dictionaries.last_exception`, `part_log.exception` |
| Server log lines (raw queries, table names, paths, client addresses) | `system.text_log`, `text_log_<ts>`, `logs/*.log` |
| Process command lines, mount points, kernel banner | `host_info.top_processes_by_rss`, `host_info.disks`, `host_info.os` |
| User names and client addresses | `system.processes`, `query_log_details.user`, `system.clusters.user` |
| Stack traces (function names, build id) | `system.stack_trace`, `system.crash_log` |

What sanitisation **does** remove from `configuration/`: passwords, hashes, tokens, keys, secrets, PEM blocks, long hex/base64 blobs, credentials in URLs. It does **not** remove identifiers. Assume a config file identifies the organisation immediately.

Gov mode hashes database/table/user/host names and withholds text, logs, host facts and config. It is the right choice when the bundle must cross an organisational boundary.

## Rules for the assistant

1. **Analysis stays local.** Read the bundle from disk. Do not upload it, paste large excerpts into web services, or send it to any API that is not the one you are already running in. Never run `curl`/`wget` with bundle content.
2. **Quote the minimum.** In findings, cite `file → column → value` for the few rows that carry the evidence (3–10 rows), truncate query text to 150–300 characters, and never paste whole files or `configuration/` XML.
3. **Never paste verbatim** `configuration/`, `logs/`, `host_info.json`, `system.stack_trace`, `system.crash_log.trace_full`, or `create_table_query` blocks into anything that leaves the machine (chat transcripts that are shared, tickets, pastebins). Summarise, or redact first.
4. **Gov artefacts:** the `*_gov_name_mapping.csv` and the salt must never leave the customer's machine. Never ask for the salt. If the user offers the mapping CSV, use it locally only to label findings.
5. **Redact before sharing.** When the user wants to send the summary to someone else (support ticket, colleague, forum), run the checklist below and replace identifiers with placeholders (`<db>`, `<table>`, `<host-1>`, `<user>`).
6. **Do not correlate across bundles from different organisations.** Each bundle is analysed on its own.
7. **Do not fabricate identifiers.** If a name is hashed or withheld, say so — never guess a table name from a hash or a query fragment.

## Redaction checklist (before any text leaves the machine)

Search the draft for and replace:

- Hostnames / FQDNs / IPs / ports: `[a-z0-9-]+\.[a-z0-9.-]+`, `\b\d{1,3}(\.\d{1,3}){3}\b`
- Database/table/column names that are not `system.*`
- User names (`default` is fine; anything else is not)
- Query text beyond the shape needed to explain the finding (keep `SELECT ... FROM <table> WHERE <col> = ? GROUP BY ...` shapes; drop literals)
- Exception messages containing literals, paths or hostnames
- Paths under `/var/lib/clickhouse`, mount points, device names
- Anything from `configuration/` other than setting names and numeric values
- The bundle's own timestamps are fine; the organisation name (often visible in database names or `os.hostname`) is not

## What is safe to share as-is

- The health scorecard (area → status → one-line evidence with numbers)
- Setting names and their values (`parts_to_throw_insert = 3000`, `max_memory_usage = 10 GiB`)
- Error codes, names and counts
- Part counts, sizes, durations, rates, percentages
- ClickHouse version and the public changelog/issue links you cite
- Recommendations phrased in terms of settings, patterns and public documentation

## When the user is ClickHouse support

The same rules apply; the bundle owner's consent covers ClickHouse support systems only. Do not copy bundle content into public GitHub issues, community Slack, or LLM services outside the company boundary.
