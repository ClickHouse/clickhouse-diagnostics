-- 可刷新物化视图（REFRESH EVERY … / AFTER …）按计划运行，而不是在插入时
-- 运行，所以 query_views_log 永远看不到它们，system.tables 也无法
-- 把它们与普通 MV 区分开。这是唯一能说明一次刷新是 Scheduled /
-- Running / Disabled 以及上次成功时间的表。
--
-- 没有根目录文件：该表随 23.12 的可刷新 MV 一起出现，所以在旧服务器
-- 上直接跳过这一级，而不是在那里失败。
--
-- 脱敏（gov）变体：database 和 view 用与 system.tables 相同的盐
-- 哈希，因此该行可与哈希后的表列表关联；异常文本完全
-- 不采集，因为它会嵌入标识符。
SELECT
    hex(SHA256(concat(database, '%salt%'))) AS database,
    hex(SHA256(concat(view, '%salt%')))     AS view,
    status,
    last_success_time,
    next_refresh_time,
    exception != '' AS has_exception
FROM system.view_refreshes
