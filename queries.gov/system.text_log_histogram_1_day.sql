-- Warning-and-worse log volume per hour, level and logger CLASS over the last
-- day, with one example line. The logger name is normalised to its class
-- ("db.t (uuid)(PartsUpdateElection)" → PartsUpdateElection, "DDLWorker(db)" →
-- DDLWorker) so 25 000 tables do not become 25 000 rows. This is the file that
-- says WHEN errors started and WHICH component; system.text_log (2000 rows)
-- only shows the newest lines.
--
-- Gov: the class stays readable — it is a ClickHouse component name, not
-- customer data. Three guards cover the shapes where a raw identifier could
-- survive normalisation: the class regexes accept digits/spaces
-- (S3ObjectStorage, "KafkaConsumer 1"), a "<disk>::" prefix is masked to "*::"
-- (disk names are hashed everywhere else in gov), and whatever still contains
-- a dot (a db.table that no pattern recognised) is hashed with the salt. The
-- example line is always hashed.
SELECT
    toStartOfHour(event_time)          AS time,
    level,
    if(masked LIKE '%.%', hex(SHA256(concat(masked, '%salt%'))), masked)   AS logger_class,
    count()                            AS count,
    hex(SHA256(concat(anyLast(leftUTF8(message, 300)), '%salt%'))) AS example
FROM
(
    SELECT
        event_time,
        level,
        message,
        replaceRegexpOne(
            multiIf(match(logger_name, '\\)\\(([A-Za-z0-9_ ]+)\\)$'),        extract(logger_name, '\\)\\(([A-Za-z0-9_ ]+)\\)$'),
                    match(logger_name, '^[A-Za-z0-9_:/]+ ?\\(.*\\)$'),          extract(logger_name, '^([A-Za-z0-9_:/]+) ?\\('),
                    match(logger_name, ' \\(([A-Za-z0-9_ ]+)\\)$'),             extract(logger_name, ' \\(([A-Za-z0-9_ ]+)\\)$'),
                    logger_name),
            '^[^:\\s]+::', '*::')                                       AS masked
    FROM system.text_log
    WHERE event_date >= toDate({from:1d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:1d} AND event_time <= {to:now}
      AND level IN ('Warning', 'Error', 'Critical', 'Fatal')
)
GROUP BY time, level, logger_class
ORDER BY time, count DESC
LIMIT 10000
