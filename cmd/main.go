package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/internal/alert"
	"clickhouse-diagnostic/internal/bundle"
	"clickhouse-diagnostic/internal/clustercollect"
	"clickhouse-diagnostic/internal/collectmode"
	"clickhouse-diagnostic/internal/config"
	"clickhouse-diagnostic/internal/dashboard"
	"clickhouse-diagnostic/internal/hostinfo"
	"clickhouse-diagnostic/internal/keeper"
	"clickhouse-diagnostic/internal/logfiles"
	"clickhouse-diagnostic/internal/query"
	"clickhouse-diagnostic/internal/runlog"
	"clickhouse-diagnostic/internal/version"
	"clickhouse-diagnostic/pkg"

	"golang.org/x/term"
)

func main() {
	// `clickhouse-diagnostic cluster …` 会为自管理集群的每台主机运行一次
	// 本二进制；它永远不会走到下面的标志解析。
	if len(os.Args) > 1 && os.Args[1] == "cluster" {
		os.Exit(clustercollect.Main(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}

	// 定义命令行标志
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "用法：%s [flags]\n", os.Args[0])
		flag.PrintDefaults()
	}
	hostFlag := flag.String("host", "", "ClickHouse 主机")
	portFlag := flag.String("port", "", "ClickHouse 端口")
	userFlag := flag.String("user", "", "用户名")
	passwordFlag := flag.String("password", "", "密码（出于安全原因不推荐）")
	protocolFlag := flag.String("protocol", "", "协议（http 或 https）")
	modeFlag := flag.String("mode", "onprem", "查询模式（cloud、onprem、gov）")
	outputDirFlag := flag.String("output-dir", "./clickhouse_results", "结果输出目录")
	configDirFlag := flag.String("config-dir", "", "要采集的 ClickHouse 配置目录（默认：/etc/clickhouse-server/config.d/）")
	skipConfigFlag := flag.Bool("skip-config", false, "跳过采集配置文件")
	skipArchiveFlag := flag.Bool("skip-archive", false, "跳过创建结果与配置的归档包")
	skipDashboardFlag := flag.Bool("skip-dashboard", false, "跳过生成 HTML dashboard")
	airgapFlag := flag.Bool("airgap", false, "将 Chart.js 内嵌进 dashboard.html，使其在无网络访问时也能打开（见 airgap/README.md）")
	skipAlertsFlag := flag.Bool("skip-alerts", false, "跳过评估 alert rule")
	singleNodeFlag := flag.Bool("single-node", false, "在 SharedMergeTree 集群（cloud_mode = 1）上，让 -mode onprem 只采集本节点，而不是切换到通过 clusterAllReplicas(default, ...) 的 cloud 采集")
	alertsDirFlag := flag.String("alerts-dir", "./alerts", "包含 alert YAML 规则文件的目录")
	saltFlag := flag.String("salt", "", "gov 模式的哈希 salt（8–64 个字母数字字符；为空时交互式提示）")
	queryIDFlag := flag.String("query-id", "", "运行以该 query_id（UUID）为中心的查询分析")
	normalizedHashFlag := flag.String("normalized-query-hash", "", "运行以该 normalized_query_hash（uint64）为中心的查询分析")
	fromFlag := flag.String("from", "", "采集窗口的起点（RFC3339 或 YYYY-MM-DD）。"+
		"覆盖每个查询默认的回看范围（大多数日志表为 7 天，text_log 为 1 天）。"+
		"同时设置查询分析窗口")
	toFlag := flag.String("to", "", "采集窗口的终点（RFC3339 或 YYYY-MM-DD；默认：当前时间）。"+
		"同时设置查询分析窗口")
	analysisDirFlag := flag.String("analysis-dir", "./queries.query_analysis", "包含查询分析 SQL 文件的目录")
	hostInfoFlag := flag.String("host-info", "auto", "采集主机 OS/内核/CPU/内存/磁盘/进程信息：auto|on|off。"+
		"auto = onprem 时为 on，cloud 时为 off（否则工具会剖析它所在的机器，而不是托管服务器），"+
		"gov 时为 off（从不采集——主机名和命令行无法哈希）")
	logsFlag := flag.String("logs", "auto", "从磁盘采集 ClickHouse 服务器日志文件：auto|on|off。"+
		"auto = onprem 时为 on，cloud 时为 off（日志位于托管节点而非本地），gov 时为 off")
	logsDirFlag := flag.String("logs-dir", "", "存放 ClickHouse 服务器日志的目录。默认：从服务器配置的 <log>/<errorlog> 中发现，回退到 /var/log/clickhouse-server")
	logsMaxMBFlag := flag.Int("logs-max-mb", 50, "采集日志的单个文件大小上限，单位为 MiB。更大的文件会被尾部截断（保留最近的部分）")
	logsArchivesFlag := flag.Bool("logs-include-archives", false, "同时采集轮转日志归档（*.gz、*.zst）。默认关闭——它们通常比 bundle 其余部分加起来还大")
	keeperMntrFlag := flag.String("keeper-mntr", "auto", "向 system.zookeeper_connection 中的每个 Keeper 成员发送 ruok / isro / lgif / srvr / mntr / conf / dirs "+
		"（leader 或 follower、Raft 日志位置、快照大小与设置、未完成请求、延迟、版本），并写入 keeper/<host>_<port>.txt：auto|on|off。"+
		"auto = onprem 和 gov 时为 on（工具运行在集群旁；gov 会对主机名做哈希），cloud 时为 off（托管服务的 Keeper 不可达）")
	collectTextLogFlag := flag.Bool("collect-text-log", false, "采集 system.text_log 中限定时间的一片数据。需要 --from 和 --to；gov 模式不可用")
	textLogLevelFlag := flag.String("text-log-level", "", "--collect-text-log 的最低严重级别（Fatal|Critical|Error|Warning|Notice|Information|Debug|Trace）。默认：所有级别")
	textLogLimitFlag := flag.Int("text-log-limit", 0, fmt.Sprintf("--collect-text-log 的行数上限（默认 %d）", query.DefaultTextLogRowLimit))
	outputFormatFlag := flag.String("output-format", query.DefaultOutputFormat.Name,
		fmt.Sprintf("查询结果的序列化格式：%s。jsonl 每行一个自描述 "+
			"JSON 对象（可用 grep 和 jq 处理）；native 是 ClickHouse 二进制格式；"+
			"tsv 在前两行携带名称和类型。三者中的 64 位整数都精确", query.OutputFormatNames()))
	queryTimeoutFlag := flag.Duration("query-timeout", pkg.DefaultQueryTimeout,
		"每个采集查询的服务端限制（max_execution_time）。HTTP 客户端会多等 60 秒，"+
			"因此慢查询会在客户的 query_log 中以清晰的 Code: 159 失败，而不是客户端静默断开、"+
			"服务器却仍在执行。0 表示同时禁用两项限制。")
	collectFlag := flag.String("collect", "auto", "采集多少：auto（默认）先探测服务器负载，仅在其吃力时（响应缓慢、接近 max_concurrent_queries 或内存上限、高负载、只读副本）采集必要且便宜的表——使用 1 天窗口、每查询 30 秒超时和温和的查询设置——并在查询开始超时时于运行中途切换到 essential；essential 强制使用该模式；full 始终采集全部")
	timeBudgetFlag := flag.Duration("time-budget", 0, "采集器的总时限；届时尚未运行的内容会被跳过并列入 execution_log.txt。默认：full 采集无限制，essential 采集为 3m")
	dryRunFlag := flag.Bool("dry-run", false, "列出工具将执行的每个查询（包括各自触及的系统表，以及每个 SELECT 的 EXPLAIN ESTIMATE）后退出。不会写入结果，也不会创建归档包。EXPLAIN ESTIMATE 是只读元数据查询——它报告该 SELECT 将会扫描的行/mark/part，而不读取任何数据。")

	// 解析命令行标志
	flag.Parse()

	// -mode 默认是 onprem，因此其值无法说明运维人员是主动选择了 onprem，
	// 还是根本没写。flag.Visit 只报告命令行上实际出现的标志，这正是
	// resolveMode 需要的区分：既能对拼写错误大声报错，又能保留默认值。
	// -password 以同样方式记录：显式的 -password "" 在读取 CLICKHOUSE_PASSWORD
	// 之前会触发提示，现在仍然必须如此。
	modeExplicit, passwordExplicit, timeoutExplicit := false, false, false
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "mode":
			modeExplicit = true
		case "password":
			passwordExplicit = true
		case "query-timeout":
			timeoutExplicit = true
		}
	})
	collectRequested, err := collectmode.Parse(*collectFlag)
	if err != nil {
		fmt.Printf("错误：%v\n", err)
		os.Exit(1)
	}
	if *timeBudgetFlag < 0 {
		fmt.Println("错误：-time-budget 不能为负数")
		os.Exit(1)
	}

	// 用默认值初始化变量
	var (
		host           = *hostFlag
		port           = *portFlag
		username       = *userFlag
		password       = *passwordFlag
		protocol       = *protocolFlag
		mode           = *modeFlag
		outputDir      = *outputDirFlag
		configDir      = *configDirFlag
		skipConfig     = *skipConfigFlag
		skipArchive    = *skipArchiveFlag
		dryRun         = *dryRunFlag
		skipDashboard  = *skipDashboardFlag
		skipAlerts     = *skipAlertsFlag
		singleNode     = *singleNodeFlag
		alertsDir      = *alertsDirFlag
		govSalt        = *saltFlag
		queryID        = *queryIDFlag
		normalizedHash = *normalizedHashFlag
		fromStr        = *fromFlag
		toStr          = *toFlag
		analysisDir    = *analysisDirFlag
		hostInfoMode   = *hostInfoFlag
		logsMode       = *logsFlag
		logsDir        = *logsDirFlag
		logsMaxMB      = *logsMaxMBFlag
		logsArchives   = *logsArchivesFlag
		keeperMntrMode = *keeperMntrFlag
		collectTextLog = *collectTextLogFlag
		textLogLevel   = *textLogLevelFlag
		textLogLimit   = *textLogLimitFlag
	)
	// --from / --to 收窄每个声明了采集窗口的查询的窗口。在任何阻塞操作
	//（密码提示、连接）之前解析，这样格式错误或起止颠倒的窗口会立即失败。
	//
	// 与 analysisOpts 分开解析：标志缺省时，analysis 会替换为自己以事件
	// 时间为基准的默认值，而采集查询则回退到它们各自声明的按查询默认值。
	collectFrom, err := query.ParseTimeFlag(fromStr)
	if err != nil {
		fmt.Printf("错误：无效的 --from：%v\n", err)
		return
	}
	collectTo, err := query.ParseTimeFlag(toStr)
	if err != nil {
		fmt.Printf("错误：无效的 --to：%v\n", err)
		return
	}
	if !collectFrom.IsZero() && !collectTo.IsZero() && collectTo.Before(collectFrom) {
		fmt.Println("错误：--to 早于 --from")
		return
	}
	// 早前故障遗留的 -to 会在那一刻静默截断每张日志表，而 system.errors、
	// system.parts 和其他时间点表仍描述当前状态——于是 bundle 看起来完整，
	// 最近几小时却根本不在其中。一个真实案例在 24 日运行了 -to 2026-09-16，
	// 丢掉了此后发生的两次会话丢失。
	if !collectTo.IsZero() {
		if lag := time.Since(collectTo); lag > 2*time.Hour {
			fmt.Printf("警告：-to 距今已有 %.0f 小时，因此 query_log、part_log、metric_log 和 "+
				"text_log 都停在该时刻。此后的任何内容都会从 bundle 中缺失，而不是显示为健康——"+
				"如果问题仍在发生，请放宽 -to。\n", lag.Hours())
		}
	}

	outputFormat, err := query.ParseOutputFormat(*outputFormatFlag)
	if err != nil {
		fmt.Printf("错误：%v\n", err)
		return
	}

	// --dry-run 按定义是只读的——静默那些会向磁盘写入空/垃圾产物的副作用。
	if dryRun {
		skipConfig = true
		skipArchive = true
	}

	// 获取缺失参数的用户输入
	if err := getUserInput(&protocol, &host, &port, &username, &password, &mode, &configDir, skipConfig, modeExplicit, passwordExplicit); err != nil {
		// 以非零码退出。被拒绝的模式若以 0 退出，几乎不比它所替代的静默
		// onprem 回退更好：这里保护的脚本化采集检查的是 $?，而不是 stdout。
		// 这么早还没有任何 defer。
		fmt.Printf("错误：%v\n", err)
		os.Exit(1)
	}

	// gov 绝不采集配置文件：它们内嵌原始主机名——<macros> 的 shard/replica
	// 名、<remote_servers>、<zookeeper> 主机——正是 gov 哈希要保护的标识符，
	// 而 XML 树一旦哈希就无法保持可合并或可比较。与下面的 host facts 和
	// 日志拒绝一样，这是无条件的。
	if govWithholdsConfig(mode) && !skipConfig {
		skipConfig = true
		fmt.Println("gov 模式：不采集配置文件 " +
			"（它们内嵌了无法哈希的原始主机名——macros、remote_servers、zookeeper）。")
	}

	// cloud 在 ClickHouse 服务器之外的某处运行工具，因此默认没有可读取的
	// 本地配置目录。显式的 -config-dir 仍可主动启用（自托管的
	// SharedMergeTree 集群会从某个节点以 -mode cloud 运行），所以这是一条
	// 默认行为，而不是像上面 gov 那样的拒绝。
	if cloudSkipsConfig(mode, configDir) && !skipConfig {
		skipConfig = true
		fmt.Println("cloud 模式：不采集配置文件 " +
			"（服务器的配置目录不在此机器上；要采集请传入 -config-dir）。")
	}

	// 根据运行模式解析这两个本地文件系统 collector。二者读取的都是工具
	// 正在执行的那台机器，而这只在 onprem 部署中才是 ClickHouse 服务器——
	// 因此默认值随模式变化，而不是简单的 on/off。
	//
	// 这必须在 getUserInput 之后运行：该调用会规范化模式（把 "-mode GOV"
	// 转成小写），并允许交互式用户在提示符处修改它。若按原始标志值解析，
	// `-mode GOV -host-info=on` 就会把 host facts 混进 gov bundle。
	skipHostInfo, err := resolveLocalCollector(hostInfoMode, mode, "host-info")
	if err != nil {
		fmt.Printf("错误：%v\n", err)
		return
	}
	skipLogs, err := resolveLocalCollector(logsMode, mode, "logs")
	if err != nil {
		fmt.Printf("错误：%v\n", err)
		return
	}
	// Keeper four-letter command 通过网络发往 Keeper 成员，而不是访问本地
	// 文件系统，因此它们有自己的解析器（允许 gov——主机会被哈希；cloud
	// 默认关闭，因为托管服务的 Keeper 不可达）。与上面两个解析器一样，
	// 都在 getUserInput 之后运行。
	skipKeeper, keeperWarn, err := keeper.Resolve(keeperMntrMode, mode)
	if err != nil {
		fmt.Printf("错误：%v\n", err)
		return
	}
	if keeperWarn != "" {
		fmt.Println(keeperWarn)
	}
	if dryRun {
		// host facts 和日志文件是文件系统拷贝而非查询；没有值得“预览”的
		// 内容，因此跳过它们，而不是写入 dry run 承诺不会写入的文件。
		skipHostInfo = true
		skipLogs = true
	}

	// 将 mode 映射到 queries 目录
	queriesDir, err := getQueriesDir(mode)
	if err != nil {
		fmt.Printf("错误：%v\n", err)
		os.Exit(1)
	}

	// 若输出目录不存在则创建
	if err := os.MkdirAll(outputDir, 0750); err != nil {
		fmt.Printf("创建输出目录失败：%v\n", err)
		return
	}

	// 检查 queries 目录是否存在
	if _, err := os.Stat(queriesDir); os.IsNotExist(err) {
		fmt.Printf("错误：queries 目录 '%s' 不存在\n", queriesDir)
		return
	}

	// 显示连接信息
	fmt.Printf("\n正在连接 ClickHouse：%s://%s:%s\n", protocol, host, port)
	if username != "" {
		fmt.Printf("使用用户认证：%s\n", username)
	}
	fmt.Printf("使用查询模式：%s（查询来自：%s）\n", mode, queriesDir)

	// 配置文件稍后与其他磁盘 collector 一起采集，因为它们会写入本次运行的
	// 结果目录，而该目录要等 ExecuteQueries 创建后才存在。

	// 创建 ClickHouse 客户端
	client := pkg.NewClickHouseClient(protocol, host, port, username, password)
	if *queryTimeoutFlag < 0 {
		fmt.Println("错误：-query-timeout 不能为负数（使用 0 禁用该限制）。")
		return
	}
	client.SetQueryTimeout(*queryTimeoutFlag)
	// 一个 replica 下线不能让每个 clusterAllReplicas 查询都失败：否则下面的
	// SharedMergeTree fan-out 探测会失败，运行会静默地只采集一个节点，而在
	// cloud 模式下每个按 replica 的 collector 都会报错。不分发的查询不受影响。
	// 哪些 replica 未应答由 system.replicas_answered 记录。
	client.SetSetting("skip_unavailable_shards", "1")
	if *queryTimeoutFlag == 0 {
		fmt.Println("警告：-query-timeout=0——查询在服务端不受限制地运行，客户端会无限等待。慢查询将拖住采集而不是失败。")
	}

	// 检查 ClickHouse 服务器版本
	fmt.Println("正在检查 ClickHouse 服务器版本...")
	versionResult, err := client.ExecuteQuery("SELECT version()")
	if err != nil {
		fmt.Printf("检查 ClickHouse 版本失败：%v\n", err)
		return
	}

	// 解析版本字符串
	parser := version.NewParser()
	serverVersion, err := parser.ParseClickHouseVersion(versionResult)
	if err != nil {
		fmt.Printf("解析 ClickHouse 版本失败：%v\n", err)
		return
	}

	fmt.Printf("ClickHouse 服务器版本：%d.%d.%d.%d\n",
		serverVersion.Major, serverVersion.Minor, serverVersion.Patch, serverVersion.Build)

	// 没有对象列举授权时，运行仍会“成功”：受保护的系统表以 497 失败，
	// system.databases/tables/columns 被过滤为空，于是归档只有几百 KB，
	// 完全描述不了任何用户数据。某个集群的五个节点就是这样被采集并在无人
	// 察觉的情况下发出的。要在还能补救的地方说出来，而不是写进执行日志。
	//
	// dry-run 下不执行：那里不会真正发送查询（客户端返回合成的空响应），
	// 因此无法从中得出任何结论。
	if !client.IsDryRun() {
		dbCount, dbErr := client.ExecuteQuery(
			"SELECT count() FROM system.databases WHERE name NOT IN ('system','INFORMATION_SCHEMA','information_schema')",
		)
		grants := func() {
			fmt.Println("    授予诊断权限后重新运行：")
			fmt.Println("    GRANT SHOW DATABASES, SHOW TABLES, SHOW COLUMNS ON *.* TO " + username + ";")
			fmt.Println("    GRANT SELECT ON system.* TO " + username + ";")
		}
		// 三种结果，其中只有两种与权限有关。查询被拒绝只有在服务器明说
		//（Code: 497、ACCESS_DENIED）时才是授权问题；超时、网络错误或旧版
		// 服务器的解析错误都不是，为这些情况让运维人员去 GRANT 会误导他们。
		switch databaseVisibilityVerdict(dbCount, dbErr) {
		case visibilityDenied:
			fmt.Printf("警告：用户 '%s' 无权读取 system.databases（%v），因此大多数 collector "+
				"会以 Code: 497 失败，bundle 将几乎为空。\n", username, dbErr)
			grants()
		case visibilityNone:
			fmt.Printf("警告：用户 '%s' 看不到 system 之外的任何数据库，因此此 bundle 将不会描述你的任何表、part 或 replica——文件会是空的，而不是缺失。\n", username)
			grants()
		case visibilityUnknown:
			fmt.Printf("警告：无法检查用户 '%s' 能看到哪些数据库（%v）。采集继续；"+
				"如果 system.databases、system.tables 和 system.columns 返回 0 行，"+
				"则需要授予诊断权限：\n", username, dbErr)
			fmt.Println("    GRANT SHOW DATABASES, SHOW TABLES, SHOW COLUMNS ON *.* TO " + username + ";")
			fmt.Println("    GRANT SELECT ON system.* TO " + username + ";")
		}
	}

	// 执行日志：每个 collector 及其结果与耗时、每条 alert rule、每个阶段——
	// 以 execution_log.txt 写入 bundle，读者可据此区分失败的 collector 和
	// 空表，并看出哪些查询在这台服务器上开销大。
	rec := runlog.New().WithGov(mode == "gov")

	// 谁应答的，以及它已经运行了多久。一次升级处理中的两个 bundle 被误读
	// 了节点——一个经由负载均衡器采集，一个是在案例所涉主机的同级节点上
	// 采集的——还有第三个描述的是采集前几分钟刚重启的 replica。探测只是
	// 三次廉价的往返；失败时（无授权、dry run）头部就没有节点行，警告也
	// 保持静默。
	var identity nodeIdentity
	if !dryRun {
		identity = probeNodeIdentity(client)
	}

	// SharedMergeTree 集群按 replica 保留系统表；在那里使用 -mode onprem
	// 只会采集 N 个节点中的一个。运行会切换到 cloud 采集（按 replica 的系统表
	// 通过 clusterAllReplicas 读取；system.parts 等共享表保持单副本——见
	// query.SharedSystemTables），同时保留本节点的 host facts、配置和日志
	// 文件——仅用 cloud 模式绝不会采集这些。从此以后 effectiveMode 决定查询集、
	// alert 和 dashboard；`mode` 仍表示运维人员请求的内容。
	// dry-run 下跳过：该约定只允许 version / preflight / EXPLAIN 元数据查询
	// 到达服务器，而这些探测都不属于其中。
	effectiveMode := mode
	collectionNote := ""
	serverIsSMT := mode == "cloud"
	if mode == "onprem" && !dryRun {
		if cloudMode, err := client.ExecuteQuery("SELECT value FROM system.settings WHERE name = 'cloud_mode'"); err == nil && isSharedMergeTree(cloudMode) {
			serverIsSMT = true
			var fanoutErr error
			if !singleNode {
				_, fanoutErr = client.ExecuteQuery("SELECT count() FROM clusterAllReplicas(default, system.one)")
			}
			d := decideSMTCollection(fanoutErr, singleNode, sharedMergeTreeHint(mode, cloudMode))
			fmt.Println(d.Message)
			collectionNote = d.Note
			if d.EffectiveMode != mode {
				effectiveMode = d.EffectiveMode
				if queriesDir, err = getQueriesDir(effectiveMode); err != nil {
					fmt.Printf("错误：%v\n", err)
					os.Exit(1)
				}
				fmt.Printf("使用查询模式：%s（查询来自：%s）\n", effectiveMode, queriesDir)
			}
		}
	}

	// 身份警告在这里派生，而不是在上面的探测处，因为负载均衡端点的解读
	// 方式取决于 bundle 实际是如何采集的——而这只有在 SharedMergeTree 决策
	// 之后才知道。一次切换到 cloud 采集的 -mode onprem 运行会在头部写入
	// `mode: cloud` 并分发按 replica 的表，因此强烈的 onprem 警告
	//（“bundle 不描述任何单一节点”）会与它自己的 bundle 相矛盾。
	// 读者看到的是 effectiveMode，因此由 effectiveMode 决定措辞。混合主机
	// 警告不受影响：这种混合运行确实从 collector 所在机器读取 host facts、
	// 配置和日志，nodeWarnings 仍会说明这一点。
	if !dryRun {
		localHostname, _ := os.Hostname()
		warnings := nodeWarnings(identity, host, localHostname, effectiveMode, !skipHostInfo || !skipLogs || !skipConfig)
		for _, w := range warnings {
			fmt.Println(w)
		}
		// 警告会点名主机；gov 不让主机名进入日志。负载均衡那条在 cloud
		// 采集中是 "Note:"，其他情形是 "Warning:"——见 nodeWarnings——pre-pass
		// 读取的是这个前缀，而不是该行是否存在。
		if len(warnings) > 0 && mode != "gov" {
			rec.SetMeta("warnings", strings.Join(warnings, " | "))
		}
	}

	rec.SetMeta("mode", effectiveMode)
	rec.SetMeta("server", fmt.Sprintf("%d.%d.%d.%d", serverVersion.Major, serverVersion.Minor, serverVersion.Patch, serverVersion.Build))
	if mode == "gov" {
		// 主机是客户基础设施；gov 会在每个结果文件中哈希主机名，因此日志也
		// 不得以明文携带它。
		rec.SetMeta("target", fmt.Sprintf("（gov 模式下已隐去主机）via %s", protocol))
	} else {
		rec.SetMeta("target", fmt.Sprintf("%s:%s (%s)", host, port, protocol))
		if identity.Err == nil && identity.Host != "" {
			node := identity.Host
			if identity.FQDN != "" && identity.FQDN != identity.Host {
				node += " (" + identity.FQDN + ")"
			}
			rec.SetMeta("node", node)
		}
	}
	if identity.Err == nil && identity.UptimeSeconds > 0 {
		rec.SetMeta("uptime", humanUptime(identity.UptimeSeconds))
		rec.SetMeta("uptime-seconds", fmt.Sprintf("%d s", identity.UptimeSeconds))
	}
	if collectionNote != "" {
		rec.SetMeta("collection", collectionNote)
	}
	if collectFrom.IsZero() && collectTo.IsZero() {
		rec.SetMeta("window", "each query's own default look-back (7 days for most log tables, 3 days for part_log and metric_log_coordination, 1 day for text_log and zookeeper_log)")
	} else {
		rec.SetMeta("window", describeWindow(collectFrom, collectTo))
	}
	// 采集多少：负载探测决定起始模式（auto），而当服务器开始超时时，
	// watchdog 仍可把 full 运行切换为 essential。
	decision, watch := setupCollection(client, collectRequested, *timeBudgetFlag, dryRun)
	collectLine := decision.Summary()
	if decision.Mode == collectmode.Essential {
		notes := applyEssential(client, timeoutExplicit)
		why := strings.Join(decision.Reasons, "; ")
		if why == "" {
			why = "通过 -collect essential 请求"
		}
		watch.Trip(why)
		collectLine += " — " + strings.Join(append(notes, "1 天窗口", "跳过 full 层级的 collector"), "; ")
	}
	watch.OnTrip(func(reason string) {
		notes := applyEssential(client, timeoutExplicit)
		fmt.Printf("\n正在切换到 essential 采集：%s（%s）\n", reason, strings.Join(notes, "; "))
		rec.SetMeta("collect", "started full, switched to essential: "+reason+" — "+strings.Join(append(notes, "1 天窗口", "跳过其余 full 层级的 collector"), "; "))
	})
	// replicated 表只读时没有 Keeper 会话：所有等待会话的查询都会以这个
	// 原因被跳过。
	keeperDownReason := ""
	if decision.KeeperDown {
		keeperDownReason = fmt.Sprintf("负载探测发现，在 %d 个 replicated 表处于只读状态时没有 Keeper 会话", decision.Signals.ReadonlyReplicas)
		collectLine += " — 跳过 Keeper 查询（没有 Keeper 会话）"
	}

	// 大型 SharedMergeTree 服务器上的 detached part：列出每个非共享表，
	// 以及 broken-part 日志行点名的共享表，而不是每个表的目录
	//（query.DetachedScope）。-collect full 会列出全部。
	detachedScope, detachedScopeP := "", ""
	if decision.Large && serverIsSMT && collectRequested != collectmode.Full && !dryRun {
		raw, _ := client.ExecuteQueryReal(query.SignalTablesQuery(effectiveMode))
		signal := query.ParseSignalTables(raw)
		detachedScope = query.DetachedScope(true, signal, "")
		detachedScopeP = query.DetachedScope(true, signal, "p.")
		note := fmt.Sprintf("为非共享表以及 broken-part 日志行中提到的 %d 个共享表列出 detached part（SharedMergeTree 服务器上共有 %d 个表；-collect full 会列出每个表）", len(signal), decision.Signals.Tables)
		collectLine += " — " + note
		fmt.Println("Note: " + note)
	}
	fmt.Printf("采集情况：%s\n", collectLine)
	rec.SetMeta("collect", collectLine)
	if b := watch.Budget(); b > 0 {
		rec.SetMeta("budget", fmt.Sprintf("%v for the collectors (-time-budget)", b))
	}
	rec.SetMeta("timeout", fmt.Sprintf("%v per query (-query-timeout; 0 = unbounded)", client.QueryTimeout()))
	rec.SetMeta("format", outputFormat.Name)
	if dryRun {
		rec.SetMeta("dry-run", "yes — no query was sent to the server, durations are not meaningful")
	}

	// gov 模式下不提供查询分析：其结果内嵌原始查询文本、异常消息、标识符
	// 和完整 DDL（query_details、failed_queries、tables_for_query、text_log
	// 切片），gov 模式哈希无法覆盖——归档会泄露 gov 本要保护的名称。拒绝
	// 这些标志，而不是静默生成未哈希的 bundle。在 dry-run 横幅之前检查，
	// 使不支持的组合立即失败。
	if mode == "gov" && (queryID != "" || normalizedHash != "") {
		fmt.Println("错误：gov 模式不支持 --query-id / --normalized-query-hash " +
			"（查询分析输出包含无法哈希的原始查询文本和标识符）")
		return
	}

	// text_log 同理：其消息是自由文本，携带原始 SQL、标识符和文件路径。
	// 哈希日志行会毁掉它唯一有用的东西，因此 gov 拒绝，而不是未哈希地发送。
	if mode == "gov" && collectTextLog {
		fmt.Println("错误：gov 模式不支持 --collect-text-log " +
			"（日志消息内嵌了无法哈希的原始查询和标识符）")
		return
	}

	// 对必需的时间窗口快速失败，而不是等到采集运行之后。
	if collectTextLog && (fromStr == "" || toStr == "") {
		fmt.Println("错误：--collect-text-log 需要同时提供 --from 和 --to " +
			"（system.text_log 数据量很大；无界导出会非常庞大，" +
			"而且本身就会给服务器增加负载）")
		return
	}

	// 说明本地 collector 为何关闭，使它在 bundle 中的缺席读起来是有意选择，
	// 而不是静默失败。resolveLocalCollector 已作出决定；这里只负责报告——
	// 按 collector 分别报告，因为可能一个关闭而另一个仍在运行。
	if !dryRun {
		var off []string
		if skipHostInfo {
			off = append(off, "host facts（主机信息）")
		}
		if skipLogs {
			off = append(off, "服务器日志文件")
		}
		if len(off) > 0 {
			what := strings.Join(off, " 或 ")
			switch mode {
			case "gov":
				fmt.Printf("gov 模式：不采集 %s "+
					"（主机名、挂载路径、进程命令行和日志正文无法哈希）。\n", what)
			case "cloud":
				fmt.Printf("cloud 模式：不采集 %s——它们描述的是本机器，"+
					"而不是托管服务。在自管理节点上运行时，可传入 --host-info=on / --logs=on 覆盖此行为。\n", what)
			}
		}
	}

	// dry-run：在版本探测之后激活，这样版本检测仍然可用，但要在其他一切
	// 之前。resolveAnalysisOpts 的预检查询使用 ExecuteQueryReal，绕过
	// dry-run 拦截——这样派生出的 query_id / hash 会流入打印的 SQL，而不是
	// 留下未绑定的 `{query_id}` 标记。
	if dryRun {
		fmt.Println("\n=== DRY RUN（试运行）===")
		fmt.Println("不会向磁盘写入任何数据。")
		fmt.Println("不会创建归档包。")
		fmt.Println("每个 SELECT 都会打印它触及的系统表，以及一个")
		fmt.Println("EXPLAIN ESTIMATE 块（只读元数据，不扫描任何数据 part）。")
		fmt.Println("空估算会显示为 `this table is empty`——这是 planner")
		fmt.Println("在确认没有任何数据匹配查询谓词。")
		fmt.Println()
		// --dry-run 下 EXPLAIN ESTIMATE 始终开启——一个标志，一种行为。
		// 预检查询（PreflightForX）使用 ExecuteQueryReal 绕过拦截，因此
		// 派生值会流入打印的 SQL。
		client.SetDryRun(os.Stdout, true)
		tmpDir, err := os.MkdirTemp("", "diag-dryrun-*")
		if err != nil {
			fmt.Printf("创建 dry-run 临时目录失败：%v\n", err)
			return
		}
		defer os.RemoveAll(tmpDir)
		outputDir = tmpDir
	}

	// 提前解析查询分析选项，使无效的 --query-id 或
	// --normalized-query-hash 在任何繁重工作之前快速失败。
	// 用 effectiveMode 而不是 mode：onprem → cloud 切换后，--query-id /
	// --normalized-query-hash 预检必须搜索分析 collector 将要读取的同一
	// 范围（每个 replica），否则可能拒绝一个存在于其他 replica 上的 id。
	analysisOpts, err := resolveAnalysisOpts(client, effectiveMode, queryID, normalizedHash, fromStr, toStr)
	if err != nil {
		fmt.Printf("错误：%v\n", err)
		return
	}

	// gov 模式要求客户提供 salt，使要发送给支持的产物中的哈希无法被公开的
	// 彩虹表反推。若未通过 --salt 传入，现在就提示输入（不回显）。salt
	// 永远不会离开运维人员的机器。
	//
	// dry-run 下跳过提示并使用占位符，这样打印的 `.sql` 文件仍能展示替换
	// 结果；不会写入任何内容，也不会构建归档，因此占位符不会泄露。
	if mode == "gov" && !dryRun {
		if govSalt == "" {
			s, err := promptForGovSalt()
			if err != nil {
				fmt.Printf("读取 gov 模式 salt 失败：%v\n", err)
				return
			}
			govSalt = s
		}
		if err := internal.ValidateGovSalt(govSalt); err != nil {
			fmt.Printf("gov 模式 salt 无效：%v\n", err)
			return
		}
	} else if mode == "gov" && dryRun && govSalt == "" {
		govSalt = "DRYRUNPLACEHOLDER"
	}

	if !collectFrom.IsZero() || !collectTo.IsZero() {
		fmt.Printf("采集窗口：%s\n", describeWindow(collectFrom, collectTo))
	}

	// 查找并执行查询——获取具体的文件夹路径
	queryManager := query.NewManager().WithOutputFormat(outputFormat).
		WithWindow(collectFrom, collectTo).WithMode(effectiveMode).WithRecorder(rec).WithWatchdog(watch).
		WithDetachedScope(detachedScope, detachedScopeP).WithKeeperDown(keeperDownReason)
	watch.Start()
	phaseStart := time.Now()
	finalOutputDir, err := queryManager.ExecuteQueries(client, queriesDir, serverVersion, outputDir, govSalt)
	rec.Phase("collectors", time.Since(phaseStart), "")
	if err != nil {
		fmt.Printf("执行查询失败：%v\n", err)
		return
	}

	// 限定时间的 system.text_log 切片（可选，需 --from/--to）。
	// 在 dry-run 摘要之前运行，使 --dry-run 能预览该 SQL。
	if collectTextLog {
		// collectFrom/collectTo 已与其他标志一起解析并校验，且
		// --collect-text-log 已坚持要求两者都设置。
		tlOpts := query.TextLogOpts{
			From:     collectFrom,
			To:       collectTo,
			Level:    textLogLevel,
			RowLimit: textLogLimit,
		}
		tlColl := query.NewTextLogCollector(client, effectiveMode).WithOutputFormat(outputFormat)
		phaseStart = time.Now()
		path, err := tlColl.Collect(tlOpts, finalOutputDir, serverVersion)
		tlStatus, tlErr := "ok", ""
		if err != nil {
			fmt.Printf("警告：text_log 采集失败：%v\n", err)
			tlStatus, tlErr = "failed", err.Error()
		} else if path != "" {
			fmt.Printf("  text_log 已写入 %s\n", path)
		}
		rec.Record(runlog.Entry{Stage: "text_log", Name: "--collect-text-log slice", Status: tlStatus, Duration: time.Since(phaseStart), Rows: -1, Extra: filepath.Base(path), Error: tlErr})
		rec.Phase("text_log slice", time.Since(phaseStart), "")
	}

	// 查询分析 bundle（仅在设置了 --query-id 或 --normalized-query-hash 时）。
	// 以聚焦参数运行 analysisDir 下的 .sql 文件，并把结果写入
	// <finalOutputDir>/query_analysis/。在 dry-run 摘要之前运行，使
	// --dry-run 也能预览分析 SQL（Collect 的 IsDryRun 处理会打印每个文件而
	// 不写入结果）——否则“列出工具将执行的每个查询”会漏掉这个 bundle。
	if analysisOpts.Enabled() {
		coll := query.NewAnalysisCollector(client, effectiveMode).WithOutputFormat(outputFormat)
		phaseStart = time.Now()
		written, skipped, err := coll.Collect(analysisOpts, analysisDir, finalOutputDir, serverVersion)
		qaStatus, qaErr := "ok", ""
		if err != nil {
			fmt.Printf("警告：查询分析失败：%v\n", err)
			qaStatus, qaErr = "failed", err.Error()
		}
		rec.Record(runlog.Entry{Stage: "analysis", Name: "query_analysis/ (--query-id / --normalized-query-hash)", Status: qaStatus,
			Duration: time.Since(phaseStart), Rows: -1, Extra: fmt.Sprintf("%d files written, %d skipped", written, skipped), Error: qaErr})
		rec.Phase("query analysis", time.Since(phaseStart), "")
	}

	// Keeper 自身的视角：Keeper 成员的 four-letter command 应答
	//（keeper.Commands）。这些是任何系统表都不携带的服务端事实——leader
	// 还是 follower、Raft 日志位置、快照大小与传输设置、未完成请求、Keeper
	// 自身延迟、版本——写入 keeper/<host>_<port>.txt。成员列表来自两个来源：
	// system.zookeeper_connection 指明每个已配置连接当前 ON 的成员（每连接
	// 一行，而非每个 ensemble 成员一行），服务器配置的 <zookeeper><node>
	// 条目则在工具运行于服务器上时指明整个 ensemble。两者取并集，因此本
	// 服务器未连接的 follower 也会被探测——那通常正是 Keeper 事故所涉及的
	// 成员。不可达的成员是文件和执行日志中的一种结果，绝不是运行失败。
	// --dry-run 下只列出成员而不联系任何对象；成员查询本身与版本探测一样是
	// 元数据预检读取，使用 ExecuteQueryReal 真实执行并打印，使 dry run 仍能
	// 展示每个到达服务器的 SELECT。
	if !skipKeeper {
		phaseStart = time.Now()
		if dryRun {
			fmt.Printf("\n-- pre-flight（已执行，仅元数据）：%s\n", keeper.TargetsQuery)
		}
		// 没有 Keeper 会话时，system.zookeeper_connection 会等到超时而不是
		// 应答；此时成员仅来自服务器配置——失败 ensemble 的判定本也来自那里。
		var raw string
		var qerr error
		if keeperDownReason == "" {
			raw, qerr = client.ExecuteQueryReal(keeper.TargetsQuery)
		} else {
			qerr = fmt.Errorf("未读取：%s", keeperDownReason)
		}
		connected := keeper.ParseTargets(raw)
		configured := keeper.TargetsFromConfig(configDir)
		targets := keeper.MergeTargets(connected, configured)
		switch {
		case qerr != nil && len(targets) == 0:
			// system.zookeeper_connection 自 23.8 起存在；没有 <zookeeper>
			// 块的服务器得到空表，而不是错误。
			fmt.Printf("Keeper 信息：已跳过——无法读取 system.zookeeper_connection（%s）\n", firstLine(qerr.Error()))
			rec.Record(runlog.Entry{Stage: "keeper", Name: "system.zookeeper_connection", Status: "skipped",
				Duration: time.Since(phaseStart), Rows: -1, Extra: "-keeper-mntr: no member list", Error: qerr.Error()})
		case len(targets) == 0:
			fmt.Println("Keeper 信息：已跳过——system.zookeeper_connection 或服务器配置中都没有 Keeper 成员（未配置 <zookeeper>）")
			rec.Record(runlog.Entry{Stage: "keeper", Name: "system.zookeeper_connection", Status: "skipped",
				Duration: time.Since(phaseStart), Rows: -1, Extra: "-keeper-mntr: no Keeper member configured"})
		case dryRun:
			fmt.Printf("\nKeeper 信息（dry run）：将通过 TCP 向 %d 个成员发送 %s，并写入 %s/<host>_<port>.txt——现在不联系任何成员：\n",
				len(targets), strings.Join(keeper.Commands, ", "), keeper.DirName)
			for _, t := range targets {
				fmt.Printf("  %s:%s\n", keeper.Label(t.Host, govSalt), t.Port)
			}
		default:
			src := fmt.Sprintf("%d 个已连接", len(connected))
			if extra := len(targets) - len(connected); extra > 0 {
				src += fmt.Sprintf(" + %d 个来自服务器配置", extra)
			}
			fmt.Printf("正在从 %d 个成员采集 Keeper 信息（%s；%s）...\n", len(targets), src, strings.Join(keeper.Commands, " / "))
			outcomes, kerr := keeper.Collect(finalOutputDir, targets, keeper.Options{GovSalt: govSalt})
			if kerr != nil {
				fmt.Printf("警告：Keeper 信息无法写入：%v\n", kerr)
			}
			fmt.Print(keeper.Summary(outcomes))
			for _, o := range outcomes {
				extra := ""
				if o.File != "" {
					extra = keeper.DirName + "/" + filepath.Base(o.File)
				}
				rec.Record(runlog.Entry{Stage: "keeper", Name: o.Label + ":" + o.Port, Status: o.Status,
					Duration: o.Duration, Bytes: o.Bytes, Rows: -1, Extra: extra, Error: o.Err})
			}
			rec.Phase("keeper facts", time.Since(phaseStart), "")
		}
	}

	if dryRun {
		fmt.Println("\n=== DRY RUN 摘要 ===")
		fmt.Println("以上是将要执行的查询。")
		fmt.Println("要真正运行，请去掉 --dry-run 后重新调用。")
		return
	}
	// gov 模式：打印并保存本地映射，从真实数据库/表名映射到出现在要发送给
	// 支持的输出文件中的 hex(SHA256(name+salt)) 形式。保存在归档文件夹之外。
	// dry-run 下跳过——该映射是本地产物，生成它没有意义（函数运行时 SQL
	// 本身仍会通过拦截客户端打印）。
	if mode == "gov" && !dryRun {
		if err := internal.PrintGovNameMapping(client, outputDir, finalOutputDir, govSalt); err != nil {
			fmt.Printf("警告：gov 模式名称映射失败：%v\n", err)
		}
	}

	// host facts：解释 ClickHouse 系统表所报告内容的 OS/内核/硬件上下文。
	// 从 /proc、/sys 和 /etc 读取——因此只有在服务器上运行时才有内容，否则
	// 会降级为 "unavailable" 并附一条说明，而不是让运行失败。保留到该代码块
	// 之外，使 dashboard 能渲染与写入 host_info.json 相同的事实；host-info
	// 被跳过时为 nil。
	var hostReport *hostinfo.Report
	if !skipHostInfo {
		fmt.Println("正在采集主机 OS 和硬件信息...")
		phaseStart = time.Now()
		report := hostinfo.Collect()
		rec.Phase("host facts", time.Since(phaseStart), "")
		hostReport = report
		if path, err := hostinfo.WriteJSON(finalOutputDir, report); err != nil {
			fmt.Printf("警告：host info 无法写入：%v\n", err)
		} else {
			fmt.Print(report.Summary())
			for _, n := range report.Notes {
				fmt.Printf("  说明：%s\n", n)
			}
			fmt.Printf("  已写入 %s\n", filepath.Base(path))
		}
	}

	// 从磁盘采集 ClickHouse 服务器日志文件。未显式给出目录时从服务器配置中
	// 发现，因为运维人员移动日志位置远比移动其他东西频繁。
	if !skipLogs {
		fmt.Println("正在采集 ClickHouse 服务器日志文件...")
		phaseStart = time.Now()
		res, err := logfiles.Collect(finalOutputDir, logfiles.Options{
			Dir:             logsDir,
			ConfigDir:       configDir,
			IncludeArchives: logsArchives,
			MaxBytesPerFile: int64(logsMaxMB) << 20,
		})
		if err != nil {
			fmt.Printf("警告：日志采集失败：%v\n", err)
		} else {
			fmt.Print(res.Summary())
		}
		rec.Phase("log files", time.Since(phaseStart), "")
	}

	// 从磁盘采集 ClickHouse 配置文件。写入运行目录之下，使归档永远只包含
	// 本主机、本次运行的配置。
	if !skipConfig {
		configDest := filepath.Join(finalOutputDir, "configuration")
		phaseStart = time.Now()
		if err := config.NewCollector().Collect(configDir, configDest, false); err != nil {
			// 读取 /etc/clickhouse-server/config.d/ 通常需要 root 或
			// clickhouse 组，因此无特权运行会在这里失败。给出警告而不是让
			// 运行失败——查询结果仍然有价值——但绝不保持沉默。
			fmt.Printf("警告：配置采集失败：%v\n", err)
		}
		rec.Phase("configuration", time.Since(phaseStart), "")
	}

	// 若未跳过则评估 alert rule
	var alertResults []alert.Result
	if !skipAlerts {
		fmt.Println("正在评估 alert rule...")
		phaseStart = time.Now()
		essentialRun, _ := watch.Tripped()
		ev := alert.NewEvaluator(client, effectiveMode).WithEssential(essentialRun).
			WithDetachedScope(detachedScope).WithKeeperDown(keeperDownReason).WithStop(watch.OverBudget)
		// 检查会读取 JSONEachRow 文件；使用 -output-format native 或 tsv 时，
		// 规则改为实时运行它们的查询。
		if outputFormat.Name == query.FormatJSONL.Name {
			ev = ev.WithBundle(bundle.Open(finalOutputDir, collectorStatus(rec)).WithWindowEnd(collectTo))
		}
		alertResults = ev.RunAll(alertsDir, serverVersion)
		rec.Phase("alerts", time.Since(phaseStart), "")
		for _, r := range alertResults {
			status := "clean"
			switch {
			case r.Skipped:
				status = "skipped"
			case r.Error != "":
				status = "failed"
			case len(r.Rows) > 0:
				status = "fired"
			}
			ruleName, ruleDir := r.File, ""
			if i := strings.LastIndex(r.File, "/"); i >= 0 {
				ruleDir, ruleName = r.File[:i], r.File[i+1:]
			}
			rec.Record(runlog.Entry{Stage: "alert", Name: ruleName, Source: ruleDir, Status: status, Duration: time.Duration(r.DurationMs) * time.Millisecond,
				Rows: -1, Extra: fmt.Sprintf("%d instance(s)%s", len(r.Rows), map[bool]string{true: "; " + r.Reason, false: ""}[r.Reason != ""]), Error: r.Error})
		}
		// “checked”只统计产出了答案的规则：skipped（此处没有该表）和
		// errored（查询失败）的规则都被排除，因为把任一者报告为已检查都会
		// 暗示发生过一次并不存在的验证。见 alert.Summarize。
		evaluated, fired, errored, skipped := alert.Summarize(alertResults)
		fmt.Printf("alert 评估完成：已检查 %d 条规则，%d 条触发，%d 条出错，%d 条不适用\n",
			evaluated, fired, errored, skipped)
		if errored > 0 {
			fmt.Printf("  （%d 条规则无法运行——请查看上方的 [alert] ERROR 行；"+
				"这些不是 finding）\n", errored)
		}
	}

	// 若未跳过则生成 HTML dashboard。
	//
	// gov 模式下不生成：dashboard.html 与哈希后的结果文件落在同一个归档中，
	// 但它的查询在 Go 中构建，会选择原始标识符（最多 2000 个表的数据库/表名、
	// 磁盘路径、用户，以及 last_exception、last_error_message 之类的服务器
	// 生成文本）——正是 queries.gov/*.sql 会哈希的那些值。发送它会破坏
	// gov 模式哈希，因此与查询分析一样被拒绝。对每个 dashboard 面板做哈希是
	// 恢复它的后续工作。
	dashboardWritten := false
	if generate, skipReason := dashboardDecision(skipDashboard, effectiveMode); !generate {
		fmt.Println(skipReason)
	} else {
		gen := dashboard.NewGenerator(client, effectiveMode).
			WithCollection(rec.Meta("node"), rec.Meta("uptime"), rec.Meta("collection")).
			WithServerVersion(serverVersion).
			WithAnalysis(analysisOpts, analysisDir).
			WithHostInfo(hostReport).
			WithAirgap(*airgapFlag).
			WithCollectMode(collectKind(watch), rec.Meta("collect"), skippedCollectors(rec)).
			WithDetachedScope(detachedScope).
			WithKeeperDown(keeperDownReason != "").
			WithStop(watch.OverBudget)
		// 面板读取已采集的 JSONEachRow 文件；使用 -output-format native 或
		// tsv 时则像以前一样查询服务器。
		if outputFormat.Name == query.FormatJSONL.Name {
			gen = gen.WithBundle(bundle.Open(finalOutputDir, collectorStatus(rec)).WithWindowEnd(collectTo)).
				WithWindow(collectFrom, collectTo)
		}
		// 现在先写一次执行日志，使 dashboard 的 Collected Files 面板能为它
		// 建立索引；下面的最终写入会用 dashboard 阶段本身来刷新它。
		if _, err := rec.Write(finalOutputDir); err != nil {
			fmt.Printf("警告：execution log 无法写入：%v\n", err)
		}
		phaseStart = time.Now()
		if err := gen.Generate(finalOutputDir, alertResults); err != nil {
			fmt.Printf("警告：dashboard 生成失败：%v\n", err)
			rec.Phase("dashboard", time.Since(phaseStart), "failed: "+err.Error())
		} else {
			dashboardWritten = true
			rec.Phase("dashboard", time.Since(phaseStart), "")
		}
	}

	// dashboard 是 alertResults 的唯一另一个消费者，因此只要它未生成——
	// gov 模式、--skip-dashboard 或生成失败——归档就完全不带 alert 记录：
	// 规则运行了，某条 critical 的规则可能触发了，而客户发回的产物却什么也
	// 没说。改为写入规则级元数据：name/title/severity/state 和实例数量，
	// 绝不包含匹配到的行（它们的列由规则定义，因此在 gov 模式下会携带原始
	// 标识符）。
	if !skipAlerts && !dashboardWritten && len(alertResults) > 0 {
		if err := alert.WriteSummaryJSON(finalOutputDir, alertResults, effectiveMode); err != nil {
			fmt.Printf("警告：alert 摘要无法写入：%v\n", err)
		} else {
			fmt.Println("已写入 alerts_summary.json（规则结果和实例计数；" +
				"绝不包含匹配到的行）。")
		}
	}

	// 执行日志——放在最后，使其覆盖上面每个阶段；随后归档会携带它。
	// 绝不致命：即使没有它，结果仍然有价值。
	if path, err := rec.Write(finalOutputDir); err != nil {
		fmt.Printf("警告：execution log 无法写入：%v\n", err)
	} else {
		fmt.Printf("execution log 已写入：%s（每个 collector 的结果和耗时）\n", path)
	}

	// 若未跳过则创建归档包——使用实际创建的那个具体文件夹
	if !skipArchive {
		if err := createArchiveWithTimestamp(finalOutputDir); err != nil {
			// 非零退出，使脚本或 cluster 命令不会把一次没有 bundle 的运行
			// 当作成功。不会跳过任何 defer：main 中唯一的 defer 属于 dry-run，
			// 它永远不会走到归档。
			fmt.Printf("创建归档包失败：%v\n", err)
			os.Exit(1)
		}
	}
}

// passwordEnvVar 在未给出 -password 时、提示之前读取——这是
// clickhouse-client 使用的名称。cluster 命令依赖它：每个按主机的运行都
// 继承该变量，因此不会把密码放到命令行上，以免被 ps 看到。
const passwordEnvVar = clustercollect.PasswordEnvVar

// resolvePassword 在给出 -password 时返回其值，否则在环境变量已设置时
// 返回它——即使是空字符串，那也是一个显式的空密码。found=false 表示：
// 提示输入。以 "" 给出的 -password 会提示，与读取该变量之前一样，
// 而不是让另一个客户端导出的密码顶替它。
func resolvePassword(flagValue string, flagSet bool, lookup func(string) (string, bool)) (string, bool) {
	if flagSet {
		return flagValue, flagValue != ""
	}
	return lookup(passwordEnvVar)
}

// modeExplicit 报告 -mode 是否真的被传入。该标志默认是 onprem，因此仅凭
// 值无法区分“未设置”与“有意选择 onprem”，而二者必须有不同的行为。
func getUserInput(protocol, host, port, username, password, mode, configDir *string, skipConfig, modeExplicit, passwordExplicit bool) error {
	reader := bufio.NewReader(os.Stdin)

	// 未提供时获取协议
	if *protocol == "" {
		fmt.Print("选择协议（http/https）[默认：http]：")
		input, _ := reader.ReadString('\n')
		*protocol = strings.TrimSpace(input)
		if *protocol == "" {
			*protocol = "http"
		}
	}
	// 校验协议值
	*protocol = strings.ToLower(*protocol)
	if *protocol != "http" && *protocol != "https" {
		fmt.Printf("协议 '%s' 无效。改用 http。\n", *protocol)
		*protocol = "http"
	}

	// 未提供时获取主机
	if *host == "" {
		fmt.Print("输入 ClickHouse 主机 [默认：localhost]：")
		input, _ := reader.ReadString('\n')
		*host = strings.TrimSpace(input)
		if *host == "" {
			*host = "localhost"
		}
	}

	// 未提供时获取端口
	if *port == "" {
		defaultPort := "8123"
		if *protocol == "https" {
			defaultPort = "8443"
		}
		fmt.Printf("输入 ClickHouse 端口 [默认：%s]：", defaultPort)
		input, _ := reader.ReadString('\n')
		*port = strings.TrimSpace(input)
		if *port == "" {
			*port = defaultPort
		}
	}

	// 未提供时获取用户名
	if *username == "" {
		fmt.Print("输入用户名：")
		input, _ := reader.ReadString('\n')
		*username = strings.TrimSpace(input)
	}

	// 未提供时获取密码：先 -password，再 CLICKHOUSE_PASSWORD，
	// 最后提示输入。
	if *password == "" && *username != "" {
		if p, found := resolvePassword(*password, passwordExplicit, os.LookupEnv); found {
			*password = p
			fmt.Printf("使用来自 %s 的密码\n", passwordEnvVar)
		} else {
			fmt.Print("输入密码：")
			passwordBytes, _ := term.ReadPassword(int(syscall.Stdin))
			*password = string(passwordBytes)
			fmt.Println() // 密码输入后换行
		}
	}

	// 解析模式。无法解析的模式是错误，绝不静默降级到 onprem——方向为何
	// 重要见 resolveMode。
	resolved, notice, err := resolveMode(*mode, modeExplicit,
		term.IsTerminal(int(syscall.Stdin)),
		func() (string, error) {
			fmt.Print("选择查询模式（cloud/onprem/gov）[默认：onprem]：")
			return reader.ReadString('\n')
		})
	if err != nil {
		return err
	}
	*mode = resolved
	if notice != "" {
		fmt.Println(notice)
	}

	// 显示可用模式供用户参考
	fmt.Printf("可用模式：%s\n", strings.Join(canonicalModes, "、"))

	// 未提供且未跳过配置采集时获取配置目录。gov 绝不采集配置，cloud 在本机
	// 也没有配置，因此这两种模式都不询问。
	if *configDir == "" && !skipConfig && !govWithholdsConfig(*mode) && !cloudSkipsConfig(*mode, *configDir) {
		fmt.Print("输入要采集的 ClickHouse 配置目录 [默认：/etc/clickhouse-server/config.d/]：")
		input, _ := reader.ReadString('\n')
		*configDir = strings.TrimSpace(input)
		if *configDir == "" {
			*configDir = "/etc/clickhouse-server/config.d/"
		}
	}

	return nil
}

// resolveAnalysisOpts 从原始 CLI 标志字符串构建查询分析选项。当设置了
// --query-id 而未设置 --normalized-query-hash 时，一次针对 system.query_log
// 的预检 SELECT 会派生哈希以及（找到时）查询的 event_time——随后默认时间
// 窗口就以该 event_time 为中心，而不是“从现在起最近 N 天”，这样一周前的
// 慢查询不会被以当前时间为基准的默认值漏掉。
func resolveAnalysisOpts(client *pkg.ClickHouseClient, mode, queryID, normalizedHash, fromStr, toStr string) (query.AnalysisOpts, error) {
	opts := query.AnalysisOpts{
		QueryID:             queryID,
		NormalizedQueryHash: normalizedHash,
	}
	if !opts.Enabled() {
		return opts, nil // 两者都未设置；分析保持关闭，跳过校验
	}

	from, err := query.ParseTimeFlag(fromStr)
	if err != nil {
		return opts, fmt.Errorf("--from: %w", err)
	}
	to, err := query.ParseTimeFlag(toStr)
	if err != nil {
		return opts, fmt.Errorf("--to: %w", err)
	}
	opts.From, opts.To = from, to

	// 预检：当只设置了 query_id 时，派生哈希和查询的 event_time。若用户
	// 已提供哈希，或未给出 query_id，则跳过。
	if opts.QueryID != "" && opts.NormalizedQueryHash == "" {
		hash, eventTime, err := query.PreflightForQueryID(client, mode, opts.QueryID)
		if err != nil {
			return opts, err
		}
		opts.NormalizedQueryHash = hash
		fmt.Printf("pre-flight：query_id %s → normalized_query_hash %s（event_time %s）\n",
			opts.QueryID, hash, eventTime.Format(time.RFC3339))
		// 若用户未固定 --from/--to，则把默认的 3 天窗口以派生出的
		// event_time 为中心，使那次慢执行一定落在分析窗口内。
		if opts.From.IsZero() && opts.To.IsZero() && !eventTime.IsZero() {
			opts.From = eventTime.Add(-36 * time.Hour)
			opts.To = eventTime.Add(12 * time.Hour)
		}
	}

	// Validate 为仍然为空的字段运行默认窗口逻辑。反向预检
	//（hash → 最慢 query_id）运行之前，需要一个已填充的窗口。
	if err := opts.Validate(time.Now().UTC()); err != nil {
		return opts, err
	}

	// 反向预检：当只设置了 --normalized-query-hash 时，选取最慢的一次已完成
	// 执行作为代表性 query_id，使那些按单个 id 查询的文件（ProfileEvents、
	// text_log、tables-referenced …）有可过滤的对象。没有它时，仅给哈希的
	// 模式会生成一个大多数卡片为空的 dashboard（用户的主要抱怨）。
	if opts.NormalizedQueryHash != "" && opts.QueryID == "" {
		qid, eventTime, err := query.PreflightForHash(client, mode, opts.NormalizedQueryHash, opts.From, opts.To)
		if err != nil {
			return opts, err
		}
		if qid == "" {
			fmt.Printf("pre-flight：在窗口内没有找到 normalized_query_hash %s 的 QueryFinish 行——将跳过 single-id 文件\n",
				opts.NormalizedQueryHash)
		} else {
			opts.QueryID = qid
			fmt.Printf("pre-flight：normalized_query_hash %s → 最慢的 query_id %s（event_time %s）\n",
				opts.NormalizedQueryHash, qid, eventTime.Format(time.RFC3339))
		}
	}
	return opts, nil
}

// promptForGovSalt 向运维人员询问 gov 模式 salt，而不在终端回显。salt 是
// 客户的本地机密——让它不出现在屏幕上，可避免在屏幕共享录像、工单附件或
// 终端回滚中被意外捕获。
func promptForGovSalt() (string, error) {
	fmt.Println()
	fmt.Println("gov 模式：请输入用于哈希输出中数据库 / 表名的 salt。")
	fmt.Println("  - 8–64 个字母数字字符（A–Z、a–z、0–9）")
	fmt.Println("  - 请将此值保留在本地；不要与 ClickHouse 支持团队共享。")
	fmt.Println("  - 如果希望哈希可比较，请在多次运行中使用相同的 salt。")
	fmt.Print("Salt：")
	saltBytes, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(saltBytes)), nil
}

// canonicalModes 是面向运维人员的列表，按打印顺序排列。
var canonicalModes = []string{"cloud", "onprem", "gov"}

// modeAliases 把我们接受的每种拼写映射到规范模式。
//
// gov 刻意没有别名。它是保护模式，而这张表的全部意义就在于：近似写法必须
// 被拒绝，而不是靠猜测解决——那里猜错就会发送未哈希的标识符。
var modeAliases = map[string]string{
	"cloud": "cloud", "ch-cloud": "cloud", "clickhouse-cloud": "cloud",

	"onprem": "onprem", "on-prem": "onprem", "on_prem": "onprem",
	"onpremise": "onprem", "on-premise": "onprem",
	"onpremises": "onprem", "on-premises": "onprem",
	"selfhosted": "onprem", "self-hosted": "onprem", "self_hosted": "onprem",

	"gov": "gov",
}

// canonicalMode 把一种拼写规范化为 cloud/onprem/gov，无法识别时返回 ""。
//
// TrimSpace 是关键，不是整洁：`-mode " gov"` 过去能挺过 ToLower，却通不过
// == "gov" 比较，于是落到 onprem。
func canonicalMode(raw string) string {
	return modeAliases[strings.ToLower(strings.TrimSpace(raw))]
}

// nearestMode 返回与 raw 编辑距离不超过 2 的规范模式；若没有足够接近、值得
// 建议的，则返回 ""。距离 2 能抓住现实中的笔误（"govv"、"clod"、"onrem"），
// 又不会为无关单词建议 "gov"。
func nearestMode(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	best, bestDist := "", 3
	for _, m := range canonicalModes {
		if d := editDistance(s, m); d < bestDist {
			best, bestDist = m, d
		}
	}
	return best
}

// editDistance 是 rune 上的 Levenshtein 距离，仅用于建议模式。
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min(min(cur[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(br)]
}

// invalidModeErr 点明有效模式，并在有接近者时给出建议。
func invalidModeErr(raw string) error {
	if s := nearestMode(raw); s != "" {
		return fmt.Errorf("模式 %q 无效——请选择 %s；你是指 %q 吗？",
			strings.TrimSpace(raw), strings.Join(canonicalModes, ", "), s)
	}
	return fmt.Errorf("模式 %q 无效——请选择 %s",
		strings.TrimSpace(raw), strings.Join(canonicalModes, ", "))
}

// resolveMode 把 -mode 标志转换为恰好 cloud/onprem/gov 之一，否则失败。
// 它是纯函数，因此下面的策略无需终端即可测试。
//
// 真正重要的规则是回退的“方向”。onprem 是三者中保护最弱的，因此在运维
// 人员意图无法解析时猜它，是唯一可能造成伤害的答案：`-mode govv` 过去只
// 打印一行，然后采集 onprem bundle——一次请求 gov 的运行却得到未哈希的
// 数据库和表名。当 stdin 关闭时，如同任何 CI 任务或脚本化采集，没有人会
// 读那一行。
//
// 因此显式提供的模式要么被理解，要么报错。默认值只在无歧义的地方保留：
// 完全没给 -mode。
func resolveMode(raw string, explicit, interactive bool, prompt func() (string, error)) (mode, notice string, err error) {
	if explicit {
		if m := canonicalMode(raw); m != "" {
			return m, "", nil
		}
		return "", "", invalidModeErr(raw)
	}
	// 没有请求任何模式，也没有人可以问。通告默认值，而不是静默采用它。
	if !interactive {
		return "onprem", "未提供 -mode，且 stdin 不是终端；使用 onprem。", nil
	}
	answer, perr := prompt()
	answer = strings.TrimSpace(answer)
	if perr != nil && answer == "" {
		return "", "", fmt.Errorf("读取模式失败：%w", perr)
	}
	// 在提示符处回车接受所提供的默认值；拼写错误则不行。
	if answer == "" {
		return "onprem", "", nil
	}
	if m := canonicalMode(answer); m != "" {
		return m, "", nil
	}
	return "", "", invalidModeErr(answer)
}

// getQueriesDir 把 mode 映射到对应的 queries 目录。
func getQueriesDir(mode string) (string, error) {
	switch canonicalMode(mode) {
	case "cloud":
		return "./queries.cloud", nil
	case "onprem":
		return "./queries.onprem", nil
	case "gov":
		return "./queries.gov", nil
	}
	// resolveMode 运行后不可达——这正是它不得猜测的原因。这里过去会回退到
	// ./queries.onprem，使其成为无法识别的模式悄悄变成未哈希采集的第二处。
	return "", fmt.Errorf("无法识别的模式 %q——无法选择 queries 目录", mode)
}

// createArchiveWithTimestamp 把运行目录归档为
// <output-dir>/<run-dir-name>.tar.gz，放在它所打包的目录旁边。它过去会把
// <run-dir-name>.tar.gz 写到当前目录：运行目录按运行启动的那一秒命名，因此
// 同一秒启动的两次运行即使 -output-dir 不同也会互相覆盖归档，而且丢失是
// 静默的。
func createArchiveWithTimestamp(specificDir string) error {
	archiveName := filepath.Clean(specificDir) + ".tar.gz"
	// 一个目录：configuration/ 现在与其他所有产物一起位于运行目录内，
	// 因此没有其他内容需要添加。
	return internal.CreateArchive(archiveName, specificDir)
}

// resolveLocalCollector 决定是否运行两个读取本地文件系统的 collector
// （host facts、服务器日志文件）之一，不应运行时返回 skip=true。
//
// 默认值随模式变化，因为这些 collector 描述的是工具所执行的机器：
//
//   - onprem——那台机器就是 ClickHouse 服务器，因此采集（on）。
//   - cloud——ClickHouse Cloud 是托管的、通过网络访问，因此本地 /proc 和
//     /var/log 描述的是运维人员的笔记本或堡垒机。采集它们会把看似确定实则
//     错误的数据放进支持归档，这比什么都不采集更糟（off）。
//   - gov——任何设置下都绝不：主机名、挂载路径、进程命令行和日志正文正是
//     gov 哈希要保护的内容，而它们一旦哈希就不再有用。
//
// "on" 仍会强制采集（例如 cloud 模式针对自管理节点），gov 除外，那里的
// 拒绝是绝对的。
func resolveLocalCollector(setting, mode, name string) (skip bool, err error) {
	// 这里和调用点都规范化模式。调用方本应传入 getUserInput 之后的值，
	// 但这个函数守卫着 gov 的隐私保证——它不能依赖每个未来的调用方都记得
	// 这一点（"GOV" 曾绕过 == "gov" 检查）。
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch strings.ToLower(strings.TrimSpace(setting)) {
	case "off":
		return true, nil
	case "on":
		if mode == "gov" {
			return true, fmt.Errorf("--%s=on 在 gov 模式下不允许 "+
				"（host facts 和日志正文包含无法哈希的标识符）", name)
		}
		if mode == "cloud" {
			fmt.Printf("警告：cloud 模式下 --%s=on 采集的是运行此工具的机器信息，"+
				"而不是托管的 ClickHouse 节点。\n", name)
		}
		return false, nil
	case "auto", "":
		return mode != "onprem", nil
	default:
		return true, fmt.Errorf("--%s 必须是 auto、on 或 off（实际为 %q）", name, setting)
	}
}

// describeWindow 为运维人员渲染解析后的 --from / --to 窗口，明确说明哪一端
// 回退到了查询自身的默认值，以免把只指定了一半的窗口误认为完整窗口。
func describeWindow(from, to time.Time) string {
	const layout = "2006-01-02 15:04:05 UTC"
	switch {
	case from.IsZero():
		return "每个查询自己的默认起点 → " + to.UTC().Format(layout)
	case to.IsZero():
		return from.UTC().Format(layout) + " → 当前时间"
	default:
		return from.UTC().Format(layout) + " → " + to.UTC().Format(layout)
	}
}

// govWithholdsConfig 报告该模式下是否必须暂缓配置采集。它单独存在并做模式
// 规范化，理由与 resolveLocalCollector 相同：它守卫着 gov 的隐私保证，而
// "-mode GOV" 曾让一个 collector 绕过裸的 == "gov" 比较。
func govWithholdsConfig(mode string) bool {
	return strings.ToLower(strings.TrimSpace(mode)) == "gov"
}

// cloudSkipsConfig 报告 cloud 模式是否应跳过配置采集：未给出 -config-dir 时
// 应跳过，因为工具并非运行在 ClickHouse 服务器上，默认路径只会产生一条
// “目录不存在”的提示。显式的 -config-dir 会被遵守，这与 cloud 模式下
// -host-info=on / -logs=on 给其他本地文件系统 collector 的主动启用相同。
// 它自身也做模式规范化，理由与 resolveLocalCollector 相同。
func cloudSkipsConfig(mode, configDir string) bool {
	return strings.ToLower(strings.TrimSpace(mode)) == "cloud" && strings.TrimSpace(configDir) == ""
}

// dashboardDecision 报告是否构建 dashboard.html，并在答案为否时说明原因。
// 从调用点抽出来纯粹是为了让 gov 拒绝可测试：这是一条披露规则，而内联的
// switch 只差一次粗心的重构就会静默发送 gov dashboard。
//
// gov 模式永远不会得到 dashboard。其面板在 Go 中构建，会选择原始标识符——
// 最多 2000 个表的数据库和表名、磁盘路径、用户，以及 last_exception、
// last_error_message 之类的服务器生成文本——正是 queries.gov/*.sql 哈希的
// 那些值。该文件与那些哈希后的结果落在同一个要发送给支持的归档中，因此
// 发送它会破坏哈希。对每个面板做哈希是恢复它的后续工作；在那之前答案是
// 否。
func dashboardDecision(skipDashboard bool, mode string) (generate bool, skipReason string) {
	// 先报告显式的 --skip-dashboard：要求跳过它的用户应当被告知其标志已
	// 被遵守，而不是 gov 策略暂缓了它。两种情况下结果相同。
	if skipDashboard {
		return false, "跳过 HTML dashboard（--skip-dashboard）。"
	}
	// 这里和调用点都做规范化，理由与 resolveLocalCollector 相同：这守卫着
	// 一条披露规则，不能依赖每个未来的调用方都已把模式转为小写
	//（"GOV" 曾在本文件其他地方绕过 == "gov" 检查）。
	if strings.ToLower(strings.TrimSpace(mode)) == "gov" {
		return false, "gov 模式下跳过 HTML dashboard：其面板选择无法哈希的原始标识符" +
			"（表名、磁盘路径、用户、异常文本），而 dashboard 属于要发送给支持的归档包。"
	}
	return true, ""
}

// 采集前可见性探测的结果
// （SELECT count() FROM system.databases WHERE name NOT IN (system, …)）。
type visibilityVerdict int

const (
	visibilityOK      visibilityVerdict = iota // 至少有一个用户数据库可见
	visibilityNone                             // 查询已执行，count 为 0：对 system.* 有 SELECT 但无 SHOW 授权
	visibilityDenied                           // 服务器以 Code: 497 / ACCESS_DENIED 拒绝了查询本身
	visibilityUnknown                          // 其他任何错误：网络、超时、无法解析该查询的服务器
)

// databaseVisibilityVerdict 对探测结果进行分类。只有显式的访问拒绝才是授权
// 问题；其他任何错误都意味着无法完成检查，那是另一条消息、另一套建议。
func databaseVisibilityVerdict(count string, err error) visibilityVerdict {
	if err != nil {
		if isAccessDenied(err) {
			return visibilityDenied
		}
		return visibilityUnknown
	}
	if strings.TrimSpace(count) == "0" {
		return visibilityNone
	}
	return visibilityOK
}

// isAccessDenied 在 HTTP 客户端的错误文本中识别 ClickHouse 的权限错误：
// "Code: 497. DB::Exception: …: Not enough privileges. … (ACCESS_DENIED)"。
func isAccessDenied(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Code: 497") || strings.Contains(msg, "ACCESS_DENIED")
}

// collectorStatus 按没有文件的 collector 说明原因：alert 检查会报告
// "not applicable: skipped (reason)"，而不是干净的结果。
func collectorStatus(rec *runlog.Recorder) map[string]bundle.Status {
	out := map[string]bundle.Status{}
	for _, e := range rec.Entries() {
		if e.Stage != "collector" || (e.Status != "skipped" && e.Status != "failed") {
			continue
		}
		// 本服务器没有的表（text_log 被禁用、crash_log 在从未崩溃过的
		// 服务器上）不是失败：应记为“未采集”，与受版本门控的 collector 一样。
		if e.Status == "failed" && strings.Contains(e.Error, "UNKNOWN_TABLE") {
			continue
		}
		reason := e.Extra
		if e.Status == "failed" && e.Error != "" {
			reason = e.Error
		}
		out[strings.TrimSuffix(e.Name, ".sql")] = bundle.Status{State: e.Status, Reason: reason}
	}
	return out
}
