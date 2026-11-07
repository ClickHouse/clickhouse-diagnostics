-- tier: essential
-- 服务器版本，以及告警检查像实时查询那样读取其他文件所需的锚点：
-- 服务器自己的时钟（now()、collected_at）和时区——其他文件中的
-- DateTime 值就以该时区表示——以及消息所指的主机名
-- （在 gov 模式下哈希化）。在 cloud 模式下，这是做出应答的那个
-- replica，而不是每个 replica。
SELECT
  version()      AS version,
  hostName() AS hostname,
  timezone()     AS timezone,
  toString(now()) AS collected_at
