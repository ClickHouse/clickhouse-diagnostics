# 离线（air-gapped）环境下的 dashboard（仪表盘）

默认情况下，`dashboard.html` 会从公共 CDN 加载它的图表库（Chart.js）。当页面在一台**无法访问互联网**的机器上打开时，这个库始终加载不到，页面几乎一片空白：不只是图表，连告警和表格也不显示。

加上 **`-airgap`**，诊断工具就会把随二进制一同发布的 Chart.js 直接内嵌进 `dashboard.html`。这样页面在任何地方都能打开，完全不需要网络访问。其他一切不变：同样的查询照常运行，写出的也是同样的 bundle（诊断包）。不加这个 flag 时，每次运行的行为都和之前完全一样。

## 何时使用

当 dashboard 将会在一台没有互联网的机器上**打开**时使用：隔离网络、受限工作站，或会拦截 CDN 的浏览器。关键在于文件在哪里打开，而不是诊断工具在哪里运行。

## 示例

单节点，在包含 `clickhouse-diagnostic` 和 `queries.*` 文件夹的目录中执行：

```bash
export CLICKHOUSE_PASSWORD='…'
./clickhouse-diagnostic -host localhost -port 8123 -user diag -airgap
```

采集整个集群（[`cluster-collect/`](../cluster-collect/README.md)）：把它放在 `--` 之后传入，这样每台主机的运行都会带上它：

```bash
export CLICKHOUSE_PASSWORD='…'
./clickhouse-diagnostic cluster -user diag -- -airgap
```

与其他 flag 组合使用，一如既往：

```bash
./clickhouse-diagnostic -host ch-node-1 -user diag -from 2026-10-01 -to 2026-10-02 -airgap
```

要检查结果，打开 `dashboard.html` 看页脚：它会显示 **"已内嵌 Chart.js 4.4.3（air-gap 构建，无需网络）"**，而不是 "需要 Chart.js CDN"。

## 须知

- **体积：** `dashboard.html` 增大约 200 KB（在 `.tar.gz` 中约 70 KB）。
- **Gov 模式**（`-mode gov`）不会生成 dashboard，因此该 flag 在那里不起作用。
- **`schema_graph.html`** 在任何模式下都不需要网络。
- **许可：** Chart.js 使用 MIT 许可。其完整许可文本内嵌在 `dashboard.html` 中、紧挨着库代码上方，因此每份副本都会携带它。