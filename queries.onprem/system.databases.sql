-- tier: essential
-- 每个 database 一行，附带其引擎。Replicated 数据库的数量是一个
-- Keeper 负载信号（每个 replica 的每个 database 对应一个 DDLWorker、一套
-- Keeper watch 和一份 DDL log），而单看 system.tables 无法区分 Atomic 与
-- Replicated。uuid 可关联到异常消息中出现的 data/<uuid>/ 路径。
SELECT
    name,
    engine,
    uuid
FROM system.databases
ORDER BY engine, name
