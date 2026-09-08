# Summary template — what the analysis output looks like

Produce the summary inline (markdown) unless the user asks for a file. Keep it scan-friendly: a reader should get the verdict in 10 seconds and the evidence in 2 minutes. Every finding cites its evidence as `file → column → value`. Write measurements, not attributions (see §Rules).

```markdown
# ClickHouse diagnostic summary — <bundle folder name>

**Coverage.** ClickHouse <version> · mode <cloud|onprem|gov> · collected <run timestamp, tz> · query_log window <min> → <max> (<N> hour-buckets) · text_log <N> rows covering <first>–<last> · files missing/empty: <list or none> · host facts: <describe the server | not collected | describe another machine (ignored)> · logs: <files, truncated?>.
<If the user's incident is outside the window, say so here and point to the re-collection command.>

## Headline
- <one line: the most important finding, with the number>
- <one line>
- <one line — or "No critical or warning findings; details below.">

## Health scorecard
| Area | Status | Evidence |
|---|---|---|
| Availability & crashes | ok / warning / critical / n/a | `system.crash_log`: 0 rows |
| Parts & merges | … | `system.parts`: <db>.<table> partition <id> 812 active parts (alert threshold 300) |
| Replication & Keeper | … | `system.replicas`: max absolute_delay 4 s; `metric_log.zk_hw_exceptions` 0 |
| Disk & storage | … | `system.disks`: default 71 % free; `system` db 38 GiB (3rd largest) |
| Memory & CPU | … | `metric_log.avg_memory_tracking_bytes` peak 11.2 GiB of 16 GiB RAM; load 0.3/8 CPUs |
| Query workload | … | 241 = 3.1 % of queries (1 240/40 000), one hash accounts for 92 % |
| Inserts | … | `part_log` NewPart 3 900/h, avg 240 rows/part |
| Mutations & TTL | … | 2 mutations, oldest 6 h, parts_to_do 3 |
| Dictionaries | … | 4 LOADED, 0 FAILED |
| Config & host tunables | … | THP = always (warning); overcommit 0; nofile 500000 |
| Logs | … | err.log: top code 241 (312), 999 (0); 0 Fatal |

## Findings (most severe first)
### F1 — <title, symptom-first>  ·  <critical|warning|info>  ·  HC-2.1, P-01
**What:** <one or two sentences in plain language>
**Evidence:**
- `system.parts_*.jsonl` → active parts in <db>.<table>/<partition_id>: 812 (level 0: 540)
- `system.part_log_7_days_*.jsonl` → NewPart 3 900/h vs MergeParts 41/h on 2026-08-25 10:00–14:00
- `system.metric_log_7_days_*.jsonl` → max_merge_pool_tasks = 16 (pool size) for 6 consecutive hours
**Why it matters:** <consequence, with the threshold that will bite next: "inserts are delayed from 1000 parts and rejected at 3000 (`parts_to_throw_insert`)">
**Recommended action:** <ordered, concrete, with settings + defaults; distinguish immediate mitigation from durable fix>
**Confidence:** high | medium | low — <why; what would confirm it>

### F2 — …

## Recommendations (by impact / effort)
1. <action> — addresses F1, F3 — low effort
2. …

## Suggested follow-up prompts
- "Analyse the query pattern with normalized_query_hash <hash> — rerun the tool with `--normalized-query-hash <hash> -from … -to …`, identify the shape (MergeTree read, table function, worker sub-query of a distributed statement), build the time budget from ProfileEvents, and only then compare slow vs fast runs."
- "Review the ORDER BY and partition key of <db>.<table> against the top 5 query shapes in this bundle and propose a schema."
- "Explain the MEMORY_LIMIT_EXCEEDED messages by class (query / total / user / merge) and map each to a setting."
- "Compare insert rate (NewPart/h) to merge throughput per table and propose batch sizes."
- "Draft TTL and merge-setting changes for the tables with the most parts, with rollback notes."
- "Which system log tables have no TTL and how much disk they use; write the config change."
- "Given version <v>, list backward-incompatible changes and relevant bug fixes between <v> and the latest patch of this minor / the latest LTS." (network)
- gov: "Join these hashed table names with my local mapping CSV and relabel the findings" (runs locally).

## What to re-collect (only if needed)
- <exact command from running-the-tool.md §8, with the reason>

## Caveats
- <coverage limits, hashed names, estimates, anything inferred rather than measured>
```

## Rules for the text

- **Measurement vs attribution.** Every causal sentence must be backed by a measured fact at a stated confidence. "Memory peaked at 11.2 GiB while 2 merges of `<table>` ran (peak_memory_usage 4.8 GiB each)" is a measurement; "the merges caused the OOM" is an attribution — phrase it as "consistent with" until a second source confirms it.
- **Numbers over adjectives.** "812 parts (threshold 300)" not "many parts". Always show the threshold you are comparing against and where it comes from (alert rule, ClickHouse default, guideline).
- **One paragraph = one line.** No manual wrapping inside paragraphs or bullets; the renderer wraps. Short paragraphs (≤ 3 sentences).
- **Cite files, not feelings.** Each evidence bullet starts with the bundle file (glob form is fine) and the column(s).
- **Version-aware.** Quote settings with the default for *this* version; when unsure, say "verify in `Settings.cpp` at tag <v>" rather than guessing.
- **Language for bugs/issues:** "resembles", "consistent with", "a fix was merged in PR #N (first in vX.Y) — worth confirming"; never "known issue" (see `clickhouse-source.md` §4).
- **No customer-facing copy unless asked.** If asked, add a `**Suggested reply:**` header followed by a fenced block with the reply body (so it pastes cleanly), keep it factual, and run the privacy checklist first (`privacy.md`).
- **Links** as `[title](url)` to public docs/GitHub only; omit the section if nothing directly applies.
- **Brevity.** Headline ≤ 3 bullets; scorecard one line per area; ≤ 7 findings (fold the rest into a "Also observed" list); recommendations ≤ 7.

## Optional: HTML companion

If the user wants a shareable page next to `dashboard.html`, render the same markdown to a standalone `summary.html` (no external assets) in the bundle's *parent* directory, not inside the bundle folder — the archive should stay exactly what the tool produced. Redact per `privacy.md` before doing so.
