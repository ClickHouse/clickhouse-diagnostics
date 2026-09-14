-- Warning-and-worse log volume per hour, level and logger CLASS over the last
-- day, with one example line. The logger name is normalised to its class
-- ("db.t (uuid)(PartsUpdateElection)" → PartsUpdateElection, "DDLWorker(db)" →
-- DDLWorker) so 25 000 tables do not become 25 000 rows. This is the file that
-- says WHEN errors started and WHICH component; system.text_log (2000 rows)
-- only shows the newest lines.
-- Gov: the normalised class and the example line are hashed (the class can
-- fall back to a raw logger name that embeds a table name).
SELECT
    toStartOfHour(event_time)          AS time,
    level,
    hex(SHA256(concat(multiIf(match(logger_name, '\\)\\(([A-Za-z]+)\\)$'),           extract(logger_name, '\\)\\(([A-Za-z]+)\\)$'),
            match(logger_name, '^[A-Za-z0-9_:/]+ ?\\(.*\\)$'),       extract(logger_name, '^([A-Za-z0-9_:/]+) ?\\('),
            match(logger_name, ' \\(([A-Za-z]+)\\)$'),              extract(logger_name, ' \\(([A-Za-z]+)\\)$'),
            logger_name), '%salt%'))) AS logger_class,
    count()                            AS count,
    hex(SHA256(concat(anyLast(leftUTF8(message, 300)), '%salt%'))) AS example
FROM system.text_log
WHERE event_date >= toDate({from:1d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:1d} AND event_time <= {to:now}
  AND level IN ('Warning', 'Error', 'Critical', 'Fatal')
GROUP BY time, level, logger_class
ORDER BY time, count DESC
LIMIT 10000
