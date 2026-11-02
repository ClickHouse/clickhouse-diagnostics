-- tier: essential
-- system.error_log (24.8+): the history behind system.errors — every error code
-- raised anywhere in the server (queries AND background merges, fetches,
-- Keeper client, DDL workers), per minute, summed here per hour. This is the
-- file that puts 999 / 242 / 252 / 107 on a timeline even when query_log
-- never saw them. remote = 1 means the error was received from another server.
SELECT
    toStartOfHour(event_time) AS time,
    code,
    error,
    remote,
    sum(value)                AS errors
FROM system.error_log
WHERE event_date >= toDate({from:7d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:7d} AND event_time <= {to:now}
GROUP BY time, code, error, remote
-- The Keeper-dependent codes come first, so the cap never cuts them (the
-- dashboard's Keeper panel and the Keeper health checks read them; at most
-- 168 hours x 5 codes x 2 remote values = 1680 rows): a noisy server's
-- other codes lose their latest hours to the cap, the Keeper series never.
ORDER BY code IN (999, 242, 319, 571, 252) DESC, time, errors DESC
LIMIT 20000
