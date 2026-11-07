# 健康检查 — 将bundle（诊断包）转化为finding（发现）的规则集

在覆盖情况说明（SKILL.md步骤0）之后，按顺序运行这些检查。每项检查都会指明文件/列、阈值、产生的严重级别、通常意味着什么，以及下一步该做什么。阈值与工具自身的告警（`alerts/*.yaml`）和dashboard（仪表盘）配色保持一致，因此skill永远不会与`dashboard.html`矛盾；其余阈值来自ClickHouse默认值和现场经验，并标记为 *（指南）*。

严重级别词汇：**critical** = 数据当前不可用/有风险 · **warning** = 正在退化或很快会失败 · **info** = 值得了解，无需采取行动 · **ok**。输入文件缺失/按设计为空时，检查为 **n/a**（绝不是“ok”）。务必在结论旁边写出证据行（`file → column → value`）；查询配方在`reading-recipes.md`，模式id在`known-patterns.md`。

只有在固定一个`tables`值之后，才能对`system.query_log_details_7_days`求和（见bundle-layout §4）。只能读取`active = 1`的`system.parts`。

---

## HC-0覆盖情况（关口 — 先做这一步）

| 检查 | 来源 | 结论 |
|---|---|---|
| 版本和模式已知 | `system.version`、文件集（bundle-layout §3） | 说明它们 |
| 实际覆盖的时间窗 | `query_log_details`中的`min/max(time)`；`text_log`中的`min(event_time)` | 如果用户的事件不在该窗口内 → 说明这一点，并在给出任何结论前建议用`-from/-to`重新运行（`running-the-tool.md`） |
| 空/缺失的文件 | `wc -l` | 区分“因为健康而为空”（`crash_log`、`merges`、`replication_queue`、`detached_parts`）和“因为未采集而缺失”（gov、版本门槛、表被禁用） |
| 被截断的日志 | `logs/*`第一行`### support-diagnostic: TRUNCATED` | 注意第一条幸存的日志行并不是起点 |
| SharedMergeTree集群中的一个节点 | `system.settings` `cloud_mode = 1`、`system.tables`中的引擎`Shared*MergeTree`、`system.clusters`的`default`有N个主机、模式`onprem` | bundle描述的是 **N个replica（副本）中的一个**（part（数据分片）、errors、part_log、query_log、text_log都是按replica的）；说明这一点，并建议用`-mode cloud`重新运行以获取集群范围的证据 |
| Host facts（主机信息）描述的是正确的机器 | `host_info.os.hostname`对`system.clusters.host_name` / 日志主机名 | 如果工具是在笔记本上对远程服务器运行的，忽略`host_info`并说明这一点 |
| 未运行的collector（采集器） | `execution_log.txt` *Failed collectors* | 逐一列出并附上其错误；缺失的结果文件属于这一类，而不是空表。Code 159 = 工具的`-query-timeout`；497 = grant（授权）；60/139 = 该服务器上不存在对应的表或配置 |
| collector自身的超时 | 采集时collector用户产生的`query_log_details` / `system.errors` code 159 | collector的`-query-timeout`（默认240 s）在慢速系统表上触发了 — 该文件是部分的；不是客户的问题 |
| 是哪个节点，以及它何时启动 | `execution_log.txt`头部：`node`、`target`、`uptime`、`collection`、`warnings` | `node` ≠ 案件所针对的主机 = 错误的bundle；`uptime` < 24 h = 该节点在采集前重启过，其parts/pools/replication状态是重启后的；`warnings`行提到负载均衡器 = 系统表来自任意replica — **但要读它的前缀**：`Warning:`（onprem/gov）是那种让bundle无法描述任何单一节点的混淆，而`Note:`（cloud）是正常的负载均衡Cloud端点，此时只有共享表来自响应的replica，而按replica的表则分散开来；`collection: onprem → cloud` = 按replica的系统表（query_log、part_log、errors、metric_log、text_log …）覆盖所有replica；共享表（parts、tables、columns、databases、replicas、replication_queue、mutations、detached_parts）与任何cloud bundle一样，是某一个replica的副本；host facts/config/logs都属于collector机器 — 只有在没有混合主机的`warnings:`行时，才把它们归到`node`上 |
| collector自身的grant | 采集时间附近的`system.errors` code 497；`system.databases`只有`system`？（`system.tables`的distinct `database`） | 只显示`system`表的bundle很可能缺少`SHOW DATABASES/TABLES`，而`system.columns`为空但`system.tables`有数据则缺少`SHOW COLUMNS` — 是bundle不完整，不是服务器为空 |

## HC-1可用性与崩溃

| # | 来源 | 规则 | 严重级别 | 含义 / 下一步 |
|---|---|---|---|---|
| 1.1 | `system.crash_log`（或dashboard `crash_log`） | 任意行 | **critical** | 服务器收到了致命信号。读取`signal`、`version`、`query_id`、`trace_full`顶部；如果`query_id`已知，按hash在`query_log_details`中找到该查询；检查`logs/`中同一时间的`<Fatal>`行。同一版本上重复出现相同堆栈 → P-30。 |
| 1.2 | `logs/clickhouse-server.log` | 窗口内`Starting ClickHouse` / `Ready for connections`计数 > 1，或`Received termination signal`与下次启动之间存在间隔 | warning | 重启。与`crash_log`、OOM-killer（`host_info`备注，索取`dmesg`）或部署相关联。 |
| 1.3 | `host_info.os.uptime_seconds`对dashboard `uptime` | 服务器uptime ≪ 主机uptime | info | 服务器比主机更晚重启 — 升级后属预期，否则询问原因。 |
| 1.4 | `system.replicas` | `is_readonly = 1` | **critical**（告警`replica_readonly`） | replica无法接受插入：Keeper会话丢失（`is_session_expired = 1`）、元数据不匹配或磁盘已满。不要推断是哪一种 — `zookeeper_exception`携带从Keeper获取信息时抛出的最后一个异常，`last_queue_update_exception`则是最后一次队列更新失败（例如该版本无法解析的日志条目）；两者在gov中都会被hash，且在该列集之前采集的bundle中不存在。 → HC-3、HC-4、P-40。 |
| 1.5 | `system.errors`、`system.error_log_7_days`（≥ 24.8） | 带有近期`last_error_time`的`TABLE_IS_READ_ONLY` (242) 或`KEEPER_EXCEPTION` (999)；在`error_log`中这些错误码爆发的那些小时 | warning→critical | 与1.4同一族；查看相对于uptime（`asynchronous_metrics.Uptime`）的计数，并用`error_log`看时间线 — 它包含后台线程。 |

## HC-2 part与merge（合并）

| # | 来源 | 规则 | 严重级别 | 含义 / 下一步 |
|---|---|---|---|---|
| 2.1 | `system.parts`活跃，按`(database, table, partition_id)` | `count() > 300` | warning（告警`too_many_parts`）；`≥ 500`在dashboard中显示为红色；≥ 该表的`parts_to_throw_insert`（OSS在 ≥ 23.6默认3000，更早为300；Cloud 10000 — 读取`engine_full`） | **critical** | 插入会被延迟（`parts_to_delay_insert`，默认1000/150），然后被以252拒绝。找出原因：大量小插入（2.3）、merge受阻（2.5/2.6）、分区扇出（2.4）。P-01…P-05。 |
| 2.2 | `system.parts`活跃 | 每个表`countIf(level = 0) > 100` *（指南）* | warning | 未合并的插入块堆积 — merge *此刻*跟不上。P-01。 |
| 2.3 | `system.parts`活跃；`part_log` `NewPart` | 活跃part的`rows`中位数`< 10 000`且有数百个part，或NewPart的`size_in_bytes/count`以KB计 | warning | 微小的插入。批处理 ≥ 10k–100k行或启用异步插入（HC-7）。P-02。 |
| 2.4 | `system.tables.partition_key` + parts | 每个表 > 1000个分区，或分区键具有高基数（hour/minute/tenant-id）*（指南）* | warning | 分区是生命周期工具，不是索引：每次插入涉及N个分区就会创建N个part；merge永远不会跨分区。P-03。 |
| 2.5 | `system.merges` | `elapsed > 3600`且`progress < 0.5`，或`memory_usage`接近上限 | warning | 卡住/巨大的merge占用了一个池槽；这常常是2.1增长而池“看起来空闲”的原因。P-04。 |
| 2.6 | `part_log` | `event_type = 'MergeParts' AND error != 0` — 读取`error`（241 = OOM，236 = aborted，243 = disk，1001/49 = bug，40/226 = corruption）和`distinct_exceptions` | warning→critical | 失败的merge会循环并钉住它们的part（其后的mutation（变更）也会停滞）。merge上的Code 241 → P-11；1001/49 → P-30；40/226 → P-31。 |
| 2.7 | `metric_log` | `max_merge_pool_tasks`在连续数小时等于`background_pool_size`（默认16；检查`configuration/`）*（指南：任何地方的池值 > 256 = 过载）* | warning | Merge池饱和 → part堆积。要么增加CPU/池，要么减少插入/增大插入。P-01。 |
| 2.13 | `metric_log_7_days`的`avg_fetch_pool_tasks`对`background_fetches_pool_size`（`server_settings`，或23.3之前的`settings`） | 每小时平均值 ≥ 该大小的90%，且在过去24小时中 ≥ 3小时出现（告警`fetch_pool_saturated`） | warning | fetch执行器恰好有池大小的槽位，且**没有队列**：一个调度32次fetch的表只能拿到空闲的槽位并退避，悄无声息。在SharedMergeTree上这就是part传播滞后 — replica返回的行数比同级少，重启只能暂时缓解，直到池再次填满。在`metric_log_by_host_3_days`中按主机、对照**实时**大小（`avg_fetch_pool_tasks`对`max(CurrentMetric_BackgroundFetchesPoolSize)` — 大小会在重新加载落地的那一小时跃升；永远不要假设是16，Cloud默认发的是64）。调大该大小（`IncreaseOnly`：配置重新加载即可应用，无需重启），或减少插入/part/表。P-59。 |
| 2.14 | `metric_log_7_days`的`avg_schedule_pool_tasks`对`background_schedule_pool_size` | 每小时平均值 ≥ 该大小的95%，且在过去24小时中 ≥ 3小时出现（告警`schedule_pool_saturated`） | warning | 每张表的周期性任务都在这里运行 — 调度fetch的那几轮、每张表每`shared_merge_tree_leader_update_period_seconds`一次的leader选举、清理 — 因此数以万计的复制表会最先把它钉满，而被钉满的schedule池会**让fetch池空闲**（一台主机上schedule达到大小而fetch低于大小，另一台主机上schedule低于大小而fetch被钉满，是一种原因）。与2.13成对 — 每个replica上两者都达到各自大小就是容量瓶颈的特征。在线调大它（`IncreaseOnly`，配置重新加载）；只有其按任务类型的上限要等下次重启 — 在追赶完成后安排一次滚动重启。P-59。 |
| 2.15 | `system.metric_log_by_host_3_days`（cloud，v0.7） | 按主机：某个池 ≥ 该主机实时`max(CurrentMetric_Background*PoolSize)`的90 %（fetch、merge）/ 95 %（schedule），且在过去24小时中 ≥ 3小时出现；某主机上`sum(ProfileEvent_SharedMergeTreeDataPartsFetchAttempt)` / `sum(ProfileEvent_SharedMergeTreeSelectPartsFor*FetchParts)` < 0.5；实时大小出现阶跃 | warning（钉满 / 下降）· info（大小转变） | 2.13/2.14的按主机形式：*哪个* replica被钉满、在多大的大小上。Attempted ≪ selected = fetch执行器在丢弃工作（没有队列）。大小转变是设置变更，不能证明它是如何落地的：增加与在线配置重新加载*或*带新配置的重启都吻合（`metric_log`跨越重启）；减少只可能是重启或主机被替换，因为池是`IncreaseOnly`。在被钉满的主机上增加之后，接下来的几小时是追赶期（尝试次数 ×3–5，merge池和`cpu_us`上升，`max_parts_active`下降），而不是基线。钉满主机上的fetch尝试次数平稳 ±20 % = 在它的上限上工作（≈ size × 3600 / 每次fetch的秒数），不是卡住。P-59。 |
| 2.16 | `system.merge_tree_settings` + `system.tables`（`engine`、`total_rows`）+ `metric_log_by_host_3_days`选举 | ≥ 5 000张`Shared*`表，且`shared_merge_tree_parts_load_batch_size`和`shared_merge_tree_leader_update_period_seconds`为默认值（32 / 30 s）；> 20 % 的Shared* 表为空或行数 < 100；每主机`sum(ProfileEvent_SharedMergeTreeVirtualPartsUpdatesLeader*Election)` ≥ 100/s | info | P-59的需求侧杠杆：选举/秒 ≈ 表数 / 周期，每次选举都是一个schedule池任务；空表仍然要支付每一项周期性任务。在数万张表的规模上验证过64 / 300。 |
| 2.8 | `part_log` `MergeParts` `size_in_bytes/count` | 平均merge结果很小（KBs–MBs）而part数量很高 | info | merge很忙但无效 — 与上游的2.3相同。 |
| 2.9 | `system.parts`活跃 | `bytes_on_disk > 150 GiB` | warning（告警`large_parts`） | 超过`max_bytes_to_merge_at_max_space_in_pool` — 该part再也不会被合并；`OPTIMIZE FINAL`会忽略该上限。通常没问题；对mutation（每次重写都会触及整个part）和磁盘余量有影响。 |
| 2.12 | `system.part_log_3_days` | 某个小时`NewPart > 100`且`MergeParts (error = 0) = 0`（告警`merges_stalled`） | warning → 如果连续 ≥ 3小时则 **critical** | 插入一直在创建part，却没有任何合并。查看同一小时的`metric_log_coordination`（`ZooKeeperHardwareExceptions`、`ZooKeeperSession = 0`）— 在复制/共享表上merge是通过Keeper分配的 — 再看merge池（2.7）和`text_log_histogram`（`Temporarily pause scheduling`）。P-01、P-57。 |
| 2.10 | `system.parts` | `part_type = 'Compact'`在大表中占主导 | info | Compact part会读取每一列；`min_bytes_for_wide_part`默认10 MiB（Cloud 1 GiB）。只有当查询是列选择性且很慢时才算finding。 |
| 2.11 | `system.detached_parts` | 任意行 | info（告警`detached_parts_exist`）；`reason IN ('broken','broken-on-start','covered-by-broken')` → warning | `broken*` = 损坏/写入中断（P-31）；`ignored`/`clone`/`unexpected` = 复制记账，审查后通常可以安全删除。对`bytes_on_disk`求和 — detached part（分离part）仍然占用磁盘。 |

## HC-3复制与Keeper

| # | 来源 | 规则 | 严重级别 | 含义 / 下一步 |
|---|---|---|---|---|
| 3.1 | `system.replicas`（cloud v0.7：按`hostname`，有问题的行在前，每主机200行 — 某主机恰好200行 = 被截断） | `absolute_delay > 60` s | warning（dashboard）；`> 3600` **critical** *（指南）* | ReplicatedMergeTree：replica落后于日志 — 检查`queue_size`、`inserts_in_queue`对`merges_in_queue`（受fetch限制还是受merge限制）、`queue_oldest_time`。P-41。**SharedMergeTree**（`cloud_mode = 1`、`Shared*`引擎）：没有replication queue（复制队列）— `inserts_in_queue`是**该replica尚未fetch的** level-0 part数量，`absolute_delay`是最旧那个的年龄，`oldest_part_to_get` / `inserts_oldest_time`给出它的名字和日期；多个主机上出现同一个最旧part是一个finding。当别处的merge覆盖了这些part时计数也会下降，所以下降不是追赶完成的证据 — 0才是。配合2.13/2.14/2.15，这是P-59，而不是P-41。 |
| 3.2 | `system.replicas` | `is_session_expired = 1` | **critical** | Keeper会话丢失 → 在重新建立之前只读。HC-3.5。 |
| 3.3 | `system.replicas_problem_tables`（Keeper支撑的列，最多200张表：先是有问题的表，然后是一个健康样本） | `active_replicas < total_replicas` | warning | 某个replica已宕机/不可达；`total_replicas`也计入从未从Keeper中删除的replica（P-43）。 |
| 3.4 | `system.replication_queue` | 任何`type`的`count() > 60` *（指南）*，或`last_exception != ''`（告警`replication_queue_errors`），或`create_time`早于1小时且`is_currently_executing = 0` | warning→critical | 卡住的队列条目。读取`postpone_reason`（例如 "Not executing … because N merges already executing"、"Not merging because part … is not ready"、缺失part）和`last_exception`（fetch失败210/209，40/226损坏）。`num_tries`统计该条目上的失败尝试次数，因此数值很高且`is_currently_executing = 0`表示反复重试并失败，而不是在等待轮到自己。P-41、P-44。 |
| 3.5 | `metric_log` | 任何小时`zk_hw_exceptions > 0`；`zk_transactions`相对基线尖峰 ×5 *（指南）* | warning | Keeper连接丢失/超时。与`errors`/`query_log_details`中的999以及`text_log`的`ZooKeeperClient`/`Session expired`相关联。P-40。 |
| 3.6 | `system.clusters` | `errors_count != 0` | warning（dashboard） | 分布式查询最近未能到达该主机（`estimated_recovery_time`在倒计时）。P-44。 |
| 3.7 | `configuration/` | 生产环境中`<zookeeper>`/`<keeper_server>`的主机数为偶数或1；`<macros>`在多台主机间重复 | warning | Keeper需要奇数法定人数（3或5）；重复的`{replica}`宏 → 224 REPLICA_IS_ALREADY_ACTIVE。P-42。 |
| 3.8 | `metric_log_7_days`（`zk_hw_exceptions`、`zk_transactions`）或`metric_log_coordination_3_days` | **Keeper健康测试，按小时**（见下方区块）：硬件异常 > 1000 **且**事务 < 7天中位数的50 % → *不可用*（告警`keeper_health`）；异常 > 1000且流量 ≥ 50 % → *短暂抖动*（告警`keeper_connection_blips`）；事务 < 中位数的10 % 且无异常 → *空闲或断开*（检查`ZooKeeperSession`） | **critical** / warning | Keeper中断、leader选举或网络分区的客户端一侧：会话过期、请求超时、流经Keeper的工作停止。指出具体小时；这些小时内的其他每个finding（252、319、571、57、merge停滞）都是下游。P-40、P-57。 |
| 3.9 | `system.zookeeper_connection` | `is_expired = 1`；或`session_uptime_elapsed_seconds` ≪ 服务器`Uptime`（`asynchronous_metrics`） | critical / info | 现在没有会话 / 会话在 *connected_time* 重新建立 — 即最后一次过期的时刻。Cloud：`host`或会话年龄与同级不同的replica。 |
| 3.10 | `system.zookeeper_log_errors_1_day`；延迟来自`metric_log_coordination` | 某小时内出现`error IN (ZSESSIONEXPIRED, ZCONNECTIONLOSS, ZOPERATIONTIMEOUT)`的行；`sessions_affected` > 1；每小时`sum(ProfileEvent_ZooKeeperWaitMicroseconds) / sum(ProfileEvent_ZooKeeperTransactions)`相对其中位数 ×10 | warning→critical | 哪些操作失败、影响了多少个会话；Keeper延迟是否在失败*之前*就已退化（先饱和）。大量的`ZNONODE`/`ZNODEEXISTS`/`ZBADVERSION`是正常的控制流。文件缺失 = 表未启用。 |
| 3.11 | `system.distributed_ddl_queue` | `status != 'Finished'`超过10分钟；`exception_code IN (57, 571, 999)`爆发；同一`query`（一个`CREATE OR REPLACE … .tmp.inner_id.*`）在许多`entry`值上重复 | warning | 卡住或重放的DDL：Replicated数据库在会话丢失后会重跑其DDL日志；`57`伴随 "Mapping for table with UUID … already exists" 是重放与首次尝试冲突。P-57。 |
| 3.12 | `system.metrics`（cloud：按`hostname`） | `ZooKeeperSession = 0`；`ReadonlyReplica > 0`；`MetadataFromKeeperCacheObjects`在replica之间相差几个数量级 | critical / warning | *现在*没有会话 / 只读表；在带Keeper的对象存储磁盘上，如果某个replica的元数据缓存几乎为空而同级持有数十万个对象，说明它丢失或丢弃了缓存，将读到过期的元数据（`FILE_DOESNT_EXIST` 107、`The specified key does not exist`）。P-58。 |
| 3.14 | `system.replicated_fetches` | `elapsed` > 600 s的fetch，或两个bundle中出现相同的`result_part_name`且`progress`相同 | info→warning | 中断后的追赶（预期，解释了`absolute_delay`）还是卡死的interserver连接（P-41）。 |
| 3.17 | `system.metric_log_by_host_3_days` | 某主机的`zk_wait_us / zk_transactions` > 集群中位数的2倍且 > 5 ms | warning | 那个replica的Keeper路径很慢 — 是它的网络，或它的会话所在的Keeper成员（`zookeeper_connection.host`）；其他replica正常，所以这不是Keeper范围的事件（3.8）。在P-59案例中，Keeper延迟并没有预示哪些replica会滞后；把它当作一个独立的次要finding。 |
| 3.18 | `keeper/<member>.txt`（`srvr`/`mntr`回复、collector错误），按ensemble（`# ensemble:`） | 两个或更多成员回答`This instance is not currently serving requests` **且没有任何成员在服务** → critical；任何成员`not serving`，或其探测失败（在`srvr`/`mntr`回答之前collector超时或被拒绝）→ warning | critical / warning | `not serving`是Keeper自己的回答 — 成员已启动但没有leader，或仍在加载快照。探测失败只是collector的视角：它无法说明投票者之间的链路，而这些文件只包含*这台*服务器所知道的成员，并非经证实的投票成员关系，因此两者都不能单独断言法定人数丢失。与ClickHouse一侧的P-57成对。 |
| 3.19 | `keeper/<member>.txt`的`lgif`，在同一个ensemble内 | 某成员的`last_log_idx` < **leader的** `first_log_idx` — 且仅在恰好一个leader作答时：follower各自独立压缩日志，选举期间两个成员都可能报告自己是leader，所以没有观察到单一leader就无法下结论；被截断的`lgif`（后面跟着`error: timeout … reply truncated`）不读取 | critical | 该成员只能通过完整快照重新加入（P-61）：在传输设置安全之前让它保持停止。`lgif`即使在一个未提供服务的成员上也会作答。 |
| 3.20 | `keeper/<member>.txt`的`mntr`、`conf`、`srvr` | `zk_latest_snapshot_size` > 2 GiB且`snapshot_transfer_chunk_size = 0` → critical（> 1 GiB → warning）；`write_snapshot_version` < 7 → 逐成员、对照**其自身**版本：26.8+ 上为warning（被钉在默认值8以下），更早版本（那里的默认值）或版本未报告时为info | critical / warning / info | P-61：超过2 GiB且未分块的快照无法发送给落后的成员 — 在出现这种情况之前设置`snapshot_transfer_chunk_size`和`nuraft_snapshot_sync_ctx_timeout_ms`。格式 < 7会让顺序节点计数器保持32位。文件中没有`conf`（较旧的bundle或Keeper）→ 无finding；chunk大小永远不会被假设。快照大小和chunk设置只在一个ensemble内比较 — 绝不要把默认ensemble的快照与辅助ZooKeeper的设置相比；没有`# ensemble:`行时，每个成员按它自己的数字判断。 |
| 3.21 | `system.aggregated_zookeeper_log_by_minute_1_day`（≥ 26.3；cloud：按`hostname`） | 加权`avg_latency_us` ≥ 100 ms的分钟；任何`loss_failures`；*（cloud）* 某replica的延迟中位数在**相同分钟**内（≥ 5个共同分钟）≥ 其同级的10倍且 ≥ 50 ms；`ops` ≥ 当天中位数的10倍（且 ≥ 10 000）的分钟 | warning / warning / warning / info | **何时** Keeper对这台服务器变慢，以及在哪一个replica上。健康服务每次请求约7–20 ms；在一次中断中，Gets从15 ms升到33 ms是第一个信号，比失败早11分钟。某个replica上出现秒级延迟，而其同级在同一个ensemble上保持10–15 ms，那是该replica的会话或它所处的Keeper成员（P-65）；爆发的一分钟是重启，或几个replica同时重新加载part（P-64）。用`text_log_keeper_1_day`匹配丢失的分钟。*（阈值：指南，来自P-62–P-68背后的升级案例）* |
| 3.22 | `system.aggregated_zookeeper_log_by_hour_2_days`（≥ 26.3；cloud：跨replica求和，保留`busiest_replica`） | 始终：按`ops + in_multi`排名靠前的组件；某个组件占窗口的 ≥ 60 %，且其峰值小时 ≥ 其典型小时的10倍（≥ 100 000/h；没有行的小时按0计）；某个操作的每小时加权延迟在 ≥ 100次请求上 ≥ 1 s；≥ 1M次Remove/TryRemove/RemoveRecursive请求（独立 + Multi内部，即`errors` map统计的同一批请求），其中 ≥ 50 % 被回答`ZNONODE` | info / warning / warning / warning | 服务器中**谁**在给Keeper施压。一个后台任务在Keeper上循环（P-68）；非常多个part上的分区操作导致缓慢的Remove/Check/Create（P-67），或早期25.10构建中的Keeper客户端缺陷（P-66）；replica在删除相同的插入去重记录（P-62）。 |
| 3.23 | `system.aggregated_zookeeper_log_paths_1_day`（≥ 26.3；gov：用`shape` + `path_id`代替`path`） | 某个路径类的**成功**创建数在当天超过其成功删除数 ≥ 1M次请求且 ≥ 10倍（这是请求平衡 — 一个`RemoveRecursive`会删除一棵子树 — 因此用`zk_znode_count`确认，当`keeper/`中有该数据时finding会引用它）；某个类占已采集类请求的 ≥ 50 %（≥ 1M；只有当读取或子请求占其 ≥ 50 % 时才标记为读取风暴）；`…/blocks`、`…/async_blocks`、`…/deduplication_hashes`占全部删除的 ≥ 50 %（≥ 1M） | warning / warning / warning | **Keeper中**负载和增长发生在**哪里**。Znode不断堆积直到Keeper耗尽内存，然后就是P-61的快照问题（P-63）；磁盘元数据`/…/mergetree/_objects`树上的批量读取风暴（P-64）；插入去重的频繁变动（P-62）。份额是在最多100个已采集类上计算的，而不是整个Keeper。 |
| 3.13 | `system.databases` | `engine = 'Replicated'`的数量达到数百，再乘以replica数 | info | 每个Replicated数据库都是一个DDLWorker、一组Keeper watch和每个replica的DDL日志 — 当Keeper是finding时，这是一个值得点名的Keeper负载乘数。 |

### Keeper健康 — 每当出现999、319、571或只读replica时运行的检查

Keeper问题要基于**每小时两个计数器**来判断，绝不能只看一个：`ZooKeeperHardwareExceptions`（有多少请求因连接原因失败）和`ZooKeeperTransactions`（Keeper实际完成了多少工作）。两者都在`metric_log_7_days`（`zk_hw_exceptions`、`zk_transactions`）中，并且在`metric_log_coordination_3_days`中还有更多。参考数字：繁忙的服务器每小时10⁶–10⁷ 次事务；健康的小时有0次硬件异常。

| 小时看起来像 | 结论 | 然后读取 |
|---|---|---|
| 异常0，事务 ≈ 中位数 | 健康 | — |
| 异常 > 1000，事务 ≥ 中位数的50 % | **短暂抖动** — 会话丢失并重新建立（Keeper leader变更、一个Keeper节点重启、网络抖动） | `text_log` / `text_log_keeper_1_day`：`Finalizing session`、`Connected to ZooKeeper at <host>`（服务器是否迁移到了另一个Keeper主机？）；该小时的`part_log` 999；`zookeeper_connection.connected_time` |
| 异常 > 1000 **且**事务 < 中位数的50 %（通常 < 10 %） | **不可用** — 会话不断过期且没有工作完成：法定人数丢失、leader反复切换、Keeper内存/磁盘耗尽 | 整条级联：按小时的`query_log` 999/319 → 571/57 → 252；`part_log` `MergeParts` 999，然后是只有`NewPart`而没有`MergeParts`的小时；`zookeeper_log` `ZSESSIONEXPIRED`、`sessions/h`；恢复时的`system.errors` 221/86。索取Keeper的`mntr`输出和Keeper日志（P-57） |
| 异常0，事务 < 中位数的10 % | **空闲或断开** | `system.metrics.ZooKeeperSession`（0 = 无会话）、`zookeeper_connection.is_expired`；如果服务器只是空闲（夜间、周末）就说明这一点 |
| 平均Keeper延迟（来自`metric_log_coordination`的每小时`ZooKeeperWaitMicroseconds / ZooKeeperTransactions`）在失败*之前*的小时里 ×10，或`zookeeper_log_errors`中`ZOPERATIONTIMEOUT`先于`ZSESSIONEXPIRED` | **Keeper先饱和** — 延迟上升，然后会话超时 | Keeper内存/磁盘/快照压力、请求量（`ZooKeeperTransactions`/h）、`Replicated`数据库数量 × replica数（HC-3.13） |

要在`text_log` / `logs/`中grep精确定位的标记（全部来自`ZooKeeperClient` / `clickhouse-serv`线程）：`Session expired`、`Finalizing session <id> … reason:`、`Connected to ZooKeeper at <host>:<port> with session_id`、`Trying to establish a new connection`、`ZooKeeperSend … Code: 210 … Broken pipe`（到Keeper的socket已死）。在Keeper一侧，客户可以在每个成员上运行`echo mntr | nc <keeper> <port>`：`zk_server_state`（一个leader）、`zk_followers` = `zk_synced_followers`、`zk_avg_latency` / `zk_max_latency`、`zk_outstanding_requests`；Keeper日志会在饱和导致的过期之前显示`Receiving request for session N took X ms`，并在leader反复切换期间显示选举消息。

要与**客户端侧**的Keeper噪声区分：带`Bad version` / `No node` / `Node exists`的999是普通的乐观并发重试（P-40 §），永远不会产生硬件异常。

## HC-4磁盘与存储

| # | 来源 | 规则 | 严重级别 | 含义 / 下一步 |
|---|---|---|---|---|
| 4.0 | `system.disks.type`、`system.storage_policies` | 用户表所用的存储策略中出现任何`ObjectStorage`磁盘 | info（上下文） | 数据位于S3/Azure/GCS：本地`free_space`只是缓存/元数据卷；“file doesn't exist”错误指的是blob或其元数据，而不是本地文件。带着这个前提阅读4.x，并在存在时打开`blob_storage_log_7_days`。 |
| 4.9 | `system.blob_storage_log_7_days` | `failed = 1`的行；在107/`NoSuchKey`错误之前不久，某一小时的`Delete`计数远高于基线 | warning→critical | 上传失败 = 数据从未到达bucket的part；删除爆发之后又读取相同的key = 元数据（本地或在Keeper中）指向已删除的blob。P-58。 |
| 4.10 | `system.events`、`metric_log_coordination` | `S3ReadRequestsErrors`、`ReadBufferFromS3RequestsErrors`、`S3WriteRequestsErrors`、`DiskS3*Errors` > 0且上升 | warning | 对象存储请求失败（404 = key缺失，403 = 凭据/策略，5xx/超时 = 端点）。用`metric_log_coordination`按小时拆分。 |
| 4.1 | `system.disks` | **在承载用户数据的磁盘上** `free_pct < 15` — 即某张用户表所用存储策略点名的磁盘（`system.storage_policies.disks` × `system.tables.storage_policy`） | **critical**（告警`disk_space_low`）；`< 5`为红色。**不在**该集合中的磁盘不是数据容量 — 在托管服务上它是本地文件系统缓存，设计上就是要被填满的 — 因此同样两个阈值以更低的严重级别重读：低于15 % 为info，低于5 % 为warning（该文件系统还存放merge和溢写排序的临时文件），达到或高于15 % 则无发现。判断依据是存储策略证据，绝不是采集模式：`-mode cloud`是一种查询布局，也可以指向自管理集群，在那里，位于对象存储tier（分层）旁边的一块高负载本地数据盘仍然算critical。bundle中没有策略证据 = 每块磁盘都要判断 | Merge需要约等于结果大小的空闲空间；低于`min_free_disk_ratio_to_perform_insert`/`keep_free_space_bytes`时插入会以243失败，replica可能变为只读。找出该释放什么：按表的`system.parts`、`detached_parts`、`logs/`大小、系统日志表（`system.parts`中的`system.*_log`）。 |
| 4.2 | `host_info.disks` | 承载`<path>`（来自`configuration/`）的挂载点`used_pct > 85` *（指南）* | warning | 与从操作系统视角看到的4.1相同；也能发现日志卷被填满。 |
| 4.3 | `system.disks.type` | 存在`s3`/`object_storage`磁盘 | info | S3后端：限流下预期出现499错误；merge和读取依赖对象存储延迟。启用P-45检查。 |
| 4.4 | `system.parts`按`database` | 按`sum(bytes_on_disk)`排名，`system`数据库位于前3 *（指南）* | warning | 没有TTL的系统日志表（`text_log`、`trace_log`、`query_log`、`metric_log`）会消耗磁盘和merge能力；它们的merge甚至可能导致OOM（P-17）。建议在`<query_log>`/`<text_log>`配置中设置TTL。 |
| 4.6 | `metric_log_7_days`（onprem/gov）或`metric_log_by_host_3_days`（cloud，按replica） | 每小时，基于覆盖的秒数（`metric_samples` × 采样间隔，从bundle推导 — §4a）：（`OSReadBytes` + `OSWriteBytes`）= 存储流量；`OSIOWaitMicroseconds` = 因存储而阻塞的平均线程数 *（指南：当流量能映射到单个卷 — 单个本地数据盘，或该卷自身的遥测 — 且在连续数小时内接近其配置上限、同时merge或插入很慢，并且阻塞线程数高于bundle的安静时段，则为 **warning**；否则，或在限制未知时，报告速率并索取按卷的遥测 — **info**）* | warning | **瓶颈是设备，不是池。** 面对已饱和的卷，调大`background_pool_size`只是移动队列，而不是提高吞吐 — 解决办法是更快的存储（EBS/gp3吞吐、预置IOPS）或更少的I/O（更少、更大的part）。操作系统的计数器会把每个本地卷加在一起，所以有多个卷时，总量可能达到单个卷的上限而没有任何一个卷饱和。两个信号都无法单独定论：阻塞线程数也会随并发工作增多而上升，而较低的`OSReadBytes` ÷ `ReadBufferFromFileDescriptorReadBytes`（页缓存吸收读取）**不能**排除饱和 — miss和写入仍会到达设备。bundle没有I/O操作计数，所以受IOPS限制的卷可能在其吞吐上限以下就饱和。`Disk*ElapsedMicroseconds`是线程时间，包含缓存命中 — 绝不是吞吐的分母。没有两个小时或更多采样的小时，间隔乃至每个速率都是未知的。有采样缺失的小时发生过停机或启动，其总和可能带有某个计数器的累计值；在核心不统计`OSIOWaitMicroseconds`的地方，它可以全程为0。在非Linux服务器上，`OS*`计数器为0，bundle无法测量设备 — 索取操作系统级遥测。在对象存储上接近0 — 改用S3计数器（HC-4.10）。 |
| 4.5 | `system.parts` | 大表上`data_uncompressed_bytes / data_compressed_bytes < 2` *（指南）* | info | 压缩效果差 — ORDER BY没有把相似值分组、错误的编解码器、高熵字符串。P-50。 |

## HC-5内存与CPU

| # | 来源 | 规则 | 严重级别 | 含义 / 下一步 |
|---|---|---|---|---|
| 5.1 | `metric_log.avg_memory_tracking_bytes`对`host_info.memory.total_bytes` / `cgroup_memory_limit_bytes` / `configuration/`中的`max_server_memory_usage(_to_ram_ratio)` | 每小时平均值 > 有效限制的80 % *（指南）* | warning | 持续压力 → 查询和merge出现241。按消费者拆分：每查询内存（5.3）、merge/mutation（`part_log.peak_memory_usage`）、缓存（`configuration/`的`mark_cache_size`、`uncompressed_cache_size`）、字典（`system.dictionaries.bytes_allocated`）、内存中的主键（对`system.parts.primary_key_bytes_in_memory_allocated`求和）。P-10…P-13。 |
| 5.2 | `host_info.memory` | `available_bytes < 2 GiB` | warning（对应ClickHouse自身的启动警告） | 主机RAM余量耗尽；操作系统可能换页或OOM-kill服务器。 |
| 5.3 | `query_log_details` | `exception_code = 241`的计数和占比；`memory_usage/count`最高的`normalized_query_hash` | warning；如果241命中插入或占查询 > 5 % 则 **critical** *（指南）* | 读取`exception`中的消息类别："(for query)" → 每查询限制（`max_memory_usage`），"(total)" → 服务器限制，"User memory limit" → `max_memory_usage_for_user`，"while pushing to view" → MV链，"AggregatingTransform" → GROUP BY（外部聚合），"FillingRightJoinSide" → JOIN构建侧，`part_log`中的 "while reading column" → merge。P-10、P-11、P-12。 |
| 5.4 | `system.errors` | `CANNOT_ALLOCATE_MEMORY` (173) 或 "Too many open files" (76/107) | **critical** | 操作系统级耗尽：检查`host_info.clickhouse_relevant_tunables`中的`vm.overcommit_memory`、`vm.max_map_count`、`LimitNOFILE`。P-13。 |
| 5.5 | `host_info.cpu.load_avg_1_5_15`对`logical_cpus` | load > 2× CPU数 *（指南）* | warning | 采集时的CPU饱和。检查并发查询（每小时`query_log_details`计数）、merge池（2.7）、其他进程（`top_processes_by_rss`）。 |
| 5.6 | `host_info.memory` | `swap_total_bytes - swap_free_bytes > 0` | info→warning | 正在换页会不可预测地损害延迟；ClickHouse期望没有swap。 |
| 5.7 | `host_info.top_processes_by_rss` | 任何非ClickHouse进程的RSS > RAM的10 % *（指南）* | info | 同机部署的工作负载会争抢内存/页缓存。 |
| 5.8 | `query_log_details` | 每小时`count`相对7天基线增长 ×3，而`memory_usage/count`持平 | info | 内存底线上升是因为流量，而不是泄漏 — 点名流量来源（user/table/hash），而不是分配器。P-12。 |
| 5.9 | `query_views_log_3_days` | 某个视图的`median_peak_memory_usage`在窗口小时桶的后半段比前半段高 ≥ 3倍，而`written_rows`保持在 ±25% 以内 *（指南）* | warning | 单个MV的内存回归，通常发生在升级之后。仅看`sum`不会显示它 — 那反映的是插入量。检查`system.version`和P-54。 |

## HC-6查询负载

| # | 来源 | 规则 | 严重级别 | 含义 / 下一步 |
|---|---|---|---|---|
| 6.1 | `query_log_details` | `exception_code != 0`占`count`的 > 5 % *（指南）*；任何单个错误码 > 50/h（告警`high_exception_rate`） | warning | 对错误码排序；通过`error-codes.md`解析名称；区分`ExceptionBeforeStart`（客户端/schema/access）和`ExceptionWhileProcessing`（运行时）。 |
| 6.2 | `query_log_details` | 202（`TOO_MANY_SIMULTANEOUS_QUERIES`）> 10/h | warning | 达到了`max_concurrent_queries`（OSS默认100）— 突发、失控的池，或慢查询堆积。P-20。注意：在2026年9月之前工具版本的bundle中，`too_many_simultaneous_queries`告警过滤的是252（TOO_MANY_PARTS）— 检查错误码，而不是告警名称。 |
| 6.3 | `query_log_details` `Select`，一个`tables`值 | 平均耗时 > 10 s或`read_bytes/count` > 10 GiB的模式 *（指南）*；按`query_duration_ms/count`的前20名 | info→warning | `--normalized-query-hash`分析的候选。查看`query`样本：`SELECT *`、`FINAL`、ORDER BY前缀上没有过滤、`LIKE '%…%'`、`JOIN`大的右侧、`uniqExact`/`COUNT DISTINCT`。P-21、P-50…P-55。 |
| 6.4 | `query_log_details` | 159（`TIMEOUT_EXCEEDED`）或394（cancelled）集中在某些小时 | warning | 慢查询触发了`max_execution_time` / 客户端超时；检查同一小时是否出现241、高merge活动或Keeper错误 — 是竞争，而不只是查询本身。 |
| 6.5 | `query_log_details`按`user` | 某个非内部用户占`count`或`read_bytes`的 > 70 % *（指南）* | info | 工作负载集中；对后续提示有用。 |
| 6.6 | `query_log_details` | 每小时`query_kind = 'Insert'`计数 ≫ 1000，且`written_rows/count < 1000` *（指南）* | warning | 与从客户端侧看到的HC-2.3是同一个finding。 |
| 6.7 | `query_analysis/profile_events_compare` | `SelectedParts`/`SelectedMarks`/`ReadBufferFromS3*`/`OSCPUWait*`/`MemoryTracker*`中存在较大的`delta` | info | 解释慢与快：剪枝失效、冷缓存、CPU等待、内存。`text_log_parts`："Selected N/N parts" = 没有剪枝。 |

## HC-7插入

| # | 来源 | 规则 | 严重级别 | 含义 / 下一步 |
|---|---|---|---|---|
| 7.1 | `asynchronous_insert_log_7_days` | `status != 'Ok'`的flush > 0 | warning | `ParsingError` = 客户端数据有问题；`FlushError` = flush失败（当`wait_for_async_insert = 0`时客户端从不知情）。P-33。 |
| 7.2 | `asynchronous_insert_log_7_days` | `p90_flush_ms > 5000` *（指南）* | warning | Flush延迟 — 目标上的MV、`async_insert_max_data_size`过大，或merge压力。 |
| 7.3 | `query_log_details` `Insert` | 252/241/242的`distinct_exceptions`很少但计数很高 | warning | 一个反复出现的插入失败；消息会点名该表。 |
| 7.4 | `system.tables` | 一个源表上有 ≥ 5个`MaterializedView`；MV链 ≥ 2跳 *（指南）* | info | 每个插入块都会被每个MV同步处理；MV目标上的TOO_MANY_PARTS意味着*源*收到了太多小插入。P-34。 |
| 7.5 | `text_log` / `system.errors` | `INSERT_WAS_DEDUPLICATED` (389) 或 "Deduplication path already exists" | info→warning | 客户端重试被去重（正常）— 或者每次插入都被去重（token误用）→ P-33。 |
| 7.6 | `system.view_refreshes`（≥ 23.12） | `status = 'Disabled'`，或`exception != ''`，或`last_success_time`早于该视图`REFRESH`间隔的约3倍 *（指南）* | warning | 一个可刷新MV停止了刷新 — 其读取者看到过期数据，而其他地方没有任何错误（`query_views_log`从不记录这些视图）。异常会点名它失败的对象；`Disabled`表示执行了`SYSTEM STOP VIEW`，或服务器在反复失败后放弃。 |
| 7.7 | `query_views_log_3_days` | 任何`status = 'ExceptionWhileProcessing'` | warning→critical | MV在基础part提交后抛出了异常：源有这些行，目标没有。`exception_code`给出类别（swap/rename/detach之后为60，241内存，252 parts，drain期间为341，395是`throwIf`）。**默认情况下父INSERT也会失败**，管道中的每个视图都携带同一条消息 — 一次失败的INSERT会在每个视图上各出现一次，所以**统计罪魁祸首的行数，绝不要对同级求和**；罪魁祸首是`… while pushing to view db.mv`中点名的视图。当`materialized_views_ignore_errors = 1`时，INSERT成功，只有抛出异常的视图被标记。预检按失败视图消息点名的视图对它们分组，每个罪魁祸首报告一个finding；没有文本时（gov）它列出失败的视图而不做选择。P-36。 |
| 7.8 | `query_views_log_3_days` | 一个**停止**了的视图：在其最后一个`written_rows > 0`的小时之后，有 ≥ 2个后续小时（排除仍在填充的最后一个桶）`read_rows > 0`且`written_rows = 0`，并且这段连续时间比该视图自身此前最长的安静间隔更久 | warning | MV运行正常，却在以前会产生行的地方什么也没产出 — 不是过滤型MV，而是上游有东西变了（某个被JOIN的表被替换、行策略、schema变更）。在窗口的**每个**小时`written_rows = 0`的视图只报告为`info`：过滤型MV就是这样，只有它的`as_select`能说明并非如此。刻意不把平稳的`zero_write_executions / executions`比值作为规则 — 它会在每个正确过滤的MV上触发。P-35。 |
| 7.9 | `query_views_log_3_days` | 某个视图每小时的`written_rows`与同一源上的同级出现偏离 | warning | 链中的某一跳开始跟不上。与`system.tables`的依赖关系配合。P-35。 |

## HC-8 Mutation与TTL

| # | 来源 | 规则 | 严重级别 | 含义 / 下一步 |
|---|---|---|---|---|
| 8.1 | `system.mutations` | 年龄 > 3小时且`parts_to_do > 0`（告警`mutation_running_too_long`）；`is_killed = 1`仍然存在（cloud） | warning | 缓慢、被阻塞或队头阻塞：将`parts_to_do_names`与`system.merges`/`part_log`失败中的part（某个merge钉住了该part）以及`command`（繁重的`MODIFY COLUMN`、全表`UPDATE`/`DELETE`）进行比较。P-14。 |
| 8.2 | `system.mutations` | 一张表上 > 100个mutation *（指南）*；接近1000 = `number_of_mutations_to_throw` | warning→critical | 通常是一个失败的mutation被重试，或把`DELETE`/`UPDATE`当作行级操作使用。P-15。 |
| 8.3 | `part_log` | `event_type = 'MutatePart' AND error != 0` | warning | 读取`exception`：53/70/117/349 = 类型转换失败（NULL → non-Nullable、错误的JSON hint）→ 该mutation永远不会完成；kill掉并修复类型。P-15、P-32。 |
| 8.4 | `part_log` | 在DDL含`TTL`的表上，7天内没有`merge_reason IN ('TTLDeleteMerge','TTLRecompressMerge')`事件 *（来自`system.tables.create_table_query`）* | info | TTL未被应用（part已过期？`merge_with_ttl_timeout`默认4小时；`ttl_only_drop_parts`）。检查新part上的`system.parts.delete_ttl_info_min/max` = `1970-…` → TTL未注册。P-18。 |
| 8.5 | `system.tables.engine_full` | 大量使用TTL的表没有`ttl_only_drop_parts = 1` *（指南）* | info | 整part删除远比重写行便宜；当TTL与分区对齐时建议使用。 |

## HC-9字典

| # | 来源 | 规则 | 严重级别 | 含义 / 下一步 |
|---|---|---|---|---|
| 9.1 | `system.dictionaries` | `status IN ('FAILED','FAILED_AND_RELOADING')`或`last_exception != ''` | warning（dashboard红色） | 源不可达/凭据/schema；对其`dictGet`会抛出156/36。 |
| 9.2 | `system.dictionaries` | 采集时`status = 'LOADING'` | info | 加载时间长 — 检查`loading_duration`、`element_count`。 |
| 9.3 | `system.dictionaries` | `bytes_allocated`总计 > RAM的10 % *（指南）*；`cache`布局上`hit_rate < 0.5` | info | 内存预算的消耗者；cache布局的字典“potentially poor performance”。 |

## HC-10配置与主机调优参数（onprem）

对应`internal/dashboard/generator.go`中的`hostChecks()`；只有前三项是ClickHouse自身在启动时警告的内容。

| # | 来源 | 规则 | 严重级别 | 含义 / 下一步 |
|---|---|---|---|---|
| 10.1 | `clickhouse_relevant_tunables.transparent_hugepages` | 包含`always` | warning | 启动警告；设置`madvise`/`never`。会导致延迟尖峰和RSS膨胀。 |
| 10.2 | `vm_overcommit_memory` | `2` | warning | 启动警告；分配会提前失败（173）。使用`0`。 |
| 10.3 | `memory.available_bytes` | `< 2 GiB` | warning | 启动警告；见HC-5.2。 |
| 10.4 | `cgroup_memory_limit_bytes` | 存在且`< memory.total_bytes` | info→warning | 容器限制才是真正的天花板；ClickHouse ≥ 22.x会为`max_server_memory_usage_to_ram_ratio`遵循cgroup，更早版本可能不会 — 与`metric_log`内存比较。 |
| 10.5 | `clickhouse_open_files_soft/hard` | `< 500000` *（指南；软件包设置为500000）* | warning | 在大量part/连接下出现 "Too many open files"（76/107）。 |
| 10.6 | `vm_max_map_count` | `< 262144` *（指南）* | info | 当活动映射超过它的90 % 时ClickHouse报错。 |
| 10.7 | `fs_nr_open`、`fs_file_max`、`cgroup_cpu_max`、`vm_swappiness`、THP defrag | — | info | 作为事实报告；ClickHouse没有记录目标值。 |
| 10.8 | `system.settings` / `system.server_settings`（`changed = 1`），然后是`configuration/` | `max_server_memory_usage_to_ram_ratio` > 0.9、`max_concurrent_queries`被调低、`background_pool_size`在没有增加CPU的情况下被调高、`mark_cache_size` > RAM的25 %、每个profile的`max_memory_usage` > 服务器限制、`parts_to_throw_insert`被调高到数万 *（指南）* | info→warning | 标记偏离默认值的项并解释权衡；在评论之前确认每个设置在该版本上存在（`clickhouse-source.md` §2）。 |
| 10.9 | `configuration/` | `<text_log>`/`<query_log>`/`<trace_log>`缺少`<ttl>`/`<partition_by>`，或生产环境中`text_log`为`trace`级别 | info | 与HC-4.4相关。 |
| 10.10 | `configuration/` | `<listen_host>::</listen_host>`或`0.0.0.0`且`users.d`的`<networks>`不受限；默认用户没有密码 | warning | 安全暴露 — 中立地报告。 |

## HC-11日志

| # | 来源 | 规则 | 严重级别 | 含义 / 下一步 |
|---|---|---|---|---|
| 11.1 | `system.text_log`、`logs/*.err.log` | `Code: NNN`直方图前5；任何`<Fatal>` | info→critical | 用`error-codes.md`映射错误码；Fatal行 = HC-1.1。 |
| 11.2 | `logs/` | 同一条消息在一分钟内重复 > 100次 | warning | 重试循环（merge失败、fetch失败、字典重载）— 计数即可，不要阅读；消息会点名对象。 |
| 11.3 | `logs/clickhouse-server.log`启动块 | `Available memory … too low`、`Transparent hugepages`、`overcommit`、`TaskStats`/`NETLINK`、`max_map_count`、"Effective user of the process" | info | ClickHouse自身的环境警告；印证HC-10。 |
| 11.5 | `system.text_log_histogram_1_day` | 每小时Error/Fatal `count`相对安静小时 ×10；只在事件小时出现的`logger_class` | info→warning | 服务器自身日志按组件的一天时间线：它告诉你*何时*和*哪一类*，然后`text_log`/`logs/`给出确切消息。典型的事件类别：`ZooKeeperClient`、`DDLWorker`、`DatabaseReplicated`、`InterserverIOHTTPHandler`、`MergeTreeBackgroundExecutor`、`executeQuery`、`TCPHandler`、`DiskS3`/`*::MetaInKeeper`。 |
| 11.4 | `text_log` | `min(event_time)`与`max(event_time)`相差仅几分钟 | 对“不存在”类结论为n/a | 达到2000行上限 — 该切片覆盖的是几分钟而不是一天；绝不要据此得出“过去24小时没有错误”的结论。 |

## 生成scorecard（评分卡）

每个领域（HC-1…HC-11）一行，status = 该领域内最差的检查，evidence = 最具体的那一个数字（`system.parts: db.t partition 202609 has 812 active parts`）。然后按严重级别列出findings，每条都指向检查id和模式id。所有输入都缺失的领域标记为 *not collected* 并写明原因（gov、版本、表被禁用）以及重新采集的命令。