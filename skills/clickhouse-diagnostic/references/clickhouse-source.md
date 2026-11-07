# 把客户的ClickHouse版本对应到源码、changelog和issue

仅在用户要做版本研究时才需要（需要网络）。bundle（诊断包）总是带有精确版本（`system.version_*.jsonl`，还有`dashboard.html → DATA.version`和`logs/clickhouse-server.log`中的启动banner）。要用它——绝不要基于"最新版ClickHouse"来推理。

## 1. 版本字符串 → git tag

`version()`返回`MAJOR.MINOR.PATCH.BUILD`，例如`25.3.2.39`。发布tag是`v25.3.2.39-lts`或`v25.3.2.39-stable`（更早的版本是`-prestable`、`-testing`）。不要猜，按下面方式解析：

```bash
V=25.3.2.39
gh api "repos/ClickHouse/ClickHouse/git/matching-refs/tags/v$V" --jq '.[].ref'        # → refs/tags/v25.3.2.39-lts
gh release view "v$V-lts" -R ClickHouse/ClickHouse --json tagName,publishedAt,url   # 该补丁版本的 release notes
```
没有`gh`时：在浏览器中打开`https://github.com/ClickHouse/ClickHouse/tags?q=v25.3.2`。

Cloud版本形如`26.2.1.558`；Cloud构建可能包含OSS tag中没有的变更——当bundle来自ClickHouse Cloud（模式`cloud`）时要说明这一点。

发布节奏：每月一个新的minor（`YY.M`）；每年3月（`YY.3`）和8月（`YY.8`）的minor是 **LTS**，支持12个月，其他minor支持3个月。非LTS minor上、版本超过约3个月的on-prem服务器已不再获得修复支持——值得在摘要中说明。

## 2. 哪个源文件回答哪个问题

在客户对应的tag上读文件：`https://github.com/ClickHouse/ClickHouse/blob/<tag>/<path>`（或`gh api repos/ClickHouse/ClickHouse/contents/<path>?ref=<tag> --jq .content | base64 -d`）。

| 问题 | `<tag>`处的文件 |
|---|---|
| 错误码N叫什么 / 该版本上是否存在？ | `src/Common/ErrorCodes.cpp` |
| 查询设置X是否存在、默认值是什么、何时改过？ | `src/Core/Settings.cpp`（≤ 24.x：`src/Core/Settings.h`）；变更历史在`src/Core/SettingsChangesHistory.cpp` |
| 服务器级设置X是否存在（如`max_server_memory_usage`、`background_pool_size`这类config.xml键）？ | `src/Core/ServerSettings.cpp`（≤ 24.x：`ServerSettings.h`） |
| MergeTree表设置X（`parts_to_throw_insert`、`ttl_only_drop_parts`、`min_age_to_force_merge_seconds`……）——默认值与含义 | `src/Storages/MergeTree/MergeTreeSettings.cpp`（≤ 24.x：`MergeTreeSettings.h`） |
| 服务器会发出哪些启动警告（THP、overcommit、内存不足、`max_map_count`）？ | `programs/server/Server.cpp`（搜索`warnings` / `Server::main`） |
| "Too many parts" 消息及其阈值长什么样？ | `src/Storages/MergeTree/MergeTreeData.cpp`（`delayInsertOrThrowIfNeeded`） |
| merge选择 / 为什么小part（数据分片）没有被合并 | `src/Storages/MergeTree/MergeTreeDataMergerMutator.cpp`、`src/Storages/MergeTree/Compaction/`（≥ 25.x） |
| mutation（变更）的队头阻塞行为、被merge锁定的part | `src/Storages/MergeTree/Compaction/MergePredicates/MergeTreeMergePredicate.cpp`、`src/Storages/StorageReplicatedMergeTree.cpp` |
| 该版本上有哪些`system.*`列 | `src/Storages/System/StorageSystem<Table>.cpp`（例如`StorageSystemParts.cpp`、`StorageSystemReplicas.cpp`） |
| ProfileEvents / CurrentMetrics的名称与含义 | `src/Common/ProfileEvents.cpp`、`src/Common/CurrentMetrics.cpp`、`src/Interpreters/ServerAsynchronousMetrics.cpp` |
| Keeper客户端的会话/超时行为 | `src/Common/ZooKeeper/ZooKeeperImpl.cpp`、`src/Common/ZooKeeper/ZooKeeperArgs.cpp` |

规则（不可协商）：**不要描述、确认或推断你未在该版本的源码或官方文档中找到的设置行为或默认值。** 如果在对应tag的`Settings.cpp` / `ServerSettings.cpp` / `MergeTreeSettings.cpp`中找不到，就说 "not present on this version"。

公开文档：`https://clickhouse.com/docs/operations/settings/settings#<setting>`、`/docs/operations/server-configuration-parameters/settings`、`/docs/operations/settings/merge-tree-settings`。文档描述的是*当前*版本——对于较旧的服务器，优先使用对应tag的源码。

## 3. Changelog：客户版本与候选升级版本之间改了什么

- OSS changelog：`https://github.com/ClickHouse/ClickHouse/blob/master/CHANGELOG.md`（每个minor一节，包含 **Backward Incompatible Change**、**New Feature**、**Performance Improvement**、**Bug Fix**）。文档镜像：`https://clickhouse.com/docs/whats-new/changelog`。
- 每个补丁的release notes：`gh release view v25.3.3.42-lts -R ClickHouse/ClickHouse`——列出回移植的bug修复及其PR编号。读取两个补丁之间的各个release，即可对比同一minor的两个补丁。
- Cloud changelog：`https://clickhouse.com/docs/whats-new/cloud`。
- 当某条finding（发现）"resembles a fixed bug" 时，要说明修复PR、包含该修复的第一个版本，以及客户版本是在其之前还是之后。按下面 §4的措辞表述。

## 4. 搜索并引用issue——强制协议

搜索：
```bash
gh issue list -R ClickHouse/ClickHouse --state all --search '"<exact error text>" label:bug' --limit 10
gh issue list -R ClickHouse/ClickHouse --state all --search 'TOO_MANY_PARTS merges not scheduled 25.3' --limit 10
gh pr list    -R ClickHouse/ClickHouse --state merged --search 'fixes #<issue>' --limit 5
```
以**精确的错误码/名称或消息片段**开头——这比症状式描述更有效。用3–4个短查询先广泛搜索，再过滤。

在引用任何issue之前，完成以下四步（改编自本skill继承的支持方法论）：

1. 阅读完整issue正文和关键评论——不只是标题。
2. 找到关联的修复PR/commit（`fixes #N`、`closes #N`）以及包含它的**第一个版本**（release notes或`gh pr view <n> --json milestone,labels`；Cloud的回移植可能不同）。
3. 检查客户的确切版本是否**早于**该修复。
4. 验证客户的场景满足该issue的区分条件（引擎、设置、数据类型、所用功能）——依据bundle，而不是假设。

如果任何一步无法完成，要明确说明。

措辞规则——可以怎么写：

| 情况 | 可以说 | 不可以说 |
|---|---|---|
| 仅有症状相似 | "this pattern resembles ClickHouse/ClickHouse#NNNN" | "you are hitting #NNNN" |
| 四步都完成、条件吻合 | "the evidence is consistent with #NNNN; a ClickHouse engineer should confirm" | "this is a known bug tracked in #NNNN" |
| 某个具体版本中已有修复 | "a fix was merged in PR #MMMM (first in vX.Y.Z); worth confirming whether it applies" | "this was fixed in X.Y" |
| 没有修复 / issue仍开放 | "this may be related to open issue #NNNN" | "this is a known issue" |

绝不要仅凭模式匹配就写 "known issue"。绝不要编造issue编号——只引用你在本次会话中打开并读过的issue。

## 5. 可以回传给用户的实用公开参考

- 文档：`https://clickhouse.com/docs/`（operations、settings、merge-tree、replication、keeper、TTL、异步插入、备份）
- 知识库文章：`https://clickhouse.com/docs/knowledgebase`
- Cloud与OSS的差异：`https://clickhouse.com/docs/cloud/reference/shared-merge-tree`
- 自管理升级指南：`https://clickhouse.com/docs/operations/update`
- Keeper：`https://clickhouse.com/docs/guides/sre/keeper/clickhouse-keeper`

只包含与某条finding直接相关的链接；没有适用内容时省略该节。