-- 可刷新的物化视图（REFRESH EVERY … / AFTER …）按计划运行，
-- 而不是在插入时运行，所以 query_views_log 永远看不到它们，system.tables
-- 也无法把它们与普通 MV 区分开。这是唯一能说明一次刷新是
-- Scheduled / Running / Disabled、上次何时成功、上次失败原因是什么的表。
-- schema graph 会给这些视图不同的着色。
--
-- 没有根文件：该表随 23.12 的可刷新 MV 一起出现，所以这一级
-- 在更旧的服务器上会被直接跳过，而不是在那里失败。
SELECT
    database,
    view,
    status,
    last_success_time,
    next_refresh_time,
    -- 异常文本会内嵌标识符；在 onprem 中保留，在 gov 中丢弃。
    leftUTF8(exception, 500) AS exception
FROM system.view_refreshes
