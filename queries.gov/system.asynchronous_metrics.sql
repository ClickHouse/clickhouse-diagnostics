-- tier: essential
-- Gov：还额外丢弃每个按对象区分的 gauge（DiskUsed_<disk>、
-- NetworkSendBytes_<iface>、BlockReadOps_<device>、…）—— 后缀是
-- 基础设施名称，无法在指标名内部做哈希。
SELECT
    metric,
    value,
    description
FROM system.asynchronous_metrics
WHERE NOT match(metric, '(CPU[0-9]+|MHz_[0-9]+|_[A-Za-z0-9]+)$')
ORDER BY metric
