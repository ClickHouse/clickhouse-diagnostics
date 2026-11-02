-- tier: essential
SELECT
    hostName() AS hostname,
    name,
    code,
    value,
    toString(last_error_time)    AS last_error_time,
    last_error_message,
    left(toString(last_error_trace), 500)  AS last_error_trace
FROM clusterAllReplicas(default, system.errors)
WHERE value > 0
-- 100 per replica, not 100 over the cluster: the dashboard sums the
-- replicas' counters per error, and a cluster-wide cap taken before that
-- sum could drop the error that is most frequent overall.
ORDER BY value DESC
LIMIT 100 BY hostname
