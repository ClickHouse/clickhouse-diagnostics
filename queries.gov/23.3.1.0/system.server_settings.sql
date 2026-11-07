-- tier: essential
-- system.server_settings 首次出现于 23.3，所以这个采集器
-- 只存在于版本目录中，没有根目录对应文件：在旧服务器上
-- finder 会跳过它，而不是让整次运行失败。门槛设为
-- 23.3.1.0，即加入该表的版本：background_*_pool_size 在 23.3
-- 从 system.settings 移到这里，而 system.settings 仍会以旧的会话
-- 默认值列出这些名称，所以没有本文件的 23.3 bundle
-- 会读到错误的池大小。
--
-- 服务器级设置 —— max_server_memory_usage、后台
-- 池、路径。system.settings 覆盖会话；这里覆盖
-- 进程，二者回答的是不同问题。
--
-- Gov：这里没有任何东西被哈希 —— 设置名和值都没有。
-- 名称是 ClickHouse 常量，而被哈希的值在任何地方
-- 都没有解码器，因为 gov 名称映射
-- （internal/gov_mapping.go）由 system.tables 构建，只覆盖
-- 数据库和表名。支持人员会收到 64 个十六进制
-- 字符，却无从还原。
--
-- 少数指名客户基础设施的值会被 REMOVED
-- 替代，这比给它们加盐更安全，也更容易推理：
-- 被移除的值不可能泄露，也不需要密钥才能读取。
-- 'REMOVED' 与配置脱敏器用于已脱敏凭据的哨兵值
-- 相同（internal/collection/heuristics.go）。设置名、
-- `default` 和 `changed` 都会保留，所以归档仍能表明
-- 某条路径被定制过，却不显示它是什么 —— 这正是
-- 支持工程师实际需要的部分。
--
-- 依据值的形状而不是设置名列表来判定，
-- 因为名称不遵循任何约定：`config-file`、
-- `include_from`、`logger.log` 和 `logger.errorlog` 都是
-- 文件系统路径，名称里却没有 "path"，所以基于名称的
-- 允许列表恰恰会漏掉没人想得到的设置。任何
-- 绝对路径或 URL 都会隐藏，再加上那些两者都不是的
-- 客户自选名称。没有 `type = 'String'` 守卫，因为不需要：
-- 在 26.7 上检查过，形状测试匹配的每一行都已经是 String
-- （17/17），所以不加守卫也能覆盖 URI 类型的设置，
-- 同时永远不会匹配到数字。
--
-- `default` 是 ClickHouse 自己编译进去的默认值，不是
-- 客户的，所以它保持可读 —— 正因如此，值为 REMOVED 时
-- `changed = 1` 才仍然可读。
--
-- 空值保持为空，而不是变成 REMOVED。这里
-- 没有什么可移除，而 'REMOVED' 会暗示配置了一条
-- 并不存在的路径：stock 服务器上 interserver_http_host 未设置，
-- 读到“未配置”才是有用且无害的答案。
SELECT
    name,
    if(identifying AND value != '', 'REMOVED', value) AS value,
    `default`,
    changed,
    type
FROM (
    SELECT
        name,
        value,
        `default`,
        changed,
        type,
        value LIKE '/%' OR value LIKE '%://%'
        OR `default` LIKE '/%' OR `default` LIKE '%://%'
        OR name IN ('default_database', 'default_replica_name',
                    'interserver_http_host', 'default_profile',
                    'merge_workload', 'mutation_workload')
                                                    AS identifying
    FROM system.server_settings
)
ORDER BY name
