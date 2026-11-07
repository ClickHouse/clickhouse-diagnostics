-- tier: essential
-- 服务器版本，以及告警检查像实时查询那样读取其他文件所需的锚点：
-- 服务器自己的时钟（now()、collected_at）和时区 ——
-- 其他文件中的 DateTime 值就在那个时区 ——
-- 以及消息所指的主机名（在 gov 中哈希）。在
-- cloud 模式下，这是应答的副本，而不是每台副本。
SELECT
  version()      AS version,
  hex(SHA256(concat(hostName(), '%salt%'))) AS hostname,
  timezone()     AS timezone,
  toString(now()) AS collected_at
