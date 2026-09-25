-- Refreshable materialized views (REFRESH EVERY … / AFTER …) run on a schedule
-- instead of on insert, so query_views_log never sees them and system.tables
-- cannot tell them from an ordinary MV. This is the only table that says whether
-- a refresh is Scheduled / Running / Disabled, when it last succeeded and what
-- the last failure was. The schema graph colours these views differently.
--
-- No root file: the table arrived with refreshable MVs in 23.12, so this rung is
-- simply skipped on older servers rather than failing there.
--
-- State table, not a log: there is no hostname column, so the replica is
-- labelled with hostName() as every other cloud state collector does. One row
-- per (replica, view) — in a replicated database each replica reports the
-- shared schedule from its own point of view.
SELECT
    hostName() AS hostname,
    database,
    view,
    status,
    last_success_time,
    next_refresh_time,
    leftUTF8(exception, 500) AS exception
FROM clusterAllReplicas(default, system.view_refreshes)
