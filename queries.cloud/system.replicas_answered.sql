-- tier: essential
-- Which replicas of the service answered this collection. Every request runs
-- with skip_unavailable_shards = 1, so a replica that is down no longer
-- fails the per-replica (clusterAllReplicas) collectors — it is simply
-- missing from them. This file says how many and, when it can, which.
--   configured  hosts the `default` cluster lists
--   answered    replies to a fan-out over it (one row per replica that
--               answered) — the COUNTS decide whether a replica is missing
--   name_matched  this host's name matched a reply's FQDN() or hostName()
--               (or its first DNS label did); used only to NAME the missing
--               replicas, because aliases, IP endpoints or two hosts sharing
--               a short name make names unreliable on their own
SELECT
    host_name,
    splitByChar('.', host_name)[1] AS replica,
    host_name IN (SELECT FQDN() FROM clusterAllReplicas(default, system.one))
        OR host_name IN (SELECT hostName() FROM clusterAllReplicas(default, system.one))
        OR replica IN (SELECT splitByChar('.', hostName())[1] FROM clusterAllReplicas(default, system.one)) AS name_matched,
    (SELECT count() FROM system.clusters WHERE cluster = 'default') AS configured,
    (SELECT count() FROM clusterAllReplicas(default, system.one)) AS answered
FROM system.clusters
WHERE cluster = 'default'
ORDER BY name_matched, replica
