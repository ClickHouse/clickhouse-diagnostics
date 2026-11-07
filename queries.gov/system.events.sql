-- tier: essential
-- 自服务器启动以来的累计 ProfileEvents：ZooKeeper*Exceptions、
-- S3*RequestsErrors、ReadBufferFromS3RequestsErrors、ReplicatedPartFailedFetches、
-- FailedQuery/FailedInsertQuery、RejectedInserts、DelayedInserts …
-- 是计数器而非速率：对照 Uptime（system.asynchronous_metrics）读取，
-- 或比较两个 bundle 的差值。
SELECT
    event,
    value,
    description
FROM system.events
WHERE value > 0
ORDER BY event
