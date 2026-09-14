# AGENTS.md — guidance for coding agents (Codex, Claude Code, others)

This repository is `clickhouse-diagnostic`: a Go CLI that collects ClickHouse `system.*` tables, alert results, host facts, sanitised configs and server logs into a `clickhouse_backup_<ts>.tar.gz` support bundle, plus a self-contained `dashboard.html`. `README.md` is the reference for the tool itself.

## Analysing a bundle or running the tool

Read and follow `skills/clickhouse-diagnostic/SKILL.md` before touching a bundle. It defines the workflow (coverage → triage → health checks → patterns → summary → privacy gate), the reference files to load lazily (`skills/clickhouse-diagnostic/references/`), and a stdlib-only pre-pass script:

```bash
python3 skills/clickhouse-diagnostic/scripts/inspect_bundle.py <clickhouse_backup_*.tar.gz | dir>
```

The same skill is exposed to Claude Code via `.claude/skills/clickhouse-diagnostic` and to Codex via `.agents/skills/clickhouse-diagnostic` (both symlinks to `skills/clickhouse-diagnostic`).

## Rules that apply to every agent here

- Bundles are confidential customer evidence: analyse locally, quote the minimum, never upload or paste identifiers outside the machine without the checklist in `skills/clickhouse-diagnostic/references/privacy.md`. Never modify a bundle.
- No network calls with bundle content. Version research against GitHub is allowed only when the user asks and only with version strings, setting names and error texts.
- Do not commit, push or open PRs unless the user explicitly asks for it in the current session.
- Go code: `make test` must pass; query files live under `queries.<mode>/` with version subdirectories (`MAJOR.MINOR.PATCH.BUILD/`); alert rules under `alerts/` must remain read-only `SELECT`s. See `README.md` → *Version-specific queries* before adding SQL.
