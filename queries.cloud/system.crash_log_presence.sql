-- tier: essential
-- 哪些副本上存在 system.crash_log。该表在第一次崩溃时
-- 创建，因此只要任一副本从未崩溃，对它的 fan-out 就会以 UNKNOWN_TABLE
-- 失败——于是 system.crash_log.sql 没有文件。
-- 本文件告诉 crash_log_entries alert rule 是哪种情况：没有副本
-- 拥有该表（不适用），还是部分副本拥有（它们的崩溃无法
-- 读取——会暴露出来，绝不隐藏为“没有崩溃”）。
SELECT
    hostName() AS hostname,
    count() AS has_crash_log
FROM clusterAllReplicas(default, system.tables)
WHERE database = 'system' AND name = 'crash_log'
GROUP BY hostname
