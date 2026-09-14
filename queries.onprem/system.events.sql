-- Cumulative ProfileEvents since server start: ZooKeeper*Exceptions,
-- S3*RequestsErrors, ReadBufferFromS3RequestsErrors, ReplicatedPartFailedFetches,
-- FailedQuery/FailedInsertQuery, RejectedInserts, DelayedInserts …
-- Counters, not rates: read them against Uptime (system.asynchronous_metrics)
-- or diff two bundles.
SELECT
    event,
    value,
    description
FROM system.events
WHERE value > 0
ORDER BY event
