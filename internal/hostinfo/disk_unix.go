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

// pseudoFS are kernel/virtual filesystems that have no capacity worth
// reporting; including them buries the handful of real mounts a support
// engineer needs (notably where ClickHouse keeps its data).
var pseudoFS = map[string]bool{
	"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true,
	"cgroup": true, "cgroup2": true, "pstore": true, "bpf": true,
	"securityfs": true, "debugfs": true, "tracefs": true, "configfs": true,
	"fusectl": true, "hugetlbfs": true, "mqueue": true, "autofs": true,
	"binfmt_misc": true, "rpc_pipefs": true, "nsfs": true, "squashfs": true,
	"ramfs": true, "efivarfs": true, "selinuxfs": true, "fuse.gvfsd-fuse": true,
}

// collectDisks reads the mount table and adds real capacity via statfs.
// Reading /proc/mounts (rather than /etc/fstab) means we see what is
// actually mounted now, including bind mounts and container volumes.
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
		// Skip single-FILE bind mounts. Containers mount /etc/resolv.conf,
		// /etc/hostname and /etc/hosts this way; statfs reports the whole
		// backing filesystem for each, so they show up as duplicate
		// full-size "disks" and bury the mounts that actually matter.
		if fi, err := os.Stat(mount); err != nil || !fi.IsDir() {
			continue
		}
		seen[mount] = true

		var st syscall.Statfs_t
		if err := syscall.Statfs(mount, &st); err != nil {
			continue // unreadable mount (permissions, stale NFS) — skip quietly
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

// unescapeMount decodes the octal escapes the kernel uses in /proc/mounts
// for spaces and tabs in mount paths (e.g. "/mnt/my\040disk").
func unescapeMount(s string) string {
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
