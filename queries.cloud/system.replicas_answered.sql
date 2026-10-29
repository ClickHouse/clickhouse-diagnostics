-- Which replicas of the service answered this collection. Every request runs
-- with skip_unavailable_shards = 1, so a replica that is down no longer
-- fails the per-replica (clusterAllReplicas) collectors — it is simply
-- missing from them. This file says which: every host the `default` cluster
-- lists, and whether it answered. Host names in system.clusters are full DNS
-- names while hostName() is the short one, so both are compared on their
-- first label.
SELECT
    host_name,
    splitByChar('.', host_name)[1] AS replica,
    replica IN (SELECT splitByChar('.', hostName())[1] FROM clusterAllReplicas(default, system.one)) AS answered
FROM system.clusters
WHERE cluster = 'default'
ORDER BY answered, replica
