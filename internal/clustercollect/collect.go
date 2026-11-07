// Package clustercollect 实现 `clickhouse-diagnostic cluster`：它向一个节点
// 询问集群有哪些主机（system.clusters），打印每台主机要执行的
// clickhouse-diagnostic 运行命令，并在确认后逐台主机执行——每台主机都是
// 同一个二进制的独立进程。采集本身没有改动：每次按主机运行都完全等同于
// 运维人员手工输入的命令。
package clustercollect

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"clickhouse-diagnostic/pkg"
)

// PasswordEnvVar 把密码传给每次按主机运行。cluster 命令不接受 -password
// 参数，因此密码从不出现在 ps 能看到的命令行上。
const PasswordEnvVar = "CLICKHOUSE_PASSWORD"

// portalCapBytes 是支持门户的上传上限；每个 bundle 单独上传。
const portalCapBytes = 200 << 20

// DiscoveryQuery 列出节点知道的每台主机。
const DiscoveryQuery = "SELECT cluster, shard_num, replica_num, host_name, host_address, " +
	"is_local, errors_count FROM system.clusters FORMAT JSONEachRow"

// executable、hostname 和 interfaceAddrs 是操作系统查询函数，在测试中可替换。
var (
	executable     = os.Executable
	hostname       = os.Hostname
	interfaceAddrs = net.InterfaceAddrs
)

// ownedFlags 由 cluster 命令在每次运行时设置，因此不能在 -- 之后透传：
// 每个都带有按主机的保护措施（-host 和 -output-dir 让各 bundle 相互隔离，
// -single-node 阻止 SharedMergeTree 在每次运行时扇出到整个集群，
// -host-info/-logs/-skip-config 避免把本机文件混入远程 bundle），
// 密码只通过环境变量传递，而 -skip-archive / -dry-run 会导致找不到归档。
var ownedFlags = map[string]bool{
	"password": true, "host": true, "port": true, "protocol": true, "user": true, "mode": true,
	"single-node": true, "output-dir": true, "host-info": true, "logs": true, "skip-config": true,
	"skip-archive": true, "dry-run": true,
}

// validateExtra 拒绝透传参数中指定命令自有 flag 的项，包括 Go 的各种写法（-f、--f、-f=v、--f=v）。
func validateExtra(extra []string) error {
	for _, a := range extra {
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name := strings.TrimLeft(a, "-")
		if i := strings.IndexByte(name, '='); i >= 0 {
			name = name[:i]
		}
		if !ownedFlags[name] {
			continue
		}
		if name == "password" {
			return fmt.Errorf("-- -password 不能透传：请改为设置 %s（cluster 命令从不把密码放在命令行上）", PasswordEnvVar)
		}
		return fmt.Errorf("-- -%s 不能透传：cluster 命令会为每台主机设置它", name)
	}
	return nil
}

// Row 是一行 system.clusters 记录。
type Row struct {
	Cluster     string `json:"cluster"`
	ShardNum    int    `json:"shard_num"`
	ReplicaNum  int    `json:"replica_num"`
	HostName    string `json:"host_name"`
	HostAddress string `json:"host_address"`
	IsLocal     int    `json:"is_local"`
	ErrorsCount int    `json:"errors_count"`
}

// Node 是一台要采集的机器。
type Node struct {
	Host, Address  string
	Shard, Replica int
	Local          bool // 发现集群时查询的节点
	Errors         int  // system.clusters 的 errors_count：曾难以连通该节点
	MultiShard     bool // 标签来自一个不止一个分片的集群
}

// ParseRows 读取 DiscoveryQuery 的 JSONEachRow 应答。
func ParseRows(body string) ([]Row, error) {
	var rows []Row
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r Row
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("system.clusters 中出现意外的数据行 %q：%w", line, err)
		}
		rows = append(rows, r)
	}
	return rows, nil
}

func isLoopback(address string) bool {
	if ip := net.ParseIP(address); ip != nil {
		return ip.IsLoopback()
	}
	return strings.EqualFold(address, "localhost")
}

// Discover 把 system.clusters 的行转换为每台机器一个 Node。
//
//   - 每条 is_local = 1 的行都是被询问的节点，无论它使用什么地址：默认配置的
//     `default` 集群会把它列为 127.0.0.1，同时真实集群中还会列出它的真实地址。
//   - 其他 loopback 行是指回该服务器的测试集群。
//   - 一台机器每属于一个集群就出现一次。最终只保留一次，标签取自分片最多、
//     承载拓扑的那个集群。名为 `default` 的集群从不按名称优先——
//     在默认安装中它就是这一台服务器。
//
// cluster 非空时只保留该集群；未知名称会报错并列出实际存在的名称。
func Discover(rows []Row, cluster string) ([]Node, error) {
	if cluster != "" {
		var kept []Row
		names := map[string]bool{}
		for _, r := range rows {
			names[r.Cluster] = true
			if r.Cluster == cluster {
				kept = append(kept, r)
			}
		}
		if len(kept) == 0 {
			return nil, fmt.Errorf("在 system.clusters 中找不到集群 %q；可用：%s", cluster, strings.Join(sortedKeys(names), ", "))
		}
		rows = kept
	}

	shards := map[string]map[int]bool{}
	for _, r := range rows {
		if shards[r.Cluster] == nil {
			shards[r.Cluster] = map[int]bool{}
		}
		shards[r.Cluster][r.ShardNum] = true
	}
	type ranked struct {
		Node
		shards     int
		notDefault bool
	}
	better := func(a, b ranked) bool { // a 优先于 b
		if a.shards != b.shards {
			return a.shards > b.shards
		}
		return a.notDefault && !b.notDefault
	}

	nodes := map[string]*ranked{}
	for _, r := range rows {
		local := r.IsLocal == 1
		if !local && isLoopback(r.HostAddress) {
			continue
		}
		key := r.HostAddress
		if local {
			key = "\x00local"
		}
		cand := ranked{
			Node: Node{Host: r.HostName, Address: r.HostAddress, Shard: r.ShardNum, Replica: r.ReplicaNum,
				Local: local, Errors: r.ErrorsCount, MultiShard: len(shards[r.Cluster]) > 1},
			shards: len(shards[r.Cluster]), notDefault: r.Cluster != "default",
		}
		cur, seen := nodes[key]
		if !seen {
			nodes[key] = &cand
			continue
		}
		errs := max(cur.Errors, cand.Errors)
		if better(cand, *cur) {
			*cur = cand
		}
		cur.Errors = errs
	}

	out := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Node)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Shard != out[j].Shard {
			return out[i].Shard < out[j].Shard
		}
		if out[i].Replica != out[j].Replica {
			return out[i].Replica < out[j].Replica
		}
		return out[i].Host < out[j].Host
	})
	return out, nil
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var unsafePath = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// Label 是一台主机的文件夹名：s<shard>r<replica>_<host>，可安全用作单个路径段。
func Label(n Node) string {
	host := unsafePath.ReplaceAllString(n.Host, "_")
	if host == "" {
		host = "host"
	}
	return fmt.Sprintf("s%dr%d_%s", n.Shard, n.Replica, host)
}

// Options 是每次按主机运行共享的设置。
type Options struct {
	Mode, Protocol, Host, Port, User string
	OutputDir                        string
	Extra                            []string // 传给每次运行
	LocalFiles                       bool     // 本机是发现节点
}

// HereArgs 是从本机为一台节点执行的 clickhouse-diagnostic 运行。
func HereArgs(n Node, o Options) []string {
	host := n.Host
	if n.Local {
		host = o.Host
	}
	args := []string{"-mode", o.Mode, "-protocol", o.Protocol, "-host", host, "-port", o.Port,
		"-user", o.User, "-single-node", "-output-dir", filepath.Join(o.OutputDir, Label(n))}
	if !(n.Local && o.LocalFiles) {
		// host facts、日志和配置都从运行二进制的机器磁盘读取：对任何其他节点而言，
		// 它们会以该节点的名字描述本机。
		args = append(args, "-host-info", "off", "-logs", "off", "-skip-config")
	}
	return append(args, o.Extra...)
}

// EachNodeArgs 是要在节点本机执行的 clickhouse-diagnostic 运行，它还会采集
// 该节点的日志文件和配置。它不携带密码：二进制会读取 CLICKHOUSE_PASSWORD
// 或在那里提示输入。
//
// 使用 https 时它按节点名称而不是 localhost 连接：服务器证书通常就是为
// 该名称签发的。
func EachNodeArgs(n Node, o Options) []string {
	host := "localhost"
	if o.Protocol == "https" {
		host = n.Host
		if n.Local {
			host = o.Host
		}
	}
	args := []string{"-mode", o.Mode, "-protocol", o.Protocol, "-host", host, "-port", o.Port,
		"-user", o.User, "-single-node", "-output-dir", "./clickhouse_results/" + Label(n)}
	return append(args, withoutSalt(o.Extra)...)
}

// withoutSalt 去掉 -salt 及其值：一条供人阅读、复制并在另一台主机上执行的
// 命令不应携带它。不带 -salt 运行时，二进制会在那里提示输入，且不回显。
func withoutSalt(extra []string) []string {
	var out []string
	for i := 0; i < len(extra); i++ {
		if name, attached := flagName(extra[i]); name == "salt" {
			if !attached {
				i++ // 它的值
			}
			continue
		}
		out = append(out, extra[i])
	}
	return out
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./:=@%+,-]+$`)

// flagName 返回 -f、--f、-f=v 或 --f=v 参数中的名称，以及值是否附着；不是 flag 的参数返回 ""。
func flagName(a string) (name string, attached bool) {
	if !strings.HasPrefix(a, "-") {
		return "", false
	}
	name = strings.TrimLeft(a, "-")
	if i := strings.IndexByte(name, '='); i >= 0 {
		return name[:i], true
	}
	return name, false
}

// secret 中的 flag 在任何打印命令的地方都会被打码。salt 和密码一样只留在本机：
// 二进制读取时不回显，因此不会进入终端回滚、录屏和工单附件。
var secret = map[string]bool{"password": true, "salt": true}

// Shown 把命令渲染成可打印形式，按 shell 规则加引号，并把任何写法的 -password 和 -salt 值打码。
func Shown(binary string, args []string) string {
	parts := []string{binary}
	for i, a := range args {
		if name, attached := flagName(a); secret[name] && attached {
			a = a[:strings.IndexByte(a, '=')+1] + "****"
		} else if i > 0 {
			if prev, attached := flagName(args[i-1]); secret[prev] && !attached {
				a = "****"
			}
		}
		if !shellSafe.MatchString(a) {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// bundlePrefixes 是一次运行可能留下的归档名：
// clickhouse_diagnostic_<ts>.tar.gz，以及改名之前版本留下的
// clickhouse_backup_<ts>.tar.gz。
var bundlePrefixes = []string{"clickhouse_diagnostic_", "clickhouse_backup_"}

func isBundleArchive(name string) bool {
	if !strings.HasSuffix(name, ".tar.gz") {
		return false
	}
	for _, p := range bundlePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// archivesIn 把 dir 中的每个 bundle 归档映射到其 mtime。
// 目录按字面列出而不是用 glob：-output-dir 可能包含模式字符（'support[123]'），
// filepath.Glob 会把它当作模式，从而什么也找不到。
func archivesIn(dir string) map[string]time.Time {
	seen := map[string]time.Time{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !isBundleArchive(name) {
			continue
		}
		if fi, err := e.Info(); err == nil {
			seen[filepath.Join(dir, name)] = fi.ModTime()
		}
	}
	return seen
}

// verifyArchive 把归档一直读到结尾：被截断或损坏的文件——磁盘写满、进程被杀——
// 会在这里失败，而不会以 ok 进入摘要。
func verifyArchive(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	entries := 0
	for {
		_, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			return err
		}
		entries++
	}
	if entries == 0 {
		return errors.New("归档为空")
	}
	return gz.Close()
}

// newArchive 是 dir 中不在 before 里、或 mtime 已变化的最新归档；本次运行没有产出时返回 ""。
func newArchive(dir string, before map[string]time.Time) string {
	newest := ""
	for path, mtime := range archivesIn(dir) {
		if old, existed := before[path]; existed && old.Equal(mtime) {
			continue
		}
		if newest == "" || path > newest {
			newest = path
		}
	}
	return newest
}

// isThisMachine 判断 host——运维人员指定的发现节点——是否就是运行命令的机器，
// 从而可以采集其文件。地址会与本机网络接口比较。名称遵循
// cmd/node_identity.go 的 sameMachine：两个带域名的名称必须完全相等
// （"ch-01.site-a" 和 "ch-01.site-b" 是两台机器）；只有一侧不带域名时，
// 才由第一个标签决定。
func isThisMachine(host string) bool {
	if isLoopback(host) {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		addrs, err := interfaceAddrs()
		if err != nil {
			return false
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
				return true
			}
		}
		return false
	}
	name, err := hostname()
	if err != nil {
		return false
	}
	h, n := strings.ToLower(strings.TrimSpace(host)), strings.ToLower(strings.TrimSpace(name))
	if h == n {
		return true
	}
	if strings.Contains(h, ".") && strings.Contains(n, ".") {
		return false
	}
	return strings.SplitN(h, ".", 2)[0] == strings.SplitN(n, ".", 2)[0]
}

func human(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

const usage = `用法：clickhouse-diagnostic cluster [flags] [-- 传给每次运行的参数]

在自管理集群的每台主机上运行 clickhouse-diagnostic。需要在环境中设置
%s（密码从不作为命令行参数传入）。

  -collect-from here       从本机执行全部采集（默认）。
                           其他主机：仅采集系统表，含 text_log 和
                           query_log；不采集服务器日志文件、配置或 host facts。
  -collect-from each-node  仅打印需要在每台主机上执行的命令，该命令
                           还会采集该主机的日志文件和配置。

选项：
`

type result struct {
	node    Node
	status  string
	elapsed time.Duration
	archive string
	size    int64
}

// Main 运行 cluster 命令并返回进程退出码：
// 0 表示所有主机都采集成功（或未运行任何采集），1 表示有主机失败，2 表示
// 用法或发现错误。
func Main(argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cluster", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, usage, PasswordEnvVar)
		fs.PrintDefaults()
	}
	host := fs.String("host", "localhost", "用于发现集群的节点")
	port := fs.String("port", "", "HTTP 端口，所有主机相同（默认 8123，使用 https 时为 8443）")
	protocol := fs.String("protocol", "http", "http 或 https")
	user := fs.String("user", "default", "ClickHouse 用户")
	cluster := fs.String("cluster", "", "仅使用 system.clusters 中的该集群（默认：列出的全部主机）")
	collectFrom := fs.String("collect-from", "here", "here | each-node")
	mode := fs.String("mode", "onprem", "clickhouse-diagnostic -mode：onprem | gov（gov 需要 -- -salt <salt>）")
	outputDir := fs.String("output-dir", "", "bundle 的输出位置（默认 ./cluster_bundles_<timestamp>）")
	timeout := fs.Duration("timeout", 30*time.Minute, "每台主机允许的耗时")
	yes := fs.Bool("yes", false, "运行前不请求确认")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if err := validateExtra(fs.Args()); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if *collectFrom != "here" && *collectFrom != "each-node" {
		fmt.Fprintf(stderr, "-collect-from 必须是 here 或 each-node，而不是 %q\n", *collectFrom)
		return 2
	}
	if *mode != "onprem" && *mode != "gov" {
		fmt.Fprintf(stderr, "-mode 必须是 onprem 或 gov（cloud 已在一次运行中采集每个副本）\n")
		return 2
	}
	if *protocol != "http" && *protocol != "https" {
		fmt.Fprintf(stderr, "-protocol 必须是 http 或 https\n")
		return 2
	}
	if *port == "" {
		*port = "8123"
		if *protocol == "https" {
			*port = "8443"
		}
	}
	password, ok := os.LookupEnv(PasswordEnvVar)
	if !ok {
		fmt.Fprintf(stderr, "请先设置密码：  export %s='…'\n"+
			"（cluster 命令从不把密码作为参数，因此在 ps 中不可见）\n", PasswordEnvVar)
		return 2
	}

	client := pkg.NewClickHouseClient(*protocol, *host, *port, *user, password)
	body, err := client.ExecuteQueryReal(DiscoveryQuery)
	if err != nil {
		fmt.Fprintf(stderr, "无法从 %s:%s 读取 system.clusters —— %v\n", *host, *port, err)
		return 2
	}
	rows, err := ParseRows(body)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	nodes, err := Discover(rows, *cluster)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(nodes) == 0 {
		fmt.Fprintln(stderr, "system.clusters 未列出任何主机。")
		return 2
	}

	clusters := map[string]bool{}
	for _, r := range rows {
		if *cluster == "" || r.Cluster == *cluster {
			clusters[r.Cluster] = true
		}
	}
	fmt.Fprintf(stdout, "发现 %d 台主机（来源 %s；集群：%s）\n\n", len(nodes), *host, strings.Join(sortedKeys(clusters), ", "))
	for _, n := range nodes {
		where := fmt.Sprintf("副本 %d", n.Replica)
		if n.MultiShard {
			where = fmt.Sprintf("分片 %d 副本 %d", n.Shard, n.Replica)
		}
		if n.Local {
			where += "（集群的发现节点）"
		}
		if n.Errors > 0 {
			where += fmt.Sprintf("  ! %d 次连接错误", n.Errors)
		}
		fmt.Fprintf(stdout, "  %-22s %-15s %s\n", n.Host, n.Address, where)
	}
	fmt.Fprintln(stdout)

	o := Options{Mode: *mode, Protocol: *protocol, Host: *host, Port: *port, User: *user, Extra: fs.Args()}
	if *collectFrom == "each-node" {
		fmt.Fprintf(stdout, "请在每台主机上运行以下命令（已设置 %s，或由它提示输入密码），\n"+
			"然后把 ./clickhouse_results/<folder>/ 中的 .tar.gz 归档发回：\n\n", PasswordEnvVar)
		if *mode == "gov" {
			fmt.Fprintln(stdout, "每次运行都会询问 salt：请在每台主机上输入相同的值，否则各 bundle\n"+
				"之间的哈希将无法对应。")
			fmt.Fprintln(stdout)
		}
		for _, n := range nodes {
			fmt.Fprintf(stdout, "# %s\n%s\n\n", n.Host, Shown("./clickhouse-diagnostic", EachNodeArgs(n, o)))
		}
		return 0
	}

	if _, err := os.Stat("queries." + *mode); err != nil {
		fmt.Fprintf(stderr, "当前目录中找不到 queries.%s/：请从解包后的 clickhouse-diagnostic 发行版中运行。\n", *mode)
		return 2
	}
	bin, err := executable()
	if err != nil {
		fmt.Fprintf(stderr, "无法定位当前二进制文件：%v\n", err)
		return 2
	}
	dir := *outputDir
	if dir == "" {
		dir = "cluster_bundles_" + time.Now().Format("20060102_150405")
	}
	if o.OutputDir, err = filepath.Abs(dir); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	o.LocalFiles = isThisMachine(*host)

	fmt.Fprintln(stdout, "将逐台主机运行：")
	fmt.Fprintln(stdout)
	for _, n := range nodes {
		fmt.Fprintf(stdout, "%s\n\n", Shown(bin, HereArgs(n, o)))
	}
	if *mode == "gov" {
		fmt.Fprintln(stdout, "gov 模式在每台主机上都只采集系统表：无论在哪里运行，都不采集日志文件、配置或\n"+
			"host facts。")
	} else {
		fmt.Fprintln(stdout, "其他主机：仅采集系统表（含 text_log、query_log）——不采集服务器日志\n"+
			"文件、配置或 host facts。这些内容请使用 -collect-from each-node 采集。")
	}
	fmt.Fprintln(stdout)
	if !*yes {
		fmt.Fprintf(stdout, "运行 %d 个采集任务？[y/N] ", len(nodes))
		answer, _ := bufio.NewReader(stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			fmt.Fprintln(stdout, "未运行任何采集。")
			return 0
		}
	}

	var results []result
	for _, n := range nodes {
		results = append(results, runOne(bin, n, o, *timeout, stdout))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%-22s %-11s %6s %9s  %s\n", "host", "status", "time", "size", "archive")
	failed := 0
	for _, r := range results {
		size, where, note := "-", "见 "+filepath.Join(Label(r.node), "run.log"), ""
		if r.archive != "" {
			size = human(r.size)
			where, _ = filepath.Rel(o.OutputDir, r.archive)
			if r.size > portalCapBytes {
				note = "  超过 200 MB 门户上传上限"
			}
		}
		if r.status != "ok" {
			failed++
		}
		fmt.Fprintf(&sb, "%-22s %-11s %5.0fs %9s  %s%s\n", r.node.Host, r.status, r.elapsed.Seconds(), size, where, note)
	}
	if err := os.WriteFile(filepath.Join(o.OutputDir, "summary.txt"), []byte(sb.String()), 0o644); err != nil {
		fmt.Fprintf(stderr, "警告：未能写入 summary.txt：%v\n", err)
	}
	fmt.Fprintf(stdout, "\n%s\nbundle 位于 %s\n", sb.String(), o.OutputDir)

	// gov 从不采集日志文件和配置，因此没有可补充的内容。
	if *mode != "gov" {
		fmt.Fprintln(stdout, "\n服务器日志文件和配置只能在主机本机读取。如需补充，请在每台主机上运行以下命令：")
		fmt.Fprintln(stdout)
		for _, n := range nodes {
			if !(n.Local && o.LocalFiles) {
				fmt.Fprintln(stdout, Shown("./clickhouse-diagnostic", EachNodeArgs(n, o)))
			}
		}
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// runOne 采集一个节点：本二进制的独立进程，输出写入 <folder>/run.log。
// 成功的标志是文件夹中出现归档——即使无法连接服务器或写入归档，二进制也会以 0 退出。
func runOne(bin string, n Node, o Options, timeout time.Duration, stdout io.Writer) result {
	folder := filepath.Join(o.OutputDir, Label(n))
	r := result{node: n, status: "ok"}
	start := time.Now()
	fmt.Fprintf(stdout, "→ %s ... ", n.Host)
	if err := os.MkdirAll(folder, 0o755); err != nil {
		r.status = "failed"
		fmt.Fprintf(stdout, "failed: %v\n", err)
		return r
	}
	// 仅所有者可读：gov 模式下运行会在哈希旁边打印真实的数据库和表名。
	// 还要 Chmod，以处理先前运行留下的日志，OpenFile 不会重新设置其权限。
	logPath := filepath.Join(folder, "run.log")
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err == nil {
		err = os.Chmod(logPath, 0o600)
	}
	if err != nil {
		if logFile != nil {
			logFile.Close()
		}
		r.status = "failed"
		fmt.Fprintf(stdout, "failed: %v\n", err)
		return r
	}
	// 复用的 -output-dir 仍保留着先前的归档；只有本次运行创建或替换的才作数，
	// 否则当前已下线的主机会把上一次运行的 bundle 报告为 ok。
	before := archivesIn(folder)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	cmd := exec.CommandContext(ctx, bin, HereArgs(n, o)...)
	cmd.Stdout, cmd.Stderr = logFile, logFile // 环境（含 CLICKHOUSE_PASSWORD）会被继承
	runErr := cmd.Run()
	cancel()
	logFile.Close()
	r.elapsed = time.Since(start)

	// ok 需要同时满足三点：运行以 0 退出、留下新归档、归档能读到结尾。
	switch r.archive = newArchive(folder, before); {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		r.status = "超时"
	case runErr != nil:
		r.status = "failed"
	case r.archive == "":
		r.status = "无归档"
	case verifyArchive(r.archive) != nil:
		r.status = "归档损坏"
	}
	if r.archive != "" {
		if fi, err := os.Stat(r.archive); err == nil {
			r.size = fi.Size()
		}
	}
	if r.status != "ok" {
		r.archive = "" // 不是可发送的 bundle
		fmt.Fprintf(stdout, "%s，耗时 %.0f 秒，见 %s\n", r.status, r.elapsed.Seconds(), filepath.Join(Label(n), "run.log"))
		return r
	}
	note := ""
	if r.size > portalCapBytes {
		note = "，超过 200 MB 门户上传上限"
	}
	fmt.Fprintf(stdout, "ok，耗时 %.0f 秒，%s%s\n", r.elapsed.Seconds(), human(r.size), note)
	return r
}
