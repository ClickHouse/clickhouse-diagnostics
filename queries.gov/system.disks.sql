-- tier: essential
-- Root variant for the oldest supported servers (22.8/22.9):
-- unreserved_space was added in 22.10 — see 22.10.1.0/system.disks.sql
-- for the richer variant used on newer servers.
SELECT
    hex(SHA256(concat(name, '%salt%')))             AS name,
    hex(SHA256(concat(path, '%salt%')))             AS path,
    formatReadableSize(free_space_b)                AS free_space,
    formatReadableSize(total_space_b)               AS total_space,
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
        name,
        path,
        type,
        free_space       AS free_space_b,
        total_space      AS total_space_b
    FROM system.disks
)
ORDER BY total_space_b DESC
