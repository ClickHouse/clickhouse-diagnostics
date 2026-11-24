-- tier: essential
-- The send queues of async Distributed inserts (see the onprem file for what
-- each column means). No root file: last_exception_time arrived in 23.3.
--
-- Redacted (gov) variant: database and table are hashed with the same salt as
-- system.tables, so a row joins to the hashed table list; data_path embeds the
-- table and, for old-style directories, the shard's host and user, so it is
-- hashed too. The exception text is not collected — it names paths and hosts —
-- but its code is, with has_exception: the code alone names the cause.
SELECT
    hex(SHA256(concat(database, '%salt%')))  AS database,
    hex(SHA256(concat(table, '%salt%')))     AS table,
    hex(SHA256(concat(data_path, '%salt%'))) AS data_path,
    is_blocked,
    error_count,
    data_files,
    data_compressed_bytes,
    broken_data_files,
    broken_data_compressed_bytes,
    if(position(last_exception, 'Code: ') > 0,
       toUInt32OrZero(splitByNonAlpha(substring(last_exception, position(last_exception, 'Code: ') + 6))[1]),
       0) AS last_exception_code,
    last_exception != '' AS has_exception,
    if(last_exception_time > toDateTime(0), toString(last_exception_time), '') AS last_exception_time
FROM system.distribution_queue
-- Every state the alert reports sorts ahead of everything else, so the LIMIT
-- drops none of them before a healthy queue: failing, then broken, then stopped with files waiting;
-- then queues with files waiting or past failures, then idle ones.
ORDER BY has_exception DESC, broken_data_files > 0 DESC, (is_blocked AND data_files > 0) DESC,
         data_files > 0 DESC, error_count > 0 DESC, data_compressed_bytes DESC
LIMIT 1000
