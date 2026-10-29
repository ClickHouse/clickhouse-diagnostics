-- tier: essential
-- On which replicas system.crash_log exists. The table is created on the
-- first crash, so a fan-out over it fails with UNKNOWN_TABLE whenever any
-- replica has never crashed — and then system.crash_log.sql has no file.
-- This file tells the crash_log_entries alert which case it is: no replica
-- has the table (not applicable), or some do (their crashes could not be
-- read — surfaced, never hidden as "no crash").
SELECT
    hostName() AS hostname,
    count() AS has_crash_log
FROM clusterAllReplicas(default, system.tables)
WHERE database = 'system' AND name = 'crash_log'
GROUP BY hostname
