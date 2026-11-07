-- tier: essential
-- system.server_settings 首次出现于 23.3，所以这个 collector
-- 只放在版本目录中，没有根文件对应版本：在更旧的服务器上，
-- finder 会跳过它，而不是让整次运行失败。在 23.3.1.0
-- 这一加入该表的版本上设门：background_*_pool_size 在 23.3
-- 从 system.settings 移到这里，而 system.settings 仍会以旧的会话默认值
-- 列出这些名字，所以缺少此文件的 23.3 bundle
-- 会读到错误的池大小。
--
-- 服务器级设置——max_server_memory_usage、后台
-- 池、路径。system.settings 覆盖会话；这里覆盖
-- 进程，两者回答不同的问题。
SELECT
    name,
    value,
    `default`,
    changed,
    type
FROM system.server_settings
ORDER BY name
