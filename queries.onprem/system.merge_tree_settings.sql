-- Server-wide MergeTree defaults: the <merge_tree> block of config.xml as the
-- server resolved it, plus every setting left at its compiled-in default.
-- system.settings is the session, system.server_settings the process; this is
-- the third layer — what every MergeTree table inherits unless its own
-- SETTINGS clause (system.tables.engine_full) overrides it.
--
-- Why it is collected: the knobs that decide how a cluster with many tables
-- behaves live here and nowhere else in the bundle — parts_to_throw_insert /
-- parts_to_delay_insert (the TOO_MANY_PARTS thresholds), and on
-- SharedMergeTree shared_merge_tree_parts_load_batch_size (how many part
-- fetches one table schedules per round, default 32) and
-- shared_merge_tree_leader_update_period_seconds (how often every table runs
-- a leader election, default 30 s — tens of thousands of tables turn that
-- into hundreds of elections per second on the schedule pool). A support
-- recommendation to change them is only safe once the current value is known.
--
-- ~350 rows, a few tens of KiB. `changed = 1` marks the deviations from
-- default. The `default` column exists on recent servers only, so the root
-- file (22.8 floor) reads name, value, changed and type — all four exist on
-- 22.8 (verified on 22.8.21.38).
SELECT
    name,
    value,
    changed,
    type
FROM system.merge_tree_settings
ORDER BY name
