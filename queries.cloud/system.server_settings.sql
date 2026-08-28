-- system.server_settings first appeared in 23.3. Cloud runs 23.5 and
-- newer, so it needs no gate here and this is a plain root file — the
-- same asymmetry system.asynchronous_insert_log_7_days already has.
-- The onprem/gov trees carry it in 23.4.1.0/ with no root twin.
--
-- Server-level settings — max_server_memory_usage, the background
-- pools, the paths. system.settings covers the session; this covers
-- the process, and the two answer different questions.
SELECT
    name,
    value,
    `default`,
    changed,
    type
FROM system.server_settings
ORDER BY name
