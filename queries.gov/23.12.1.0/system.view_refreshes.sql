-- Refreshable materialized views (REFRESH EVERY … / AFTER …) run on a schedule
-- instead of on insert, so query_views_log never sees them and system.tables
-- cannot tell them from an ordinary MV. This is the only table that says whether
-- a refresh is Scheduled / Running / Disabled and when it last succeeded.
--
-- No root file: the table arrived with refreshable MVs in 23.12, so this rung is
-- simply skipped on older servers rather than failing there.
--
-- Redacted (gov) variant: database and view are hashed with the same salt as
-- system.tables, so the row joins to the hashed table list; the exception text
-- is not collected at all because it embeds identifiers.
SELECT
    hex(SHA256(concat(database, '%salt%'))) AS database,
    hex(SHA256(concat(view, '%salt%')))     AS view,
    status,
    last_success_time,
    next_refresh_time,
    exception != '' AS has_exception
FROM system.view_refreshes
