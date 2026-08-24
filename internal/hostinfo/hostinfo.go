// Package hostinfo collects OS, kernel and hardware facts about the machine
// running ClickHouse — the context that explains *why* the server behaves the
// way the ClickHouse system tables say it does.
//
// Deliberately dependency-free: everything here is a read of /proc, /sys or
// /etc, which keeps the tool a single static binary with no supply chain to
// audit. That also means Linux is the first-class target (where ClickHouse
// servers live); on other platforms each section degrades to
// Available:false with a reason rather than failing the run.
//
// Not collected in gov mode — hostnames, mount paths and process command
// lines are exactly the identifiers gov hashing exists to protect, and a
// process command line cannot be meaningfully hashed.
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

// Report is the whole collected picture, written as host_info.json.
type Report struct {
	CollectedAt string     `json:"collected_at"`
	OS          OSInfo     `json:"os"`
	CPU         CPUInfo    `json:"cpu"`
	Memory      MemoryInfo `json:"memory"`
	Disks       []DiskInfo `json:"disks"`
	Processes   []Process  `json:"top_processes_by_rss"`
	// Tunables are the host settings that most often explain ClickHouse
	// misbehaviour and are the first thing support asks for.
	Tunables Tunables `json:"clickhouse_relevant_tunables"`
	// Notes records why a section is empty, so a missing section is never
	// mistaken for a healthy one (the same "don't report a check that
	// didn't run as passing" rule the alert accounting follows).
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
	// Flags ClickHouse actually cares about for vectorised execution.
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

// Tunables are host settings that materially affect ClickHouse. Each is a
// raw string so an unreadable value is distinguishable from a set one.
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
	// ClickHouseNprocSoft is the "Max processes" soft limit from
	// /proc/<pid>/limits — RLIMIT_NPROC, the OS cap on tasks (processes
	// AND threads) for the service account. It is NOT the ClickHouse
	// max_threads setting; the old name/key said it was.
	ClickHouseNprocSoft   string `json:"clickhouse_nproc_soft,omitempty"`
	ClickHouseProcessName string `json:"clickhouse_process,omitempty"`
}

const (
	procDir  = "/proc"
	maxProcs = 25 // top-N by RSS; a full process table is noise in a bundle
)

// Collect gathers everything it can, never failing the run: a section that
// can't be read is marked unavailable and the reason recorded in Notes.
func Collect() *Report {
	r := &Report{CollectedAt: time.Now().UTC().Format(time.RFC3339)}

	if _, err := os.Stat(procDir); err != nil {
		r.note("/proc is not present (non-Linux host?) — OS, CPU, memory, process and tunable sections are unavailable")
		r.OS = OSInfo{Arch: runtimeArch(), GoOS: runtimeOS()}
		if h, err := os.Hostname(); err == nil {
			r.OS.Hostname = h
		}
		r.Disks, _ = collectDisks() // statfs may still work
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

// WriteJSON writes the report as host_info.json into dir.
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

	// /etc/os-release is the portable distro identifier (systemd standard);
	// their tool reports only a generic OS string, which doesn't tell you
	// whether you're on RHEL 8 or Ubuntu 22.04 — often the actual answer.
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
	// Flags worth reporting: these gate ClickHouse's vectorised code paths,
	// and a binary built for one of them on a host without it is a real
	// (and confusing) failure mode. Both architectures are listed — on
	// aarch64 the kernel labels the line "Features" and names are entirely
	// different, so an x86-only list silently reports nothing there.
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
	// /proc/meminfo reports kB; normalise to bytes so every size in the
	// bundle is directly comparable with ClickHouse's byte counters.
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

// collectProcesses returns the top-N processes by RSS. Bounded on purpose:
// a full table on a busy host is thousands of rows of noise, and the
// question this answers is "what else is eating this machine's memory".
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
			continue // not a pid directory
		}
		statRaw := readFile(filepath.Join(procDir, e.Name(), "stat"))
		if statRaw == "" {
			continue // process exited between ReadDir and read — normal
		}
		p, ok := parseProcStat(statRaw, pid, pageSize)
		if !ok {
			continue
		}
		if cmd := readCmdline(filepath.Join(procDir, e.Name(), "cmdline")); cmd != "" {
			p.Command = cmd
		}
		// Full command lines are kept only for ClickHouse's own binaries.
		// Everything else is reduced to argv[0]: these are OTHER PEOPLE'S
		// processes, argv routinely carries secrets (--password=…, tokens),
		// and the bundle is shared with support. ClickHouse argv is kept
		// (it is what we are diagnosing) but redacted just in case.
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

// parseProcStat handles the one genuinely tricky part of /proc parsing: the
// comm field is wrapped in parentheses and may itself contain spaces or
// parentheses, so fields cannot simply be split on whitespace. Anchor on
// the LAST ')' instead.
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
	// Per-process limits for the running server: the global fs.nr_open is
	// often fine while the unit's own LimitNOFILE is the binding constraint.
	// Matching is strict — clickhouse-server or "clickhouse server", never
	// this diagnostic tool (its own name contains "clickhouse"), keeper or
	// a client. Getting this wrong reports the limits of the wrong process
	// with nothing in the JSON to say so.
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

	// Cgroup limits for the SERVER's cgroup, resolved via /proc/<pid>/cgroup.
	// Reading the cgroupfs root only works inside a container namespace; on a
	// systemd-managed host — exactly where -host-info defaults to on — the
	// root has no memory.max and a MemoryMax= on clickhouse-server.service
	// would never show up, which is the headline case for this field.
	t.CgroupMemoryLimit, t.CgroupCPUMax = cgroupLimits(serverPID)
	return t
}

// cgroupLimits resolves the memory and CPU limits binding on pid's cgroup,
// walking UP the v2 hierarchy to the first ancestor that sets one (a limit
// anywhere up the tree binds the leaf). pid 0, an unreadable tree, or a
// non-Linux host all degrade to the container-root fallback, then to "".
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
	// Fallback: the cgroupfs root — correct inside a container namespace,
	// where the "root" is the container's own limit.
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

// ── small helpers ────────────────────────────────────────────────────────

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
	// cmdline is NUL-separated; trailing NUL produces an empty final field.
	parts := strings.Split(strings.TrimRight(raw, "\x00"), "\x00")
	cmd := strings.Join(parts, " ")
	const max = 300
	if len(cmd) > max {
		cut := max
		// Do not slice mid-rune: encoding/json would replace the torn
		// UTF-8 sequence with U+FFFD.
		for cut > 0 && !utf8.RuneStart(cmd[cut]) {
			cut--
		}
		cmd = cmd[:cut] + "…"
	}
	return cmd
}

// reSecretArg matches an argv token whose NAME suggests a secret. The value
// part (after =) is replaced; for the separated form ("--password s3cret")
// redactArgs also blanks the FOLLOWING token.
var reSecretArg = regexp.MustCompile(`(?i)^--?[^=]*(password|secret|token|credential|access[-_]?key|api[-_]?key)[^=]*`)

// redactArgs strips likely secrets from a command line before it is stored
// in the bundle.
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

// isClickHouseServer reports whether p is the ClickHouse SERVER process —
// not this diagnostic tool (whose own name contains "clickhouse"), not
// clickhouse-keeper, not a stray clickhouse-client. Both launch styles are
// recognised: the dedicated clickhouse-server binary and the multi-tool
// form "clickhouse server".
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

// bracketed extracts the selected option from a sysfs multi-choice value
// such as "always [madvise] never".
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

// Summary is a one-line-per-fact rendering for the terminal, so the operator
// sees the headline facts without opening the JSON.
func (r *Report) Summary() string {
	var b strings.Builder
	if r.OS.Available {
		fmt.Fprintf(&b, "  host: %s | %s %s | kernel %s | %s\n",
			r.OS.Hostname, r.OS.Distro, r.OS.DistroVersion, r.OS.KernelVersion, r.OS.Arch)
	}
	if r.CPU.Available {
		fmt.Fprintf(&b, "  cpu:  %d logical | %s | load %s\n",
			r.CPU.LogicalCPUs, r.CPU.ModelName, strings.Join(r.CPU.LoadAvg, " "))
	}
	if r.Memory.Available {
		fmt.Fprintf(&b, "  mem:  %.1f GiB total | %.1f GiB available | swap %.1f GiB\n",
			gib(r.Memory.TotalBytes), gib(r.Memory.AvailableBytes), gib(r.Memory.SwapTotalBytes))
	}
	if r.Tunables.TransparentHugepages != "" {
		fmt.Fprintf(&b, "  thp:  %s (swappiness %s)\n",
			r.Tunables.TransparentHugepages, r.Tunables.Swappiness)
	}
	return b.String()
}

func gib(b int64) float64 { return float64(b) / (1 << 30) }
