-- tier: essential
-- 每个副本的服务器指标（Uptime、副本延迟/队列、part 数量、缓存
-- 字节数、内存）。按核心的行会被丢弃。
SELECT
    hostName() AS hostname,
    metric,
    value
FROM clusterAllReplicas(default, system.asynchronous_metrics)
WHERE NOT match(metric, '(CPU[0-9]+|MHz_[0-9]+)$')
ORDER BY metric, hostname
