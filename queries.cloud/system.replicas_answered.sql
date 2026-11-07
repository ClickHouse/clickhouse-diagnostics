-- tier: essential
-- 本次采集中服务里哪些副本作出了响应。每个请求都以
-- skip_unavailable_shards = 1 运行，因此宕机的副本不再使
-- 按副本（clusterAllReplicas）的 collector 失败——它只是
-- 从结果中缺失。本文件准确说明是哪些：clusterAllReplicas
-- 把每个副本视为自己的 shard，并按集群顺序编号
-- （先 shard_num，再 replica_num），因此响应的 _shard_num 就是
-- 发送它的已配置主机的位置。不做主机名匹配——别名、IP
-- 端点和共享短名称的副本都无法误导它。
--   pos       主机在 `default` 集群中的位置
--   answered  当响应来自该位置时为 1
SELECT
    c.host_name,
    c.host_address,
    c.pos,
    c.pos IN (SELECT _shard_num FROM clusterAllReplicas(default, system.one)) AS answered
FROM
(
    SELECT host_name, host_address, row_number() OVER (ORDER BY shard_num, replica_num) AS pos
    FROM system.clusters
    WHERE cluster = 'default'
) AS c
ORDER BY answered, pos
