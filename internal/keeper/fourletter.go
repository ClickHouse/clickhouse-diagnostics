// Package keeper 采集 ClickHouse Keeper（或 ZooKeeper）通过其四字命令
// 报告的自身信息——这是任何 system.* 表都不携带的服务器视角。
// system.zookeeper_connection 说明本服务器与哪个 Keeper 成员通信、
// 会话有多旧；metric_log 说明请求等待了多久。只有 Keeper 自己才能说明
// 它是否是 leader、有多少请求在排队、自身延迟和节点数、
// 以及运行哪个版本——这些正是判定 Keeper 延迟或
// 会话丢失问题时需要的事实。
//
// 发送七个命令，每个都在独立的 TCP 连接上，因为 Keeper
// 只回答一个四字命令然后关闭。前三个即使在
// 不提供服务的成员上也会应答（Keeper 仅对其他命令检查
// isServerActive()），因此在 quorum 丢失期间，它们是仍然能返回的：
//
//	ruok  存活——"imok"
//	isro  只读成员或 observer 返回 "ro"，否则返回 "rw"
//	lgif  成员的 Raft 日志：首/末索引和 term、已提交与
//	      leader 已提交、最后快照索引——末索引低于 leader 首索引的
//	      成员只能通过完整快照重新加入
//	srvr  版本、延迟 min/avg/max、收/发、连接数、
//	      未完成请求、zxid、模式（leader/follower/standalone）、
//	      节点数
//	mntr  同上，以机器可读的 zk_* 计数器呈现，外加 followers、
//	      synced followers、znode / watch / ephemeral 计数、内存、
//	      最新快照大小
//	conf  当前生效的协调设置：snapshot_distance、
//	      reserved_log_items、snapshot_transfer_chunk_size、
//	      write_snapshot_version……（gov 下移除存储路径）
//	dirs  快照和日志目录占用的磁盘字节数
//
// 所有这些命令都是只读的，且都不经过 Raft，也不随数据量增长：
// 在 26.7 上实测每个约 0.3 ms，每次回复不到 3 KB。
// 有副作用的命令（csnp、rqld、ydld、rcvr、srst、crst、rclc、
// clrs）绝不发送，stat、cons、dump、wchc 或 wchp 也不发送，
// 因为它们会列出 bundle 不应携带的客户端地址或会话。
//
// 各成员并行询问，成员在首次网络失败后不再被询问
// 任何命令：否则一个接受连接但从不回应的成员（在真实 quorum 丢失中
// 出现过）会让每条命令都等满读取超时。
//
// 目标是 system.zookeeper_connection 的 (host, port) 对——
// 即本服务器实际配置的成员——因此当服务器列出全部三个成员时，
// 三成员 ensemble 会为每个成员产出一个文件；只列出自身时
// 则产出一个文件。
package keeper

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"clickhouse-diagnostic/internal/config"
)

// DirName 是这些文件落入的 bundle 子目录。
const DirName = "keeper"

// Commands 是发送的命令及其顺序：ruok 在前，以便把
// 已启动但拒绝四字命令（four_letter_word_white_list）的成员
// 与已宕机的成员区分开；然后是两个即使成员不提供服务也
// 会应答的命令；最后是其余命令。
var Commands = []string{"ruok", "isro", "lgif", "srvr", "mntr", "conf", "dirs"}

// coreCommands 决定成员的状态。其他命令补充事实，但在较老的
// Keeper 上可能缺失（lgif 不在 22.8 中），或被自定义 allow-list
// 排除，这本身并不使成员变成 partial。
var coreCommands = map[string]bool{"ruok": true, "srvr": true, "mntr": true}

// notServingMsg 是 Keeper 在已启动但不提供服务的成员上对
// srvr、mntr、conf、dirs……的回复——没有 quorum，仍在加载快照。
// Apache ZooKeeper 说的是 "This ZooKeeper instance is not currently serving
// requests"；IsNotServing 两种都能识别。
const notServingMsg = "This instance is not currently serving requests"

var reNotServing = regexp.MustCompile(`^This (ZooKeeper )?instance is not currently serving requests$`)

// IsNotServing 报告某个四字命令回复是否为 ClickHouse Keeper
// 或 Apache ZooKeeper 的 not-serving 应答。
func IsNotServing(reply string) bool { return reNotServing.MatchString(strings.TrimSpace(reply)) }

// Target 是 system.zookeeper_connection 所命名的一个 Keeper 成员。
// Ensemble 是该连接的名称——<zookeeper> 块为 "default"，
// 否则为辅助 ZooKeeper 的名称——因此不同 ensemble 的成员
// 绝不会相互比较。
type Target struct {
	Host     string
	Port     string
	Ensemble string
}

// Options 控制一次采集。
type Options struct {
	// GovSalt 非空时，以与 SQL 采集器哈希标识符完全相同的方式
	// 哈希文件名和文件头部中的主机：
	// hex(SHA256(host || salt))，大写——这样该文件就能与同一 bundle 中
	// system.zookeeper_connection 里被哈希的主机关联。
	GovSalt     string
	DialTimeout time.Duration
	ReadTimeout time.Duration
	// Now 为文件头部打时间戳；nil = time.Now。
	Now func() time.Time
}

// Outcome 是某个目标发生的结果。
type Outcome struct {
	Target
	// Label 是写入 bundle 的主机表示（gov 下为哈希）。
	Label string
	// File 是写入的路径，什么都未能采集时为 ""。
	File string
	// Status：ok（ruok、srvr 和 mntr 都应答了）| not serving（已启动，但
	// 回答 "not currently serving requests"）| partial（部分应答）|
	// refused | timeout | failed（因其他原因什么都未应答）。
	Status   string
	Duration time.Duration
	Bytes    int64
	Err      string
}

// Resolve 将 -keeper-mntr flag 应用到模式：off 始终跳过；
// on 始终采集（cloud 会得到警告——托管服务的 Keeper
// 无法从外部访问，因此 on 只对以 cloud 模式采集的自建
// 集群有意义）；auto 在 onprem 和 gov 下采集，
// 因为工具运行在集群旁边，而在 cloud 下跳过。用户的
// -mode 是输入，不是实际采集模式：自建的
// SharedMergeTree 集群会把查询切到 cloud 集，但其
// Keeper 与任何其他本地 Keeper 一样可访问。
func Resolve(setting, mode string) (skip bool, warning string, err error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch strings.ToLower(strings.TrimSpace(setting)) {
	case "off":
		return true, "", nil
	case "on":
		if mode == "cloud" {
			return false, "Warning: -keeper-mntr=on in cloud mode — a managed service's Keeper is not " +
				"reachable from outside; this only yields files when the tool runs beside a " +
				"self-managed cluster.", nil
		}
		return false, "", nil
	case "auto", "":
		return mode == "cloud", "", nil
	default:
		return true, "", fmt.Errorf("-keeper-mntr 必须是 auto、on 或 off（实际为 %q）", setting)
	}
}

// TargetsQuery 列出每个已配置 Keeper 连接当前所在的成员；
// 使用 FORMAT TSV 以便 ParseTargets 读取。system.zookeeper_connection
// 每个连接一行（默认 <zookeeper> 块加上每个
// 辅助连接），每行命名该连接所 CONNECTED 到的成员——
// 而不是整个 ensemble。其余成员来自服务器
// 配置（TargetsFromConfig）；没有它时就只探测已连接的成员，
// 文档中已说明这一点。
const TargetsQuery = "SELECT DISTINCT name, host, toString(port) FROM system.zookeeper_connection WHERE host != '' ORDER BY name, host, port FORMAT TSV"

// DefaultPort 是节点省略端口时的 <zookeeper><node><port> 默认值。
const DefaultPort = "2181"

var (
	reZKBlock = regexp.MustCompile(`(?is)<zookeeper\b[^>]*>(.*?)</zookeeper>`)
	// <auxiliary_zookeepers> 存放其他 ensemble，每个名称一个标签——
	// 名称本身也可能是 "zookeeper"；在读取默认块之前移除。
	reAuxZK  = regexp.MustCompile(`(?is)<auxiliary_zookeepers\b[^>]*>.*?</auxiliary_zookeepers>`)
	reZKNode = regexp.MustCompile(`(?is)<node\b[^>]*>(.*?)</node>`)
	reZKHost = regexp.MustCompile(`(?is)<host>\s*([^<]*?)\s*</host>`)
	reZKPort = regexp.MustCompile(`(?is)<port>\s*([^<]*?)\s*</port>`)
)

// TargetsFromConfig 从服务器配置读取每个 <zookeeper><node>——
// 包括 configDir 以及当 configDir 是 *.d 目录时相邻的
// config.xml，与 logfiles.LogPathsFromConfig 扫描的相同两处。
// 这里是声明 ENSEMBLE 的地方；system.zookeeper_connection
// 只命名本服务器所在的成员，因此失败的 follower 永远不会
// 出现在那里。未解析的变量替换（{…}、from_env）和空主机
// 会被跳过；没有 <port> 的节点使用 DefaultPort。当目录不可读时
// 不返回任何内容——此时工具不在服务器上。
func TargetsFromConfig(configDir string) []Target {
	if configDir == "" {
		return nil
	}
	roots := []string{configDir}
	if base := filepath.Base(filepath.Clean(configDir)); strings.HasSuffix(base, ".d") {
		if parent := filepath.Dir(filepath.Clean(configDir)); parent != "." {
			roots = append(roots, parent)
		}
	}
	seen := map[Target]bool{}
	var out []Target
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := strings.ToLower(e.Name())
			if e.IsDir() || (!strings.HasSuffix(name, ".xml") && !strings.HasSuffix(name, ".conf")) {
				continue
			}
			blob, err := os.ReadFile(filepath.Join(root, e.Name()))
			if err != nil {
				continue
			}
			// 先处理注释：ClickHouse 自带的 config.xml 在注释里带有完整的
			// <zookeeper> 示例（example1 / example2 / example3），
			// 把它当作配置读取会让工具在单成员集群上报告四个
			// 成员，并尝试连接运维人员从未指定的三个主机。
			// ClickHouse 自己的解析器会忽略注释；本工具也必须如此。
			cfg := reAuxZK.ReplaceAllString(string(config.StripComments(blob)), "")
			for _, block := range reZKBlock.FindAllStringSubmatch(cfg, -1) {
				for _, node := range reZKNode.FindAllStringSubmatch(block[1], -1) {
					h := reZKHost.FindStringSubmatch(node[1])
					if h == nil || h[1] == "" || strings.Contains(h[1], "{") {
						continue
					}
					port := DefaultPort
					if pm := reZKPort.FindStringSubmatch(node[1]); pm != nil && pm[1] != "" && !strings.Contains(pm[1], "{") {
						port = pm[1]
					}
					// 这里只读取 <zookeeper> 块：默认 ensemble。
					t := Target{Host: h[1], Port: port, Ensemble: "default"}
					if !seen[t] {
						seen[t] = true
						out = append(out, t)
					}
				}
			}
		}
	}
	return out
}

// MergeTargets 将已连接的成员与已配置的 ensemble 合并，
// 已连接者在前，不产生重复。
func MergeTargets(connected, configured []Target) []Target {
	seen := map[Target]bool{}
	var out []Target
	for _, list := range [][]Target{connected, configured} {
		for _, t := range list {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out
}

// ParseTargets 将 TargetsQuery 的 TSV 转换为目标，
// 丢弃格式错误的行和重复项。
func ParseTargets(tsv string) []Target {
	var out []Target
	seen := map[Target]bool{}
	for _, line := range strings.Split(strings.TrimSpace(tsv), "\n") {
		cols := strings.Split(strings.TrimSpace(line), "\t")
		if len(cols) == 2 { // TargetsQuery 会选出 name，因此是 name、host、port
			cols = append([]string{"default"}, cols...)
		}
		if len(cols) != 3 || cols[1] == "" || cols[2] == "" {
			continue
		}
		if cols[0] == "" {
			cols[0] = "default"
		}
		t := Target{Host: cols[1], Port: cols[2], Ensemble: cols[0]}
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// FourLetter 发送一条命令并返回回复。Keeper 在应答后关闭
// 连接，因此回复会读到 EOF；readTimeout 约束
// 接受连接但从不回应的成员。被截止时间截短的回复会
// 与超时错误一并返回，以便调用方保留已到达的内容，
// 同时仍将该成员记为超时——卡住的 mntr 绝不能
// 被当作完整的 mntr。
func FourLetter(host, port, cmd string, dialTimeout, readTimeout time.Duration) (string, error) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), dialTimeout)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(readTimeout)); err != nil {
		return "", err
	}
	if _, err := io.WriteString(conn, cmd); err != nil {
		return "", err
	}
	var b strings.Builder
	r := bufio.NewReader(conn)
	if _, err := io.Copy(&b, r); err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			// 截止时间到：到达的内容都是部分内容。连同错误一并交回，
			// 以便文件中将其标记为已截断。
			return b.String(), err
		}
		// 回复之后紧跟 reset 仍然是回复。
		if b.Len() == 0 {
			return "", err
		}
	}
	return b.String(), nil
}

// reClientAddr 匹配 ip:port 或 [v6]:port 形式的 token——包括方括号形式
// 的 IPv4 映射地址（[::ffff:10.0.0.7]:60155）和带作用域的地址
// （[fe80::1%eth0]:2181）。srvr 和 mntr 目前不会打印客户端地址；
// 在 gov 下该过滤器保护文件免受未来某个会打印的 Keeper 影响，
// 因为哈希化的 bundle 绝不能携带这类地址。
var reClientAddr = regexp.MustCompile(`(\d{1,3}\.){3}\d{1,3}:\d+|\[[0-9a-fA-F:.%A-Za-z0-9_-]+\]:\d+`)

// govFilter 丢弃任何携带客户端地址的回复行。
func govFilter(reply string) string {
	lines := strings.Split(reply, "\n")
	out := lines[:0]
	for _, l := range lines {
		if reClientAddr.MatchString(l) {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// govConf 在 gov bundle 中只保留无法识别客户基础设施的 conf 值：
// 数字、布尔值和普通单词。无论键是什么，路径、URL、主机名和地址
// 都会被移除；即使值看起来普通，命名位置或客户自选对象的键
// 也会被移除——Keeper 的 *_path 和 *_disk、Apache ZooKeeper 的 dataDir、
// dataLogDir 以及 server.N（某个 ensemble 成员的主机和端口）。
// 与 gov 下的 system.disks 和 server_settings 采集器处理方式相同：
// 设置保留，其值显示 REMOVED。
var (
	reConfPlain = regexp.MustCompile(`^(-?[0-9]+(\.[0-9]+)?|true|false|[A-Za-z_][A-Za-z0-9_,*]*)$`)
	confIDKeys  = map[string]bool{"dataDir": true, "dataLogDir": true}
	reConfLabel = regexp.MustCompile(`^[A-Za-z_ ]*:?\s*$`)
)

func govConf(reply string) string {
	lines := strings.Split(reply, "\n")
	out := lines[:0]
	for _, l := range lines {
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			// ZooKeeper 会打印分区标签（"membership: "）；其他任何
			// 没有键的内容都可能是任何东西。
			if reConfLabel.MatchString(l) {
				out = append(out, l)
			}
			continue
		}
		k = strings.TrimSpace(k)
		if confIDKeys[k] || strings.HasSuffix(k, "_path") || strings.HasSuffix(k, "_disk") ||
			strings.HasPrefix(k, "server.") || !reConfPlain.MatchString(strings.TrimSpace(v)) {
			out = append(out, k+"=REMOVED")
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// Label 是主机在 bundle 中呈现的形式：主机本身，或在
// gov 下以与 SQL 采集器产出的相同形式给出的加盐哈希。
func Label(host, govSalt string) string {
	if govSalt == "" {
		return host
	}
	sum := sha256.Sum256([]byte(host + govSalt))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// fileName 生成对每种文件系统都安全的 "<label>_<port>.txt"：label
// 可能是主机名、IP（v6 含冒号）或哈希。
func fileName(label, port string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, label)
	return safe + "_" + port + ".txt"
}

// Collect 探测每个目标，并在 destDir/keeper/ 下为每个目标写入
// 一个文件。它绝不为不可达的成员返回错误——
// 那是一种结果，按目标记录——只在目标目录无法写入时
// 返回错误。
func Collect(destDir string, targets []Target, opts Options) ([]Outcome, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 3 * time.Second
	}
	if opts.ReadTimeout <= 0 {
		opts.ReadTimeout = 10 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	dir := filepath.Join(destDir, DirName)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("创建 %s 失败：%w", dir, err)
	}
	// 每个成员一个 goroutine，结果按目标顺序保存：总耗时是
	// 最慢成员的时间，而不是各成员时间之和。
	outcomes := make([]Outcome, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t Target) {
			defer wg.Done()
			outcomes[i] = collectOne(dir, t, opts)
		}(i, t)
	}
	wg.Wait()
	return outcomes, nil
}

// collectOne 向一个成员询问每条命令并写入其文件。
func collectOne(dir string, t Target, opts Options) Outcome {
	start := time.Now()
	o := Outcome{Target: t, Label: Label(t.Host, opts.GovSalt)}
	var b strings.Builder
	fmt.Fprintf(&b, "# clickhouse-diagnostic — Keeper 四字命令\n")
	fmt.Fprintf(&b, "# target: %s:%s\n", o.Label, t.Port)
	// 该成员属于哪个 ensemble；辅助 ZooKeeper 的名称是
	// 客户选择的配置，因此 gov 会像主机一样对其哈希。
	ens := t.Ensemble
	if ens == "" {
		ens = "default"
	}
	if ens != "default" {
		ens = Label(ens, opts.GovSalt)
	}
	fmt.Fprintf(&b, "# ensemble: %s\n", ens)
	fmt.Fprintf(&b, "# collected_at: %s\n", opts.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "# commands: %s（stat/cons 永远不会发送——它们会列出客户端地址）\n", strings.Join(Commands, ", "))
	coreOK, answered, refused, timedOut, notServing := 0, 0, 0, 0, 0
	var lastErr, stoppedBy string
	for _, cmd := range Commands {
		fmt.Fprintf(&b, "\n## %s\n", cmd)
		if stoppedBy != "" {
			// 一次网络失败意味着成员已宕机、不可达或
			// 卡死；继续询问其余命令只会等满同样的超时。
			fmt.Fprintf(&b, "skipped — %s\n", stoppedBy)
			continue
		}
		reply, err := FourLetter(t.Host, t.Port, cmd, opts.DialTimeout, opts.ReadTimeout)
		if err != nil {
			lastErr = err.Error()
			// 超时可能携带部分回复：保留它，但标记为
			// 已截断，并将该命令计为超时，绝不计为 ok。
			truncated := ""
			if reply != "" {
				reply = filtered(cmd, reply, opts.GovSalt)
				if reply = strings.TrimRight(reply, "\n"); reply != "" {
					fmt.Fprintf(&b, "%s\n", reply)
					truncated = " — 回复已截断"
				}
			}
			var ne net.Error
			switch {
			case errors.As(err, &ne) && ne.Timeout():
				timedOut++
				fmt.Fprintf(&b, "error: 在 %s 后超时%s\n", opts.ReadTimeout, truncated)
				stoppedBy = cmd + " 超时"
			case strings.Contains(err.Error(), "connection refused"):
				refused++
				fmt.Fprintf(&b, "error: 连接被拒绝\n")
				stoppedBy = "连接被拒绝"
			default:
				if opts.GovSalt != "" {
					fmt.Fprintf(&b, "error: （gov 模式下隐去消息）\n")
				} else {
					fmt.Fprintf(&b, "error: %s\n", err)
				}
				stoppedBy = cmd + " 失败"
			}
			continue
		}
		reply = strings.TrimRight(filtered(cmd, reply, opts.GovSalt), "\n")
		if reply == "" {
			// 不在 four_letter_word_white_list 中，或此 Keeper
			// 版本没有该命令（23.x 之前的 lgif）。
			fmt.Fprintf(&b, "(empty reply — 命令未在 four_letter_word_white_list 中启用，或此 Keeper 版本不认识它)\n")
			continue
		}
		fmt.Fprintf(&b, "%s\n", reply)
		if IsNotServing(reply) {
			notServing++
			continue
		}
		answered++
		if coreCommands[cmd] {
			coreOK++
		}
	}
	o.Duration = time.Since(start)
	switch {
	case notServing > 0:
		o.Status = "not serving"
	case coreOK == len(coreCommands):
		o.Status = "ok"
	case answered > 0:
		o.Status = "partial"
	case refused > 0:
		o.Status = "refused"
	case timedOut > 0:
		o.Status = "timeout"
	default:
		o.Status = "failed"
	}
	if o.Status != "ok" {
		o.Err = lastErr
		if o.Status == "not serving" {
			o.Err = notServingMsg
		}
		if opts.GovSalt != "" && o.Err != "" {
			o.Err = o.Status
		}
	}
	// 默认连接和辅助连接可能共享同一个 host:port（不同的
	// chroot）：辅助成员的文件名携带其 ensemble，因此
	// 两者绝不会写入同一文件。默认成员保留原始名称。
	name := fileName(o.Label, t.Port)
	if ens != "default" {
		name = fileName(ens+"__"+o.Label, t.Port)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(b.String()), 0600); err != nil {
		o.Status = "failed"
		o.Err = fmt.Sprintf("write %s: %v", filepath.Base(path), err)
	} else {
		o.File = path
		o.Bytes = int64(b.Len())
	}
	return o
}

// filtered 对一条回复应用 gov 过滤器：从每条回复中移除
// 客户端地址，从 conf 中移除路径值。
func filtered(cmd, reply, govSalt string) string {
	if govSalt == "" {
		return reply
	}
	reply = govFilter(reply)
	if cmd == "conf" {
		reply = govConf(reply)
	}
	return reply
}

// Summary 是每个结果对应的一行面向运维人员的文本。
func Summary(outcomes []Outcome) string {
	if len(outcomes) == 0 {
		return "  system.zookeeper_connection 中没有列出 Keeper 成员\n"
	}
	var b strings.Builder
	for _, o := range outcomes {
		line := fmt.Sprintf("  %s:%s — %s", o.Label, o.Port, o.Status)
		if o.File != "" {
			line += fmt.Sprintf(" (%s/%s)", DirName, filepath.Base(o.File))
		}
		if o.Err != "" && o.Status != "ok" {
			line += " — " + o.Err
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}
