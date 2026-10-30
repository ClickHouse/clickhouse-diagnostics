-- tier: essential
SELECT
    hostname,
    name,
    path,
    formatReadableSize(free_space_b)                AS free_space,
    formatReadableSize(total_space_b)               AS total_space,
    formatReadableSize(unreserved_space_b)          AS unreserved_space,
    formatReadableSize(total_space_b - free_space_b) AS used_space,
    if(total_space_b > 0,
       round(free_space_b / total_space_b * 100, 1),
       0) AS free_pct,
    type,
    -- exact bytes: free_pct is rounded to 0.1, and the low-space check must
    -- not miss 14.96 % as 15.0
    free_space_b  AS free_bytes,
    total_space_b AS total_bytes
FROM (
    SELECT
        hostName() AS hostname,
        name,
        path,
        type,
        free_space       AS free_space_b,
        total_space      AS total_space_b,
        unreserved_space AS unreserved_space_b
    FROM clusterAllReplicas(default, system.disks)
)
ORDER BY total_space_b DESC
