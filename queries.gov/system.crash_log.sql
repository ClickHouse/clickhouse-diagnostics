-- tier: essential
-- Server crashes (the table exists only on a server that has crashed). The
-- query that was running is hashed like every gov identifier; stack traces
-- are left out — they are build addresses, not needed to see that and when
-- the server crashed, and the crash_log_entries alert reads only these.
SELECT
    event_time,
    signal,
    thread_id,
    hex(SHA256(concat(query_id, '%salt%'))) AS query_id,
    version,
    revision,
    build_id
FROM system.crash_log
ORDER BY event_time DESC
LIMIT 100
