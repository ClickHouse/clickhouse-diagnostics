# AGENTS.md — 面向编码 agent 的指南（Codex、Claude Code 及其他）

本仓库是 `clickhouse-diagnostic`：一个 Go CLI，把 ClickHouse `system.*` 表、告警结果、host facts（主机信息）、经 sanitisation（脱敏）的配置和服务器日志采集到一个 `clickhouse_diagnostic_<ts>.tar.gz` 支持 bundle（诊断包）中（重命名前为 `clickhouse_backup_<ts>.tar.gz`），并生成一个自包含的 `dashboard.html`。`README.md` 是该工具本身的参考文档。

## 分析 bundle 或运行工具

在接触 bundle 之前，先阅读并遵循 `skills/clickhouse-diagnostic/SKILL.md`。它定义了工作流（coverage → triage → health checks → patterns → summary → privacy gate）、按需惰性加载的参考文件（`skills/clickhouse-diagnostic/references/`），以及一个仅用标准库的预处理脚本：

```bash
python3 skills/clickhouse-diagnostic/scripts/inspect_bundle.py <clickhouse_diagnostic_*.tar.gz | clickhouse_backup_*.tar.gz | dir>
```

同一个 skill 通过 `.claude/skills/clickhouse-diagnostic` 暴露给 Claude Code，通过 `.agents/skills/clickhouse-diagnostic` 暴露给 Codex（两者都是指向 `skills/clickhouse-diagnostic` 的符号链接）。

## 适用于此处每个 agent 的规则

- Bundle 是机密的客户证据：在本地分析，尽量少引用，未经 `skills/clickhouse-diagnostic/references/privacy.md` 中的检查清单，绝不把标识符上传或粘贴到本机之外。绝不要修改 bundle。
- 不得携带 bundle 内容进行网络调用。只有在用户要求时，才允许针对 GitHub 做版本调研，且只能使用版本字符串、设置名称和错误文本。
- 除非用户在当前会话中明确要求，否则不要 commit、push 或创建 PR。
- Go 代码：`make test` 必须通过；查询文件位于 `queries.<mode>/` 下，并按版本子目录（`MAJOR.MINOR.PATCH.BUILD/`）组织；`alerts/` 下的 alert rule（告警规则）必须保持为只读 `SELECT`。在新增 SQL 之前，参见 `docs/versions.md` → *Version-specific queries*。