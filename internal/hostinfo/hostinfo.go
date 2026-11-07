// Package hostinfo 采集运行 ClickHouse 的机器上的操作系统、内核和硬件信息——
// 这些上下文解释了服务器为何表现出 ClickHouse 系统表所描述的行为。
//
// 刻意保持零依赖：这里的一切都是对 /proc、/sys 或 /etc 的读取，
// 因此本工具仍是单个静态二进制，没有需要审计的供应链。
// 这也意味着 Linux 是一等目标（ClickHouse 服务器所在之处）；
// 在其他平台上，各节会降级为 Available:false 并附带原因，
// 而不是让整次运行失败。
//
// gov 模式下不采集——主机名、挂载路径和进程命令行
// 正是 gov 哈希旨在保护的标识符，而进程命令行
// 无法被有意义地哈希。
package hostinfo

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Report 是采集到的完整画面，写入 host_info.json。
type Report struct {
	CollectedAt string     `json:"collected_at"`
	OS          OSInfo     `json:"os"`
	CPU         CPUInfo    `json:"cpu"`
	Memory      MemoryInfo `json:"memory"`
	Disks       []DiskInfo `json:"disks"`
	Processes   []Process  `json:"top_processes_by_rss"`
	// Tunables 是那些最常解释 ClickHouse 异常行为、
	// 也是支持人员最先索要的主机设置。
	Tunables Tunables `json:"clickhouse_relevant_tunables"`
	// Notes 记录某一节为何为空，避免把缺失的节
	// 误认为健康状态（与告警统计遵循的
	// “不要把未运行的检查报告为通过”同一规则）。
	Notes []string `json:"notes,omitempty"`
}

type OSInfo struct {
	Available     bool   `json:"available"`
	Hostname      string `json:"hostname,omitempty"`
	Distro        string `json:"distro,omitempty"`
	DistroVersion string `json:"distro_version,omitempty"`
	KernelVersion string `json:"kernel_version,omitempty"`
	KernelFull    string `json:"kernel_full,omitempty"`
	Arch          string `json:"arch"`
	GoOS          string `json:"go_os"`
	UptimeSeconds int64  `json:"uptime_seconds,omitempty"`
}

type CPUInfo struct {
	Available   bool     `json:"available"`
	LogicalCPUs int      `json:"logical_cpus,omitempty"`
	ModelName   string   `json:"model_name,omitempty"`
	VendorID    string   `json:"vendor_id,omitempty"`
	MHz         string   `json:"mhz,omitempty"`
	LoadAvg     []string `json:"load_avg_1_5_15,omitempty"`
	// Flags 是 ClickHouse 在向量化执行中真正关心的标志。
	NotableFlags []string `json:"notable_flags,omitempty"`
}

type MemoryInfo struct {
	Available      bool  `json:"available"`
	TotalBytes     int64 `json:"total_bytes,omitempty"`
	FreeBytes      int64 `json:"free_bytes,omitempty"`
	AvailableBytes int64 `json:"available_bytes,omitempty"`
	BuffersBytes   int64 `json:"buffers_bytes,omitempty"`
	CachedBytes    int64 `json:"cached_bytes,omitempty"`
	SwapTotalBytes int64 `json:"swap_total_bytes,omitempty"`
	SwapFreeBytes  int64 `json:"swap_free_bytes,omitempty"`
}

type DiskInfo struct {
	Device     string  `json:"device"`
	MountPoint string  `json:"mount_point"`
	FSType     string  `json:"fs_type"`
	TotalBytes uint64  `json:"total_bytes"`
	FreeBytes  uint64  `json:"free_bytes"`
	UsedPct    float64 `json:"used_pct"`
}

type Process struct {
	PID      int    `json:"pid"`
	PPID     int    `json:"ppid"`
	State    string `json:"state"`
	RSSBytes int64  `json:"rss_bytes"`
	Threads  int    `json:"threads"`
	Command  string `json:"command"`
}

// Tunables 是切实影响 ClickHouse 的主机设置。每项都是
// 原始字符串，以便区分不可读的值与已设置的值。
type Tunables struct {
	TransparentHugepages string `json:"transparent_hugepages,omitempty"`
	THPDefrag            string `json:"transparent_hugepages_defrag,omitempty"`
	Swappiness           string `json:"vm_swappiness,omitempty"`
	OvercommitMemory     string `json:"vm_overcommit_memory,omitempty"`
	MaxMapCount          string `json:"vm_max_map_count,omitempty"`
	FsNrOpen             string `json:"fs_nr_open,omitempty"`
	FsFileMax            string `json:"fs_file_max,omitempty"`
	CgroupMemoryLimit    string `json:"cgroup_memory_limit_bytes,omitempty"`
	CgroupCPUMax         string `json:"cgroup_cpu_max,omitempty"`
	ClickHouseNofileSoft string `json:"clickhouse_open_files_soft,omitempty"`
	ClickHouseNofileHard string `json:"clickhouse_open_files_hard,omitempty"`
	// ClickHouseNprocSoft 是 /proc/<pid>/limits 中的 "Max processes"
	// 软限制——RLIMIT_NPROC，即操作系统对服务账号任务数（进程和线程）
	// 的上限。它不是 ClickHouse 的 max_threads 设置；
	// 旧名称/键曾声称它是。
	ClickHouseNprocSoft   string `json:"clickhouse_nproc_soft,omitempty"`
	ClickHouseProcessName string `json:"clickhouse_process,omitempty"`
}

const (
	procDir  = "/proc"
	maxProcs = 25 // 按 RSS 取前 N；完整进程表在 bundle 里是噪音
)

// Collect 尽其所能否收集一切，绝不让运行失败：无法读取的节
// 会被标记为不可用，原因记入 Notes。
func Collect() *Report {
	r := &Report{CollectedAt: time.Now().UTC().Format(time.RFC3339)}

	if _, err := os.Stat(procDir); err != nil {
		r.note("/proc is not present (non-Linux host?) — OS, CPU, memory, process and tunable sections are unavailable")
		r.OS = OSInfo{Arch: runtimeArch(), GoOS: runtimeOS()}
		if h, err := os.Hostname(); err == nil {
			r.OS.Hostname = h
		}
		r.Disks, _ = collectDisks() // statfs 可能仍然可用
		return r
	}

	r.OS = collectOS(r)
	r.CPU = collectCPU(r)
	r.Memory = collectMemory(r)

	disks, err := collectDisks()
	if err != nil {
		r.note("disk usage: " + err.Error())
	}
	r.Disks = disks

	procs, err := collectProcesses()
	if err != nil {
		r.note("processes: " + err.Error())
	}
	r.Processes = procs

	r.Tunables = collectTunables(procs)
	return r
}

func (r *Report) note(s string) { r.Notes = append(r.Notes, s) }

// WriteJSON 将报告以 host_info.json 写入 dir。
func WriteJSON(dir string, r *Report) (string, error) {
	blob, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "host_info.json")
	if err := os.WriteFile(path, blob, 0600); err != nil {
		return "", err
	}
	return path, nil
}

func collectOS(r *Report) OSInfo {
	o := OSInfo{Available: true, Arch: runtimeArch(), GoOS: runtimeOS()}
	if h, err := os.Hostname(); err == nil {
		o.Hostname = h
	}
	o.KernelVersion = strings.TrimSpace(readFile("/proc/sys/kernel/osrelease"))
	o.KernelFull = strings.TrimSpace(readFile("/proc/version"))

	// /etc/os-release 是可移植的发行版标识（systemd 标准）；
	// 对方的工具只报告通用 OS 字符串，无法说明
	// 你是在 RHEL 8 还是 Ubuntu 22.04 上——而这往往才是真正的答案。
	for _, p := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		kv := parseKeyValueFile(p, "=")
		if len(kv) == 0 {
			continue
		}
		o.Distro = strings.Trim(firstNonEmpty(kv["NAME"], kv["ID"]), `"`)
		o.DistroVersion = strings.Trim(firstNonEmpty(kv["VERSION_ID"], kv["VERSION"]), `"`)
		break
	}
	if o.Distro == "" {
		r.note("distro: no readable /etc/os-release")
	}
	if f := strings.Fields(readFile("/proc/uptime")); len(f) > 0 {
		if secs, err := strconv.ParseFloat(f[0], 64); err == nil {
			o.UptimeSeconds = int64(secs)
		}
	}
	return o
}

func collectCPU(r *Report) CPUInfo {
	c := CPUInfo{Available: true}
	// 值得报告的标志：它们决定 ClickHouse 是否走向量化代码路径，
	// 而为某个标志构建的二进制却运行在缺少该标志的主机上，是一个真实
	// （且令人困惑）的故障模式。两种架构都列出——在
	// aarch64 上内核将这一行标为 "Features"，且名称完全不同，
	// 因此仅含 x86 的列表在那里会静默地什么也不报告。
	notable := map[string]bool{
		// x86_64
		"sse4_2": true, "avx": true, "avx2": true, "avx512f": true,
		"avx512bw": true, "avx512vl": true, "bmi2": true, "popcnt": true,
		// aarch64
		"asimd": true, "neon": true, "sve": true, "sve2": true,
		"crc32": true, "atomics": true, "asimdrdm": true,
	}
	seen := map[string]bool{}

	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		r.note("cpu: " + err.Error())
		c.Available = false
		return c
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, ok := splitKV(sc.Text(), ":")
		if !ok {
			continue
		}
		switch key {
		case "processor":
			c.LogicalCPUs++
		case "model name":
			c.ModelName = val
		case "vendor_id":
			c.VendorID = val
		case "cpu MHz":
			c.MHz = val
		case "flags", "Features":
			for _, fl := range strings.Fields(val) {
				if notable[fl] && !seen[fl] {
					seen[fl] = true
					c.NotableFlags = append(c.NotableFlags, fl)
				}
			}
		}
	}
	sort.Strings(c.NotableFlags)
	if la := strings.Fields(readFile("/proc/loadavg")); len(la) >= 3 {
		c.LoadAvg = la[:3]
	}
	return c
}

func collectMemory(r *Report) MemoryInfo {
	m := MemoryInfo{Available: true}
	kv := parseKeyValueFile("/proc/meminfo", ":")
	if len(kv) == 0 {
		r.note("memory: /proc/meminfo unreadable")
		m.Available = false
		return m
	}
	// /proc/meminfo 以 kB 报告；统一换算为字节，使
	// bundle 中的每个大小都能与 ClickHouse 的字节计数器直接比较。
	kb := func(k string) int64 {
		fields := strings.Fields(kv[k])
		if len(fields) == 0 {
			return 0
		}
		n, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0
		}
		return n * 1024
	}
	m.TotalBytes = kb("MemTotal")
	m.FreeBytes = kb("MemFree")
	m.AvailableBytes = kb("MemAvailable")
	m.BuffersBytes = kb("Buffers")
	m.CachedBytes = kb("Cached")
	m.SwapTotalBytes = kb("SwapTotal")
	m.SwapFreeBytes = kb("SwapFree")
	return m
}

// collectProcesses 返回按 RSS 排序的前 N 个进程。刻意设限：
// 繁忙主机上的完整进程表是数千行噪音，而这里要回答的问题
// 是“还有什么在吃这台机器的内存”。
func collectProcesses() ([]Process, error) {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return nil, err
	}
	pageSize := int64(os.Getpagesize())
	var procs []Process
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // 不是 pid 目录
		}
		statRaw := readFile(filepath.Join(procDir, e.Name(), "stat"))
		if statRaw == "" {
			continue // 进程在 ReadDir 和读取之间退出——正常
		}
		p, ok := parseProcStat(statRaw, pid, pageSize)
		if !ok {
			continue
		}
		if cmd := readCmdline(filepath.Join(procDir, e.Name(), "cmdline")); cmd != "" {
			p.Command = cmd
		}
		// 只为 ClickHouse 自己的二进制保留完整命令行。
		// 其他一律缩减为 argv[0]：这些是别人的
		// 进程，argv 通常携带机密（--password=…、token），
		// 而 bundle 会与支持人员共享。ClickHouse 的 argv 保留
		// （它正是我们要诊断的对象），但仍做脱敏以防万一。
		if !strings.Contains(p.Command, "clickhouse") {
			if i := strings.IndexByte(p.Command, ' '); i > 0 {
				p.Command = p.Command[:i]
			}
		} else {
			p.Command = redactArgs(p.Command)
		}
		procs = append(procs, p)
	}
	sort.Slice(procs, func(i, j int) bool { return procs[i].RSSBytes > procs[j].RSSBytes })
	if len(procs) > maxProcs {
		procs = procs[:maxProcs]
	}
	return procs, nil
}

// parseProcStat 处理 /proc 解析中唯一真正棘手的部分：comm 字段
// 被圆括号包裹，本身可能包含空格或圆括号，因此字段不能简单地
// 按空白切分。应以最后一个 ')' 为锚点。
func parseProcStat(raw string, pid int, pageSize int64) (Process, bool) {
	closeIdx := strings.LastIndex(raw, ")")
	openIdx := strings.Index(raw, "(")
	if closeIdx < 0 || openIdx < 0 || closeIdx < openIdx {
		return Process{}, false
	}
	comm := raw[openIdx+1 : closeIdx]
	rest := strings.Fields(raw[closeIdx+1:])
	// rest[0]=state rest[1]=ppid … rest[17]=num_threads rest[21]=rss(pages)
	if len(rest) < 22 {
		return Process{}, false
	}
	ppid, _ := strconv.Atoi(rest[1])
	threads, _ := strconv.Atoi(rest[17])
	rssPages, _ := strconv.ParseInt(rest[21], 10, 64)
	return Process{
		PID:      pid,
		PPID:     ppid,
		State:    rest[0],
		Threads:  threads,
		RSSBytes: rssPages * pageSize,
		Command:  comm,
	}, true
}

func collectTunables(procs []Process) Tunables {
	t := Tunables{
		TransparentHugepages: bracketed(readFile("/sys/kernel/mm/transparent_hugepage/enabled")),
		THPDefrag:            bracketed(readFile("/sys/kernel/mm/transparent_hugepage/defrag")),
		Swappiness:           trimmed(readFile("/proc/sys/vm/swappiness")),
		OvercommitMemory:     trimmed(readFile("/proc/sys/vm/overcommit_memory")),
		MaxMapCount:          trimmed(readFile("/proc/sys/vm/max_map_count")),
		FsNrOpen:             trimmed(readFile("/proc/sys/fs/nr_open")),
		FsFileMax:            trimmed(readFile("/proc/sys/fs/file-max")),
	}
	// 正在运行的服务器的每进程限制：全局 fs.nr_open 常常
	// 没问题，而 unit 自己的 LimitNOFILE 才是真正的约束。
	// 匹配是严格的——clickhouse-server 或 "clickhouse server"，绝不是
	// 本诊断工具（其自身名称包含 "clickhouse"）、keeper 或
	// 客户端。匹配错误会把错误进程的限制当成服务器的，
	// 而 JSON 中没有任何说明。
	serverPID := 0
	selfPID := os.Getpid()
	for _, p := range procs {
		if !isClickHouseServer(p, selfPID) {
			continue
		}
		serverPID = p.PID
		t.ClickHouseProcessName = p.Command
		limits := readFile(filepath.Join(procDir, strconv.Itoa(p.PID), "limits"))
		for _, line := range strings.Split(limits, "\n") {
			switch {
			case strings.HasPrefix(line, "Max open files"):
				if f := strings.Fields(line); len(f) >= 5 {
					t.ClickHouseNofileSoft, t.ClickHouseNofileHard = f[3], f[4]
				}
			case strings.HasPrefix(line, "Max processes"):
				if f := strings.Fields(line); len(f) >= 4 {
					t.ClickHouseNprocSoft = f[2]
				}
			}
		}
		break
	}

	// SERVER 所在 cgroup 的限制，通过 /proc/<pid>/cgroup 解析。
	// 读取 cgroupfs 根目录只在容器命名空间内有效；在
	// systemd 管理的主机上——恰好是 -host-info 默认启用的场景——
	// 根目录没有 memory.max，clickhouse-server.service 上的 MemoryMax=
	// 永远不会出现，而这正是此字段的典型场景。
	t.CgroupMemoryLimit, t.CgroupCPUMax = cgroupLimits(serverPID)
	return t
}

// cgroupLimits 解析作用于 pid 所在 cgroup 的内存和 CPU 限制，
// 沿 v2 层级向上查找第一个设置了限制的祖先（树中任意位置的限制
// 都会约束叶子）。pid 为 0、树不可读或非 Linux 主机
// 都会降级为容器根回退，最后降级为 ""。
func cgroupLimits(pid int) (mem, cpu string) {
	if pid != 0 {
		for _, line := range strings.Split(readFile(filepath.Join(procDir, strconv.Itoa(pid), "cgroup")), "\n") {
			// v2: "0::<path>"
			if rest, ok := strings.CutPrefix(line, "0::"); ok {
				for p := strings.TrimPrefix(rest, "/"); ; p = filepath.Dir(p) {
					dir := filepath.Join("/sys/fs/cgroup", p)
					if mem == "" {
						if v := trimmed(readFile(filepath.Join(dir, "memory.max"))); v != "" && v != "max" {
							mem = v
						}
					}
					if cpu == "" {
						if v := trimmed(readFile(filepath.Join(dir, "cpu.max"))); v != "" && !strings.HasPrefix(v, "max") {
							cpu = v
						}
					}
					if p == "." || p == "/" || p == "" {
						break
					}
				}
			}
			// v1: "<n>:memory:<path>" / "<n>:cpu,cpuacct:<path>"
			if f := strings.SplitN(line, ":", 3); len(f) == 3 {
				ctrl, path := f[1], f[2]
				if strings.Contains(ctrl, "memory") && mem == "" {
					mem = trimmed(readFile(filepath.Join("/sys/fs/cgroup/memory", path, "memory.limit_in_bytes")))
				}
				if strings.Contains(ctrl, "cpu") && !strings.Contains(ctrl, "cpuset") && cpu == "" {
					cpu = trimmed(readFile(filepath.Join("/sys/fs/cgroup/cpu", path, "cpu.cfs_quota_us")))
				}
			}
		}
	}
	// 回退：cgroupfs 根目录——在容器命名空间内是正确的，
	// 那里的“根”就是容器自身的限制。
	if mem == "" {
		mem = firstNonEmpty(
			trimmed(readFile("/sys/fs/cgroup/memory.max")),
			trimmed(readFile("/sys/fs/cgroup/memory/memory.limit_in_bytes")))
	}
	if cpu == "" {
		cpu = firstNonEmpty(
			trimmed(readFile("/sys/fs/cgroup/cpu.max")),
			trimmed(readFile("/sys/fs/cgroup/cpu/cpu.cfs_quota_us")))
	}
	return mem, cpu
}

// ── 小型辅助函数 ────────────────────────────────────────────────────────

func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func readCmdline(path string) string {
	raw := readFile(path)
	if raw == "" {
		return ""
	}
	// cmdline 以 NUL 分隔；末尾的 NUL 会产生一个空的末字段。
	parts := strings.Split(strings.TrimRight(raw, "\x00"), "\x00")
	cmd := strings.Join(parts, " ")
	const max = 300
	if len(cmd) > max {
		cut := max
		// 不要在 rune 中间截断：encoding/json 会把
		// 被撕裂的 UTF-8 序列替换为 U+FFFD。
		for cut > 0 && !utf8.RuneStart(cmd[cut]) {
			cut--
		}
		cmd = cmd[:cut] + "…"
	}
	return cmd
}

// reSecretArg 匹配名称暗示机密的 argv token。值部分
// （= 之后）会被替换；对于分离形式（"--password s3cret"），
// redactArgs 还会清空下一个 token。
var reSecretArg = regexp.MustCompile(`(?i)^--?[^=]*(password|secret|token|credential|access[-_]?key|api[-_]?key)[^=]*`)

// redactArgs 在命令行存入 bundle 之前剥离其中可能的机密。
func redactArgs(cmd string) string {
	parts := strings.Split(cmd, " ")
	for i, a := range parts {
		if !reSecretArg.MatchString(a) {
			continue
		}
		if eq := strings.IndexByte(a, '='); eq >= 0 {
			parts[i] = a[:eq+1] + "[redacted]"
		} else if i+1 < len(parts) {
			parts[i+1] = "[redacted]"
		}
	}
	return strings.Join(parts, " ")
}

// isClickHouseServer 报告 p 是否为 ClickHouse SERVER 进程——
// 不是本诊断工具（其自身名称包含 "clickhouse"），不是
// clickhouse-keeper，也不是意外出现的 clickhouse-client。两种启动方式
// 都能识别：专用的 clickhouse-server 二进制，以及多工具
// 形式 "clickhouse server"。
func isClickHouseServer(p Process, selfPID int) bool {
	if p.PID == selfPID {
		return false
	}
	f := strings.Fields(p.Command)
	if len(f) == 0 {
		return false
	}
	base := filepath.Base(f[0])
	if strings.HasPrefix(base, "clickhouse-server") {
		return true
	}
	return base == "clickhouse" && len(f) > 1 && f[1] == "server"
}

func parseKeyValueFile(path, sep string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(readFile(path), "\n") {
		if k, v, ok := splitKV(line, sep); ok {
			out[k] = v
		}
	}
	return out
}

func splitKV(line, sep string) (string, string, bool) {
	i := strings.Index(line, sep)
	if i < 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
}

// bracketed 从 sysfs 的多选值中提取被选中的选项，
// 例如 "always [madvise] never"。
func bracketed(s string) string {
	open := strings.Index(s, "[")
	close := strings.Index(s, "]")
	if open >= 0 && close > open {
		return s[open+1 : close]
	}
	return trimmed(s)
}

func trimmed(s string) string { return strings.TrimSpace(s) }

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Summary 是供终端使用的每行一项事实的渲染，让运维人员
// 无需打开 JSON 就能看到概览事实。
func (r *Report) Summary() string {
	var b strings.Builder
	if r.OS.Available {
		fmt.Fprintf(&b, "  主机：%s | %s %s | 内核 %s | %s\n",
			r.OS.Hostname, r.OS.Distro, r.OS.DistroVersion, r.OS.KernelVersion, r.OS.Arch)
	}
	if r.CPU.Available {
		fmt.Fprintf(&b, "  CPU： %d 逻辑核 | %s | 负载 %s\n",
			r.CPU.LogicalCPUs, r.CPU.ModelName, strings.Join(r.CPU.LoadAvg, " "))
	}
	if r.Memory.Available {
		fmt.Fprintf(&b, "  内存： %.1f GiB 总计 | %.1f GiB 可用 | swap %.1f GiB\n",
			gib(r.Memory.TotalBytes), gib(r.Memory.AvailableBytes), gib(r.Memory.SwapTotalBytes))
	}
	if r.Tunables.TransparentHugepages != "" {
		fmt.Fprintf(&b, "  THP： %s（swappiness %s）\n",
			r.Tunables.TransparentHugepages, r.Tunables.Swappiness)
	}
	return b.String()
}

func gib(b int64) float64 { return float64(b) / (1 << 30) }
