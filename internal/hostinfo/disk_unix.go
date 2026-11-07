//go:build unix

package hostinfo

import (
	"os"
	"runtime"
	"strings"
	"syscall"
)

func runtimeArch() string { return runtime.GOARCH }
func runtimeOS() string   { return runtime.GOOS }

// pseudoFS 是没有值得报告容量的内核/虚拟文件系统；
// 包含它们会淹没支持工程师真正需要的那几个挂载点
// （尤其是 ClickHouse 存放数据的位置）。
var pseudoFS = map[string]bool{
	"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true,
	"cgroup": true, "cgroup2": true, "pstore": true, "bpf": true,
	"securityfs": true, "debugfs": true, "tracefs": true, "configfs": true,
	"fusectl": true, "hugetlbfs": true, "mqueue": true, "autofs": true,
	"binfmt_misc": true, "rpc_pipefs": true, "nsfs": true, "squashfs": true,
	"ramfs": true, "efivarfs": true, "selinuxfs": true, "fuse.gvfsd-fuse": true,
}

// collectDisks 读取挂载表，并通过 statfs 补充真实容量。
// 读取 /proc/mounts（而不是 /etc/fstab）意味着我们能看到
// 当前实际挂载的内容，包括 bind mount 和容器卷。
func collectDisks() ([]DiskInfo, error) {
	raw := readFile("/proc/mounts")
	if raw == "" {
		raw = readFile("/etc/mtab")
	}
	if raw == "" {
		return nil, nil
	}
	seen := map[string]bool{}
	var out []DiskInfo
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		device, mount, fstype := f[0], unescapeMount(f[1]), f[2]
		if pseudoFS[fstype] || strings.HasPrefix(fstype, "fuse.") || seen[mount] {
			continue
		}
		// 跳过单文件 bind mount。容器以这种方式挂载 /etc/resolv.conf、
		// /etc/hostname 和 /etc/hosts；statfs 会为每个文件报告整个
		// 底层文件系统，因此它们会显示为重复的满容量
		// “磁盘”，淹没真正重要的挂载点。
		if fi, err := os.Stat(mount); err != nil || !fi.IsDir() {
			continue
		}
		seen[mount] = true

		var st syscall.Statfs_t
		if err := syscall.Statfs(mount, &st); err != nil {
			continue // 挂载点不可读（权限、NFS 陈旧）——静默跳过
		}
		bsize := uint64(st.Bsize)
		total := st.Blocks * bsize
		free := st.Bavail * bsize
		if total == 0 {
			continue
		}
		used := total - (st.Bfree * bsize)
		out = append(out, DiskInfo{
			Device:     device,
			MountPoint: mount,
			FSType:     fstype,
			TotalBytes: total,
			FreeBytes:  free,
			UsedPct:    round1(float64(used) / float64(total) * 100),
		})
	}
	return out, nil
}

// unescapeMount 解码内核在 /proc/mounts 中为挂载路径里的
// 空格和制表符使用的八进制转义（例如 "/mnt/my\040disk"）。
func unescapeMount(s string) string {
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
