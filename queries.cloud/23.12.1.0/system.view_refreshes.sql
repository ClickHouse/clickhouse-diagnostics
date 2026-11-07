-- 可刷新 materialized view（物化视图）（REFRESH EVERY … / AFTER …）按计划运行，
-- 而不是在插入时运行，因此 query_views_log 永远看不到它们，system.tables
-- 也无法把它们与普通 MV 区分开。这是唯一能说明
-- 一次刷新是 Scheduled / Running / Disabled、上次成功时间以及
-- 上次失败内容的表。schema graph（模式关系图）会以不同颜色标记这些视图。
--
-- 没有根文件：该表随 23.12 的可刷新 MV 一起出现，因此在更旧的服务器上
-- 会直接跳过，而不是失败。
--
-- 状态表，不是日志：没有 hostname 列，因此副本用
-- hostName() 标记，和其他所有 cloud 状态采集器一样。
-- 每个（replica, view）一行——在 replicated database 中，每个副本报告
-- 自己视角下的共享计划。
SELECT
    hostName() AS hostname,
    database,
    view,
    status,
    last_success_time,
    next_refresh_time,
    leftUTF8(exception, 500) AS exception
FROM clusterAllReplicas(default, system.view_refreshes)
