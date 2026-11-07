package main

import (
	"fmt"
	"strconv"
	"strings"

	"clickhouse-diagnostic/pkg"
)

// nodeIdentity 是 bundle 对其所描述服务器的说明。一次升级处理中的两个
// bundle 被误读了节点——一个经由负载均衡器（系统表来自 replica A，日志文件来自
// replica B），另一个来自同级站点——两者都只能靠人工从线程 id 和 uptime 中
// 发现。身份探测把答案写进执行日志、dashboard 头部和 pre-pass 覆盖行。
type nodeIdentity struct {
	Host          string   // hostName()——系统表所描述的节点
	FQDN          string   // FQDN()
	UptimeSeconds int64    // uptime()——该节点（重新）启动至今的时长
	Samples       []string // 重复探测得到的 hostName()；值不同意味着存在负载均衡器
	Err           error    // 探测失败；身份未知，抑制警告
}

// probeNodeIdentity 询问服务器它是谁。特意为 hostName() 做了三次独立往返，
// 每次都使用一条新连接：负载均衡器会把 keep-alive 连接固定到某个后端，因此在
// 真实的三 replica 端点上，通过共享 client 进行的探测全部应答为同一个 replica，
// 警告从未触发。每次探测使用新连接是从客户端侧发现均衡器的唯一廉价方式。
func probeNodeIdentity(client *pkg.ClickHouseClient) nodeIdentity {
	var id nodeIdentity
	raw, err := client.ExecuteQueryFreshConnection("SELECT hostName(), FQDN(), toString(uptime()) FORMAT TSV")
	if err != nil {
		id.Err = err
		return id
	}
	f := strings.Split(strings.TrimSpace(raw), "\t")
	if len(f) >= 3 {
		id.Host, id.FQDN = f[0], f[1]
		id.UptimeSeconds, _ = strconv.ParseInt(f[2], 10, 64)
	}
	id.Samples = append(id.Samples, id.Host)
	for i := 0; i < 2; i++ {
		if s, err := client.ExecuteQueryFreshConnection("SELECT hostName() FORMAT TSV"); err == nil {
			id.Samples = append(id.Samples, strings.TrimSpace(s))
		}
	}
	return id
}

// loadBalancerSuspected 报告重复的 hostName() 探测是否应答了不止一个名称。
func loadBalancerSuspected(samples []string) bool {
	seen := map[string]bool{}
	for _, s := range samples {
		if s = strings.TrimSpace(s); s != "" {
			seen[s] = true
		}
	}
	return len(seen) > 1
}

// sameMachine 判断服务器报告的名称和 collector 自身的主机名是否可能指向
// 同一台机器。两个完全限定名必须完全相等——"ch-01.site-a" 和 "ch-01.site-b"
// 首标签相同，但是两台机器。首标签匹配只用于一侧为裸名的情况（服务器报告
// "ch-01" 而操作系统给出 "ch-01.example.internal"，或反之）。
func sameMachine(serverHost, serverFQDN, localHostname string) bool {
	norm := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	qualified := func(s string) bool { return strings.Contains(s, ".") }
	first := func(s string) string { return strings.SplitN(s, ".", 2)[0] }
	l := norm(localHostname)
	if l == "" {
		return true // 无法判断；不告警
	}
	// 当服务器的 FQDN 和本地名称都是完全限定名时，仅由 FQDN 决定——否则短的
	// hostName() 会在首标签上匹配，从而掩盖 FQDN() 已证明的差异。
	if f := norm(serverFQDN); qualified(f) && qualified(l) {
		return f == l
	}
	for _, s := range []string{serverHost, serverFQDN} {
		if s = norm(s); s != "" && (s == l || first(s) == first(l)) {
			return true
		}
	}
	return false
}

// isLoopback 报告 -host 标志是否指向本机。
func isLoopback(target string) bool {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "localhost", "127.0.0.1", "::1", "[::1]", "0.0.0.0":
		return true
	}
	return false
}

// nodeWarnings 把身份探测结果转换为运维人员可读的警告。每条都说明了对 bundle
// 的影响，而不仅仅陈述事实。
//
// mode 决定负载均衡端点该如何解读。在 cloud 采集中报告此事不成问题：
// ClickHouse Cloud 端点本来就在服务的各 replica 之间负载均衡，不存在可改指的
// 按 replica 端点，而且采集本身就是为此设计的——按 replica 划分的系统表通过
// clusterAllReplicas 分发，共享表按设计只读一次。若对每个 Cloud bundle 都报
// 警告，会训练读者忽略那条在自建集群上真正重要的信息，而在自建集群上同样的
// 症状意味着 bundle 静默地没有描述任何单一节点。因此 cloud 给 Note，其余情形
// 保留 Warning。
func nodeWarnings(id nodeIdentity, target, localHostname, mode string, collectingLocalFiles bool) []string {
	if id.Err != nil {
		return nil
	}
	var out []string
	if loadBalancerSuspected(id.Samples) {
		answered := strings.Join(uniqueStrings(id.Samples), " 和 ")
		if strings.EqualFold(strings.TrimSpace(mode), "cloud") {
			out = append(out, fmt.Sprintf("Note: -host %s 的响应来自 %s——该端点在服务的各个 replica 之间负载均衡，"+
				"这是正常的，也是 cloud 采集所预期的。按 replica 划分的系统表 "+
				"（query_log、part_log、metric_log、errors、text_log …）会分发到每个 replica；共享表 "+
				"（parts、tables、columns、databases、replicas、replication_queue、mutations、detached_parts）则来自 "+
				"实际应答的那个 replica，这与任何 cloud 模式采集一致。", target, answered))
		} else {
			out = append(out, fmt.Sprintf("Warning: -host %s 的响应来自 %s——它是负载均衡器或 DNS 轮询。"+
				"此 bundle 中的每张系统表都将来自应答各查询的那个 replica，因此 bundle "+
				"不会描述任何单一节点。请将 -host 指向一个 replica（或在它上面运行工具）。", target, answered))
		}
	}
	if collectingLocalFiles && !isLoopback(target) && !sameMachine(id.Host, id.FQDN, localHostname) {
		out = append(out, fmt.Sprintf("Warning: 系统表描述的是 %s，但 host facts、配置和日志文件 "+
			"是从本机（%s）读取的。请在 %s 上运行工具，或传入 -host-info off -logs off -skip-config "+
			"以免 bundle 混合两台主机。", id.Host, localHostname, id.Host))
	}
	return out
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// humanUptime 为执行日志和覆盖行渲染 uptime()。
func humanUptime(seconds int64) string {
	if seconds <= 0 {
		return "unknown"
	}
	d := seconds / 86400
	h := (seconds % 86400) / 3600
	m := (seconds % 3600) / 60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh", d, h)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

// isSharedMergeTree 读取 `SELECT value FROM system.settings
// WHERE name = 'cloud_mode'` 的原始值。自托管的 SharedMergeTree 栈以 cloud
// mode 运行；此时每个 replica 都保有自己的系统表。
func isSharedMergeTree(cloudModeValue string) bool {
	v := strings.ToLower(strings.TrimSpace(cloudModeValue))
	return v == "1" || v == "true"
}

// smtCollectionDecision 是在 -mode onprem 下发现 SharedMergeTree 集群时的结果。
type smtCollectionDecision struct {
	EffectiveMode string // 运行切换时为 "cloud"，否则为 "onprem"
	Note          string // 执行日志中 `collection:` 键的一行内容
	Message       string // 运维人员看到的内容
}

// decideSMTCollection：在 SharedMergeTree 集群上使用 -mode onprem 只会采集
// N 个节点中的一个，曾在一次 sev-1 中产生五个描述不了任何有用信息的 bundle。
// 因此运行会切换到 cloud 采集——每张系统表都通过 `default` 集群分发——同时仍然
// 采集本节点的 host facts、配置和日志文件，而仅用 cloud 模式做不到这一点。
// fanoutErr 是探测 clusterAllReplicas(default, system.one) 的结果；探测被拒绝
// （没有 REMOTE 授权、没有 `default` 集群）意味着无法切换，运行会停留在单节点
// 并说明这一点。singleNode 是 -single-node 标志。
func decideSMTCollection(fanoutErr error, singleNode bool, hint string) smtCollectionDecision {
	switch {
	case singleNode:
		return smtCollectionDecision{
			EffectiveMode: "onprem",
			Note:          "onprem，按请求（-single-node）在 SharedMergeTree 集群上只采集单节点——其他 replica 的系统表不在本 bundle 中",
			Message:       hint,
		}
	case fanoutErr != nil:
		return smtCollectionDecision{
			EffectiveMode: "onprem",
			Note: "onprem，单节点：检测到 SharedMergeTree（cloud_mode = 1），但集群 fan-out 探测失败——" +
				firstLine(fanoutErr.Error()),
			Message: "Warning: 此服务器运行 SharedMergeTree（cloud_mode = 1），但 clusterAllReplicas(default, …) 失败：" +
				firstLine(fanoutErr.Error()) + "\n    此 bundle 将只描述本节点。要覆盖每个 replica，请向诊断用户授予 " +
				"REMOTE 和 CREATE TEMPORARY TABLE ON *.* 后重新运行。",
		}
	default:
		return smtCollectionDecision{
			EffectiveMode: "cloud",
			Note: "onprem → cloud：检测到 SharedMergeTree（cloud_mode = 1）。每个按 replica 划分的系统表都会通过 " +
				"default 集群（clusterAllReplicas）分发；共享表（parts、tables、columns、databases、replicas、" +
				"replication_queue、mutations、detached_parts）只从一个 replica 读取，与任何 cloud 采集一致；" +
				"host facts、配置和日志文件来自 collector 所在的机器——当工具运行在本节点上时就是本节点的" +
				"（若不是，上方的 warnings: 行会说明）",
			Message: "检测到 SharedMergeTree 集群（cloud_mode = 1）：正在切换到 cloud 采集，使按 replica 划分的系统表 " +
				"（query_log、part_log、errors、metric_log、text_log …）覆盖每个 replica，而不仅仅是本节点；共享表 " +
				"（parts、tables、replicas …）只读取一次，与任何 cloud 采集一致。host facts、配置和日志文件仍从 " +
				"运行此工具的机器读取。传入 -single-node 可只采集本节点。",
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}
