-- tier: essential
-- The send queues of async Distributed inserts: one row per Distributed table
-- and remote shard, the files waiting in that shard's directory on this node.
-- A snapshot of in-memory counters — the server walks its table list and
-- reads each queue's status, with no disk scan and no Keeper request
-- (measured: 15 ms and 4 MiB with 60 000 tables and 10 000 queues).
--
-- What each column means (StorageDistributed's insert queue):
--   last_exception  the last send failure; cleared by the next successful
--                   send, so non-empty means this queue is failing now. Its
--                   code names the cause — the column that decides the case.
--   error_count     failed sends; never reset, only halved every 5 minutes
--                   while files are being sent, so a queue that recovered
--                   keeps its count — context, not a symptom.
--   is_blocked      sends stopped by hand (SYSTEM STOP DISTRIBUTED SENDS).
--   broken_data_files  files moved to broken/: their rows never reached the
--                   shard; counted until someone removes them.
--   data_files / data_compressed_bytes  waiting files; 0 between flushes on a
--                   healthy queue.
--
-- No root file: last_exception_time arrived in 23.3, and with it this
-- collector; older servers skip it. Not on cloud: inserts there are
-- synchronous and there is no async queue to read.
SELECT
    database,
    table,
    data_path,
    is_blocked,
    error_count,
    data_files,
    data_compressed_bytes,
    broken_data_files,
    broken_data_compressed_bytes,
    -- The number after "Code: ", read without a regex: the first token.
    if(position(last_exception, 'Code: ') > 0,
       toUInt32OrZero(splitByNonAlpha(substring(last_exception, position(last_exception, 'Code: ') + 6))[1]),
       0) AS last_exception_code,
    substring(last_exception, 1, 500) AS last_exception,
    if(last_exception_time > toDateTime(0), toString(last_exception_time), '') AS last_exception_time
FROM system.distribution_queue
-- Every state the alert reports sorts ahead of everything else, so the LIMIT
-- drops none of them before a healthy queue: failing, then broken, then stopped with files waiting;
-- then queues with files waiting or past failures, then idle ones.
ORDER BY last_exception != '' DESC, broken_data_files > 0 DESC, (is_blocked AND data_files > 0) DESC,
         data_files > 0 DESC, error_count > 0 DESC, data_compressed_bytes DESC
LIMIT 1000
