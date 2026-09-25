-- Refreshable materialized views (REFRESH EVERY … / AFTER …) run on a schedule
-- instead of on insert, so query_views_log never sees them and system.tables
-- cannot tell them from an ordinary MV. This is the only table that says whether
-- a refresh is Scheduled / Running / Disabled, when it last succeeded and what
-- the last failure was. The schema graph colours these views differently.
--
-- No root file: the table arrived with refreshable MVs in 23.12, so this rung is
-- simply skipped on older servers rather than failing there.
SELECT
    database,
    view,
    status,
    last_success_time,
    next_refresh_time,
    -- Exception text embeds identifiers; kept here (onprem) and dropped in gov.
    leftUTF8(exception, 500) AS exception
FROM system.view_refreshes
