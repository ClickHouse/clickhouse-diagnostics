-- tier: essential
-- 自服务器启动以来的累计 ProfileEvents：ZooKeeper*Exceptions、
-- S3*RequestsErrors、ReadBufferFromS3RequestsErrors、ReplicatedPartFailedFetches、
-- FailedQuery/FailedInsertQuery、RejectedInserts、DelayedInserts …
-- 这些是计数器而非速率：要结合 Uptime（system.asynchronous_metrics）来读，
-- 或者对比两个 bundle。
SELECT
    event,
    value,
    description
FROM system.events
WHERE value > 0
ORDER BY event
