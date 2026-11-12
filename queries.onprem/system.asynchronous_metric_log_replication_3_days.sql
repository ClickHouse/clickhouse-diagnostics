-- tier: essential
-- Replication delay and queue depth per host per hour: the max of the
-- server's own Replicas* gauges (asynchronous_metric_log, one sample per
-- update interval). system.replicas is a snapshot taken at collection, so a
-- lag that cleared before it shows nothing there; this is the time series
-- of how far behind each host was, and when. Max over the hour: the worst
-- moment counts. metric is the table's primary key prefix, so only these
-- eight series are read.
SELECT
    hostName()                 AS hostname,
    toStartOfHour(event_time)  AS time,
    maxIf(value, metric = 'ReplicasMaxAbsoluteDelay')       AS max_absolute_delay_s,
    maxIf(value, metric = 'ReplicasMaxRelativeDelay')       AS max_relative_delay_s,
    maxIf(value, metric = 'ReplicasMaxQueueSize')           AS max_queue_size,
    maxIf(value, metric = 'ReplicasMaxInsertsInQueue')      AS max_inserts_in_queue,
    maxIf(value, metric = 'ReplicasMaxMergesInQueue')       AS max_merges_in_queue,
    maxIf(value, metric = 'ReplicasSumQueueSize')           AS max_sum_queue_size,
    maxIf(value, metric = 'ReplicasSumInsertsInQueue')      AS max_sum_inserts_in_queue,
    maxIf(value, metric = 'ReplicasSumMergesInQueue')       AS max_sum_merges_in_queue,
    count()                                                  AS samples
FROM system.asynchronous_metric_log
WHERE metric IN ('ReplicasMaxAbsoluteDelay', 'ReplicasMaxRelativeDelay', 'ReplicasMaxQueueSize',
                 'ReplicasMaxInsertsInQueue', 'ReplicasMaxMergesInQueue', 'ReplicasSumQueueSize',
                 'ReplicasSumInsertsInQueue', 'ReplicasSumMergesInQueue')
  AND event_date >= toDate({from:3d}, timezone()) AND event_time > {from:3d} AND event_time <= {to:now}
GROUP BY hostname, time
ORDER BY hostname, time
