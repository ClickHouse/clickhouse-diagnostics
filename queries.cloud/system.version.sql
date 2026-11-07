-- tier: essential
-- 服务器版本，以及 alert 检查读取其他文件时需要的锚点，
-- 使其像实时查询一样读取：服务器自己的时钟（now()、
-- collected_at）和时区——其他文件中的 DateTime 值使用
-- 该时区——以及消息中提到的主机名（gov 中会哈希）。在
-- cloud 模式下，这是作出响应的副本，而不是每个副本。
SELECT
  version()      AS version,
  hostName() AS hostname,
  timezone()     AS timezone,
  toString(now()) AS collected_at
