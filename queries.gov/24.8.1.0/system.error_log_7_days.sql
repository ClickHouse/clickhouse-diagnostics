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
ORDER BY time, errors DESC
LIMIT 20000
