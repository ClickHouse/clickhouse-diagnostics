-- tier: essential
-- system.server_settings 首次出现在 23.3。Cloud 运行 23.5 及
-- 更新版本，因此这里不需要版本门槛，这是一个普通根文件——
-- 与 system.asynchronous_insert_log_7_days 已有的不对称处理相同。
-- onprem/gov 目录把它放在 23.4.1.0/ 中，没有对应的根文件。
--
-- 服务器级设置——max_server_memory_usage、后台
-- pool、路径。system.settings 覆盖会话；本文件覆盖
-- 进程，两者回答不同的问题。
SELECT
    name,
    value,
    `default`,
    changed,
    type
FROM system.server_settings
ORDER BY name
