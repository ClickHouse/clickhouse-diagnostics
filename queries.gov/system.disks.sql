SELECT
    hex(SHA256(concat(name, '%salt%')))  AS name,
    hex(SHA256(concat(path, '%salt%')))  AS path,
    formatReadableSize(free_space)       AS free_space,
    formatReadableSize(total_space)      AS total_space,
    formatReadableSize(unreserved_space) AS unreserved_space,
    formatReadableSize(total_space - free_space) AS used_space,
    if(total_space > 0,
       round(free_space / total_space * 100, 1),
       0) AS free_pct,
    type
FROM system.disks
ORDER BY total_space DESC
FORMAT Native
