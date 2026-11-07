# 故障排查

*已从主 [README](../README.md) 移出，README 中保留每一节的简短版本。*

## 故障排查

**“错误：queries 目录 './queries.<mode>' 不存在”**
你传入的模式在仓库根目录没有匹配的目录。检查 `-mode` 是否属于 `cloud`、`onprem`、`gov` 之一。

**“配置目录 '<dir>' 不存在，跳过采集”**
使用 `-skip-config`，或用 `-config-dir` 传入正确路径。Cloud 用户应始终使用 `-skip-config`。

**“Connection refused” / 超时**
确认服务器在所选 `-protocol` + `-host` + `-port` 上可达。默认端口因协议而异（8123 与 8443）。

**“security: 仅允许 SELECT 查询；查询以 … 开头”（告警被拦截）**
某个 alert YAML 含有非只读语句。告警查询被限制为 `SELECT` / `WITH`；请重写该查询或移除该规则。

**“gov 模式的 salt 必须是 8–64 个 ASCII 字母或数字（A–Z、a–z、0–9）”**
`-salt` 值（或交互式提示输入）含空格、标点，或长度不对。请选择匹配 `[A-Za-z0-9]{8,64}` 的值 — 见 [Gov 模式与名称哈希](modes.md#gov-模式与名称哈希)。

**Dashboard 图表空白**
生成的 `dashboard.html` 从公共 CDN 加载 Chart.js。请在可访问互联网的环境中打开它，或参见 [`airgap/`](../airgap/README.md) 构建无需网络的 dashboard。

**配置文件“Permission denied”**
你对 `-config-dir` 没有读权限。请以适当权限运行，或使用 `-skip-config`。