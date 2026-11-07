-- tier: essential
SELECT
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
    -- 精确字节数：free_pct 四舍五入到 0.1，低空间检查不能
    -- 把 14.96 % 误当成 15.0 而漏报
    free_space_b  AS free_bytes,
    total_space_b AS total_bytes
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
