-- Gov: additionally drops every per-object gauge (DiskUsed_<disk>,
-- NetworkSendBytes_<iface>, BlockReadOps_<device>, …) — the suffix is an
-- infrastructure name that cannot be hashed inside a metric name.
SELECT
    metric,
    value,
    description
FROM system.asynchronous_metrics
WHERE NOT match(metric, '(CPU[0-9]+|MHz_[0-9]+|_[A-Za-z0-9]+)$')
ORDER BY metric
