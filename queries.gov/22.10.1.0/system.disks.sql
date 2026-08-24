-- 22.10+ variant: includes unreserved_space (added to system.disks in
-- 22.10). See the root file for the 22.8/22.9 baseline.
SELECT
    hex(SHA256(concat(name, '%salt%')))             AS name,
    hex(SHA256(concat(path, '%salt%')))             AS path,
    formatReadableSize(free_space_b)                AS free_space,
    formatReadableSize(total_space_b)               AS total_space,
    formatReadableSize(unreserved_space_b)          AS unreserved_space,
    formatReadableSize(total_space_b - free_space_b) AS used_space,
    if(total_space_b > 0,
       round(free_space_b / total_space_b * 100, 1),
       0) AS free_pct,
    type
FROM (
    SELECT
        name,
        path,
        type,
        free_space       AS free_space_b,
        total_space      AS total_space_b,
        unreserved_space AS unreserved_space_b
    FROM system.disks
)
ORDER BY total_space_b DESC
