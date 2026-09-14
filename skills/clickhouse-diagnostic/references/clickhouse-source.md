# Pinning the customer's ClickHouse version to source, changelog and issues

Only needed when the user wants version research (network required). The bundle always carries an exact version (`system.version_*.jsonl`, also `dashboard.html → DATA.version` and the startup banner in `logs/clickhouse-server.log`). Use it — never reason from "the latest ClickHouse".

## 1. Version string → git tag

`version()` returns `MAJOR.MINOR.PATCH.BUILD`, e.g. `25.3.2.39`. The release tag is `v25.3.2.39-lts` or `v25.3.2.39-stable` (older: `-prestable`, `-testing`). Resolve without guessing:

```bash
V=25.3.2.39
gh api "repos/ClickHouse/ClickHouse/git/matching-refs/tags/v$V" --jq '.[].ref'        # → refs/tags/v25.3.2.39-lts
gh release view "v$V-lts" -R ClickHouse/ClickHouse --json tagName,publishedAt,url   # release notes for that patch
```
Without `gh`: open `https://github.com/ClickHouse/ClickHouse/tags?q=v25.3.2` in a browser.

Cloud versions look like `26.2.1.558`; the Cloud build may include changes not in the OSS tag — say so when the bundle is from ClickHouse Cloud (mode `cloud`).

Release cadence: a new minor every month (`YY.M`); every March (`YY.3`) and August (`YY.8`) minor is an **LTS** supported for 12 months, other minors for 3. An on-prem server on a non-LTS minor older than ~3 months is out of support for fixes — worth stating in the summary.

## 2. Which source file answers which question

Read files at the customer's tag: `https://github.com/ClickHouse/ClickHouse/blob/<tag>/<path>` (or `gh api repos/ClickHouse/ClickHouse/contents/<path>?ref=<tag> --jq .content | base64 -d`).

| Question | File at `<tag>` |
|---|---|
| What is error code N called / does it exist on this version? | `src/Common/ErrorCodes.cpp` |
| Does query setting X exist, what is its default, when did it change? | `src/Core/Settings.cpp` (≤ 24.x: `src/Core/Settings.h`); history in `src/Core/SettingsChangesHistory.cpp` |
| Does server-level setting X exist (config.xml keys like `max_server_memory_usage`, `background_pool_size`)? | `src/Core/ServerSettings.cpp` (≤ 24.x: `ServerSettings.h`) |
| MergeTree table setting X (`parts_to_throw_insert`, `ttl_only_drop_parts`, `min_age_to_force_merge_seconds`…) — default and meaning | `src/Storages/MergeTree/MergeTreeSettings.cpp` (≤ 24.x: `MergeTreeSettings.h`) |
| Which startup warnings does the server emit (THP, overcommit, low memory, `max_map_count`)? | `programs/server/Server.cpp` (search `warnings` / `Server::main`) |
| What does the "Too many parts" message and its thresholds look like? | `src/Storages/MergeTree/MergeTreeData.cpp` (`delayInsertOrThrowIfNeeded`) |
| Merge selection / why small parts are not merged | `src/Storages/MergeTree/MergeTreeDataMergerMutator.cpp`, `src/Storages/MergeTree/Compaction/` (≥ 25.x) |
| Mutation head-of-line behaviour, parts locked by merges | `src/Storages/MergeTree/Compaction/MergePredicates/MergeTreeMergePredicate.cpp`, `src/Storages/StorageReplicatedMergeTree.cpp` |
| Which `system.*` columns exist on this version | `src/Storages/System/StorageSystem<Table>.cpp` (e.g. `StorageSystemParts.cpp`, `StorageSystemReplicas.cpp`) |
| ProfileEvents / CurrentMetrics names and meaning | `src/Common/ProfileEvents.cpp`, `src/Common/CurrentMetrics.cpp`, `src/Interpreters/ServerAsynchronousMetrics.cpp` |
| Keeper client-side session/timeout behaviour | `src/Common/ZooKeeper/ZooKeeperImpl.cpp`, `src/Common/ZooKeeper/ZooKeeperArgs.cpp` |

Rule (non-negotiable): **do not describe, confirm or infer the behaviour or default of a setting you have not found in source or official docs for that version.** If it is not in `Settings.cpp` / `ServerSettings.cpp` / `MergeTreeSettings.cpp` at the tag, say "not present on this version".

Public docs: `https://clickhouse.com/docs/operations/settings/settings#<setting>`, `/docs/operations/server-configuration-parameters/settings`, `/docs/operations/settings/merge-tree-settings`. Docs describe the *current* version — for an older server prefer source at the tag.

## 3. Changelog: what changed between the customer's version and a candidate upgrade

- OSS changelog: `https://github.com/ClickHouse/ClickHouse/blob/master/CHANGELOG.md` (one section per minor, with **Backward Incompatible Change**, **New Feature**, **Performance Improvement**, **Bug Fix**). Docs mirror: `https://clickhouse.com/docs/whats-new/changelog`.
- Per-patch release notes: `gh release view v25.3.3.42-lts -R ClickHouse/ClickHouse` — lists the backported bug fixes with PR numbers. Diff two patches of the same minor by reading the releases in between.
- Cloud changelog: `https://clickhouse.com/docs/whats-new/cloud`.
- When a finding "resembles a fixed bug", state the fix PR, the first version that carries it, and whether the customer's version is before or after it. Phrase it as in §4 below.

## 4. Searching issues and citing them — mandatory protocol

Search:
```bash
gh issue list -R ClickHouse/ClickHouse --state all --search '"<exact error text>" label:bug' --limit 10
gh issue list -R ClickHouse/ClickHouse --state all --search 'TOO_MANY_PARTS merges not scheduled 25.3' --limit 10
gh pr list    -R ClickHouse/ClickHouse --state merged --search 'fixes #<issue>' --limit 5
```
Lead with the **exact error code/name or message fragment** — it beats symptom phrasing. Cast a wide net with 3–4 short queries, then filter.

Before citing any issue, complete all four steps (adapted from the support methodology this skill inherits):

1. Read the full issue body and the key comments — not just the title.
2. Find the associated fix PR/commit (`fixes #N`, `closes #N`) and the **first version** that includes it (release notes or `gh pr view <n> --json milestone,labels`; Cloud backports may differ).
3. Check whether the customer's exact version is **before** the fix.
4. Verify the customer's scenario satisfies the issue's distinguishing conditions (engine, setting, data type, feature in use) — from the bundle, not by assumption.

If any step cannot be completed, say so explicitly.

Language rules — what you may write:

| Situation | Allowed | Not allowed |
|---|---|---|
| Symptom similarity only | "this pattern resembles ClickHouse/ClickHouse#NNNN" | "you are hitting #NNNN" |
| All four steps done, conditions match | "the evidence is consistent with #NNNN; a ClickHouse engineer should confirm" | "this is a known bug tracked in #NNNN" |
| A fix exists in a specific version | "a fix was merged in PR #MMMM (first in vX.Y.Z); worth confirming whether it applies" | "this was fixed in X.Y" |
| No fix / issue still open | "this may be related to open issue #NNNN" | "this is a known issue" |

Never write "known issue" on the strength of pattern matching. Never invent issue numbers — only cite ones you have opened and read in this session.

## 5. Useful public references to hand back to the user

- Docs: `https://clickhouse.com/docs/` (operations, settings, merge-tree, replication, keeper, TTL, async inserts, backups)
- Knowledge base articles: `https://clickhouse.com/docs/knowledgebase`
- Cloud vs OSS differences: `https://clickhouse.com/docs/cloud/reference/shared-merge-tree`
- Self-managed upgrade guide: `https://clickhouse.com/docs/operations/update`
- Keeper: `https://clickhouse.com/docs/guides/sre/keeper/clickhouse-keeper`

Include only links directly relevant to a finding; omit the section when nothing applies.
