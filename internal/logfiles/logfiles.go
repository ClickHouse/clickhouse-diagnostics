// Package logfiles 将 ClickHouse 的磁盘服务器日志复制到诊断 bundle 中。
//
// 有用的部分不是复制，而是发现：它不假设 /var/log/clickhouse-server，
// 而是读取服务器配置并跟随实际生效的 <log>/<errorlog> 路径。
// 运维人员迁移日志位置的频率远高于改动其他任何东西，
// 而一个因为日志位于数据卷上就静默不含日志的 bundle，
// 正是这里要避免的失败。
//
// gov 模式下不采集：日志行以自由文本形式包含原始查询、表名和
// 文件路径。哈希这些内容会摧毁日志之所以有用的东西，
// 这与不提供查询分析 bundle 的理由相同。
package logfiles

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"clickhouse-diagnostic/internal/config"
)

// DefaultLogDir 是打包默认位置，在配置未给出任何结果
// （或因权限原因无法读取）时使用。
const DefaultLogDir = "/var/log/clickhouse-server"

// reLogPath 匹配服务器配置中的 <log> 和 <errorlog> 元素。
// 特意使用定向正则而不是完整 XML 解析：ClickHouse
// 配置使用 include、变量替换和 XML/YAML 混合，而我们只需要
// 两个众所周知的叶子元素——部分解析绝不能导致运行失败。
var reLogPath = regexp.MustCompile(`(?is)<(log|errorlog)>\s*([^<\s][^<]*?)\s*</(?:log|errorlog)>`)

// Options 控制采集。零值是安全的默认值。
type Options struct {
	// Dir 设置时按原样使用，并跳过发现流程。
	Dir string
	// Dir 为空时，在 ConfigDir 中搜索 <log>/<errorlog> 路径。
	ConfigDir string
	// IncludeArchives 同时复制轮转的 *.gz 日志（默认关闭：
	// 它们经常比 bundle 中其他所有内容加起来还大）。
	IncludeArchives bool
	// MaxBytesPerFile 限制每个被复制的文件。更大的文件会做尾部复制——
	// 近期的一端才能解释当前的故障。
	MaxBytesPerFile int64
}

// Result 报告发生了什么，以便调用方打印诚实的摘要。
type Result struct {
	Dirs       []string
	Copied     []string
	Truncated  []string
	TotalBytes int64
	Notes      []string
}

const defaultMaxBytes = 50 << 20 // 每个文件 50 MiB

// Collect 将日志复制到 <destDir>/logs/ 并返回其行为。
// 对“什么也没找到”绝不返回错误——远程运行时日志目录缺失是
// 正常情况，会以备注形式报告。
func Collect(destDir string, opts Options) (*Result, error) {
	if opts.MaxBytesPerFile <= 0 {
		opts.MaxBytesPerFile = defaultMaxBytes
	}
	res := &Result{}

	dirs := DiscoverDirs(opts, res)
	if len(dirs) == 0 {
		res.Notes = append(res.Notes, "no readable ClickHouse log directory found — "+
			"run on the ClickHouse host, or pass -logs-dir")
		return res, nil
	}
	res.Dirs = dirs

	outDir := filepath.Join(destDir, "logs")
	if err := os.MkdirAll(outDir, 0750); err != nil {
		return res, fmt.Errorf("创建日志输出目录失败：%w", err)
	}

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("%s: %v", dir, err))
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !wanted(e.Name(), opts.IncludeArchives) {
				continue
			}
			// 仅限普通文件。copyTail 会跟随符号链接，而
			// 日志目录通常可由 clickhouse 服务账号写入，同时本工具
			// 常以 root 运行——一个植入的
			// `evil.log -> /etc/shadow` 绝不能进入与支持人员共享的
			// bundle。拒绝（并记录备注）而不是解析，
			// 可以让这一决定在输出中可见。
			if !e.Type().IsRegular() {
				res.Notes = append(res.Notes,
					fmt.Sprintf("%s: skipped (not a regular file)", filepath.Join(dir, e.Name())))
				continue
			}
			src := filepath.Join(dir, e.Name())
			// 压平到一个目录，但同一文件名出现在两个日志目录时
			// 保留来源。
			dst := filepath.Join(outDir, uniqueName(outDir, e.Name()))
			n, truncated, err := copyTail(src, dst, opts.MaxBytesPerFile)
			if err != nil {
				res.Notes = append(res.Notes, fmt.Sprintf("%s: %v", src, err))
				continue
			}
			res.Copied = append(res.Copied, filepath.Base(dst))
			res.TotalBytes += n
			if truncated {
				res.Truncated = append(res.Truncated, filepath.Base(dst))
			}
		}
	}
	sort.Strings(res.Copied)
	return res, nil
}

// DiscoverDirs 按优先顺序解析要读取的目录：
// 显式 flag，然后是配置中声明的路径，最后是
// 打包默认值。结果会去重并做存在性检查。
func DiscoverDirs(opts Options, res *Result) []string {
	if opts.Dir != "" {
		if isDir(opts.Dir) {
			return []string{filepath.Clean(opts.Dir)}
		}
		if res != nil {
			res.Notes = append(res.Notes,
				fmt.Sprintf("-logs-dir %q is not a readable directory", opts.Dir))
		}
		return nil
	}

	seen := map[string]bool{}
	var dirs []string
	add := func(d string) {
		d = filepath.Clean(d)
		if d == "" || seen[d] || !isDir(d) {
			return
		}
		seen[d] = true
		dirs = append(dirs, d)
	}

	for _, p := range LogPathsFromConfig(opts.ConfigDir) {
		add(filepath.Dir(p))
	}
	add(DefaultLogDir)
	return dirs
}

// LogPathsFromConfig 扫描配置目录（以及相邻的
// 主配置 config.xml）中的 <log>/<errorlog> 值。
func LogPathsFromConfig(configDir string) []string {
	if configDir == "" {
		return nil
	}
	var candidates []string
	candidates = append(candidates, configDir)
	// config.d/ 位于 config.xml 旁边，因此值得扫描父目录——
	// 但仅限 *.d 目录。对于 "-config-dir /etc/clickhouse-server"，
	// 父目录是 /etc，任何无关的 /etc/*.xml 只要带有 <log> 元素，
	// 就会提供一个目录，其中的文件随后会进入 bundle。
	if base := filepath.Base(filepath.Clean(configDir)); strings.HasSuffix(base, ".d") {
		if parent := filepath.Dir(filepath.Clean(configDir)); parent != "." {
			candidates = append(candidates, parent)
		}
	}

	seen := map[string]bool{}
	var paths []string
	for _, root := range candidates {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := strings.ToLower(e.Name())
			if !strings.HasSuffix(name, ".xml") && !strings.HasSuffix(name, ".conf") {
				continue
			}
			blob, err := os.ReadFile(filepath.Join(root, e.Name()))
			if err != nil {
				continue
			}
			// 注释掉的示例不是配置：否则注释中的 <log> 路径
			// 会把运维人员从未配置过的目录列入要复制文件的
			// 位置清单。在当今自带的 config.xml 上并无可观察到的错误，
			// 因为其中真实的 <log>/<errorlog> 是生效的，
			// 但这与曾经的 <zookeeper> 示例属于同一类问题。
			for _, m := range reLogPath.FindAllStringSubmatch(string(config.StripComments(blob)), -1) {
				p := strings.TrimSpace(m[2])
				// 跳过未解析的变量替换，如 <log from_env="..."/>
				// 以及无法可靠定位的相对路径。
				if p == "" || strings.Contains(p, "{") || !filepath.IsAbs(p) || seen[p] {
					continue
				}
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}
	sort.Strings(paths)
	return paths
}

func wanted(name string, includeArchives bool) bool {
	l := strings.ToLower(name)
	if strings.HasSuffix(l, ".log") {
		return true
	}
	return includeArchives && (strings.HasSuffix(l, ".gz") || strings.HasSuffix(l, ".zst"))
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// uniqueName 在两个日志目录含有相同文件名时避免覆盖
// （例如两个 shard 的 clickhouse-server.log）。
func uniqueName(dir, name string) string {
	if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; i < 100; i++ {
		cand := fmt.Sprintf("%s-%d%s", base, i, ext)
		if _, err := os.Stat(filepath.Join(dir, cand)); os.IsNotExist(err) {
			return cand
		}
	}
	return name
}

// copyTail 将 src 复制到 dst，对于超大文件只保留最后 max 字节。
// 它报告写入了多少字节以及是否发生截断。
//
// 截断保留文件的末尾，因为故障诊断依据的是近期日志行；
// 头部会记录数据已被丢弃，以免有人把第一条幸存的行
// 当作日志的开头。
func copyTail(src, dst string, max int64) (written int64, truncated bool, err error) {
	// O_NOFOLLOW（unix）关闭目录扫描检查留下的竞态：
	// 促使我们拒绝符号链接的同一威胁模型——日志目录可由服务账号
	// 写入、工具以 root 运行——允许在扫描与本次 open 之间
	// 替换符号链接。
	in, err := openNoFollow(src)
	if err != nil {
		return 0, false, err
	}
	defer in.Close()

	fi, err := in.Stat()
	if err != nil {
		return 0, false, err
	}
	// 对已打开的 fd 做 fstat——无竞态。能捕获被换入的
	// FIFO、设备或目录；符号链接已被 openNoFollow 拒绝。
	// 指向敏感文件的硬链接是普通文件，会通过两项
	// 检查——Linux fs.protected_hardlinks=1（默认值）阻止
	// 非特权用户创建这种链接，但那是默认值，不是
	// 本代码能提供的保证。
	if !fi.Mode().IsRegular() {
		return 0, false, fmt.Errorf("%s: not a regular file", src)
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return 0, false, err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		// 半写入的文件会被归档，却既不出现于
		// Copied 也不计入 TotalBytes；删除它，让失败表现为缺失
		// 加一条备注，而不是一份静默变短的日志。
		if err != nil {
			os.Remove(dst)
		}
	}()

	// 复制一个快照：限制在已测量的大小内，这样正在被
	// 写入的日志不会让副本超过上限（也不会超过截断头部
	// 所声称的大小）。
	limit := fi.Size()
	if fi.Size() > max {
		truncated = true
		start := fi.Size() - max
		if _, err := in.Seek(start, io.SeekStart); err != nil {
			return 0, true, err
		}
		// 在写入头部之前丢弃不完整的首行，让头部
		// 报告保留的精确有效载荷字节数，而不是名义上的
		// 上限（行对齐最多会削掉一行）。
		skip, err := lengthToNewline(in)
		if err != nil {
			return 0, true, err
		}
		// lengthToNewline 已向前读取；重新定位到行边界。
		if _, err := in.Seek(start+skip, io.SeekStart); err != nil {
			return 0, true, err
		}
		limit = fi.Size() - (start + skip)
		header := fmt.Sprintf(
			"### support-diagnostic: TRUNCATED — original %d bytes, kept last %d bytes (tail) ###\n",
			fi.Size(), limit)
		hn, err := out.WriteString(header)
		if err != nil {
			return 0, true, err
		}
		written += int64(hn)
	}

	// written 包含截断头部：它报告的是 bundle 中产出的字节数
	// （Result.TotalBytes 所汇总的），而不是从 src 读取的字节数。
	n, err := io.Copy(out, io.LimitReader(in, limit))
	return written + n, truncated, err
}

// lengthToNewline 返回当前位置与当前行末尾之间有多少字节
// （包含换行符），最多以缓冲块扫描 64 KiB。如果界限内没有换行
// （二进制或单行文件），则跳过整个界限。调用方之后必须
// 重新 Seek：这里的读取会让 fd 越过答案位置。
func lengthToNewline(f *os.File) (int64, error) {
	const bound = 64 << 10
	buf := make([]byte, 8<<10)
	var consumed int64
	for consumed < bound {
		n, err := f.Read(buf)
		if n > 0 {
			if i := bytes.IndexByte(buf[:n], '\n'); i >= 0 {
				return consumed + int64(i) + 1, nil
			}
			consumed += int64(n)
		}
		if err != nil {
			if err == io.EOF {
				return consumed, nil // EOF：没有需要对齐的内容
			}
			return consumed, err
		}
	}
	return bound, nil
}

// Summary 渲染一份面向运维人员的简短报告。
func (r *Result) Summary() string {
	var b strings.Builder
	if len(r.Copied) == 0 {
		b.WriteString("  未采集到日志文件\n")
	} else {
		fmt.Fprintf(&b, "  已采集 %d 个日志文件，共 %.1f MiB，来自：%s\n",
			len(r.Copied), float64(r.TotalBytes)/(1<<20), strings.Join(r.Dirs, ", "))
		if len(r.Truncated) > 0 {
			fmt.Fprintf(&b, "  尾部截断（受大小上限限制）：%s\n", strings.Join(r.Truncated, ", "))
		}
	}
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "  备注：%s\n", n)
	}
	return b.String()
}
