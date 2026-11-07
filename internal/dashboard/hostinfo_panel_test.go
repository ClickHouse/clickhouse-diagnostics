package dashboard

import (
	"strings"
	"testing"

	"clickhouse-diagnostic/internal/hostinfo"
)

func findCheck(t *testing.T, rows []map[string]interface{}, setting string) map[string]interface{} {
	t.Helper()
	for _, r := range rows {
		if r["setting"] == setting {
			return r
		}
	}
	t.Fatalf("没有针对%q的检查", setting)
	return nil
}

// 警告栏的意思是“ClickHouse也会就此告警”。这两项是
// programs/server/Server.cpp::sanityChecks中的真实检查。
func TestHostChecks_FlagsWhatClickHouseFlags(t *testing.T) {
	rep := &hostinfo.Report{Tunables: hostinfo.Tunables{
		TransparentHugepages: "always",
		OvercommitMemory:     "2",
	}}
	rows := hostChecks(rep)
	if got := findCheck(t, rows, "transparent_hugepages")["status"]; got != "warning" {
		t.Errorf("THP=always必须告警（ClickHouse在启动时也会告警），得到%v", got)
	}
	if got := findCheck(t, rows, "vm.overcommit_memory")["status"]; got != "warning" {
		t.Errorf("overcommit_memory=2必须告警（ClickHouse在启动时也会告警），得到%v", got)
	}

	ok := hostChecks(&hostinfo.Report{Tunables: hostinfo.Tunables{
		TransparentHugepages: "madvise",
		OvercommitMemory:     "1",
	}})
	if got := findCheck(t, ok, "transparent_hugepages")["status"]; got != "ok" {
		t.Errorf("THP=madvise应为ok，得到%v", got)
	}
	if got := findCheck(t, ok, "vm.overcommit_memory")["status"]; got != "ok" {
		t.Errorf("overcommit_memory=1应为ok，得到%v", got)
	}
}

// vm.swappiness是常见的数据库调优经验，但ClickHouse既不在启动时
// 检查它，也没有记录目标值——因此dashboard只报告它而不评级。
// 标记告警会把一个ClickHouse并未给出的建议说成是它的建议。
func TestHostChecks_DoesNotInventRecommendations(t *testing.T) {
	rows := hostChecks(&hostinfo.Report{Tunables: hostinfo.Tunables{Swappiness: "60"}})
	sw := findCheck(t, rows, "vm.swappiness")
	if sw["status"] == "warning" {
		t.Error("swappiness不应被评级：ClickHouse没有为其记录目标值")
	}
	if sw["value"] != "60" {
		t.Errorf("swappiness值仍应被报告，得到%v", sw["value"])
	}
}

// 启动时可用内存低于2 GiB时，ClickHouse会告警。
func TestHostChecks_LowAvailableMemory(t *testing.T) {
	low := hostChecks(&hostinfo.Report{Memory: hostinfo.MemoryInfo{Available: true, AvailableBytes: 1 << 30}})
	if got := findCheck(t, low, "available memory")["status"]; got != "warning" {
		t.Errorf("可用内存1 GiB必须告警，得到%v", got)
	}
	high := hostChecks(&hostinfo.Report{Memory: hostinfo.MemoryInfo{Available: true, AvailableBytes: 8 << 30}})
	if got := findCheck(t, high, "available memory")["status"]; got != "ok" {
		t.Errorf("可用内存8 GiB应为ok，得到%v", got)
	}
}

// 无法读取的tunable绝不能渲染为健康状态。
func TestHostChecks_UnreadableIsNotHealthy(t *testing.T) {
	rows := hostChecks(&hostinfo.Report{})
	c := findCheck(t, rows, "transparent_hugepages")
	if c["status"] == "ok" {
		t.Error("未读取的tunable不应报告为ok")
	}
	if c["value"] != "(unreadable)" {
		t.Errorf("未读取的值应如实说明，得到%v", c["value"])
	}
}

// 两个rlimit都不可读时，过去会连接成字面量“/”——它非空，
// 因此能绕过(unreadable)防护并渲染为健康行。
func TestHostChecks_UnreadableRlimitsAreNotSlash(t *testing.T) {
	rows := hostChecks(&hostinfo.Report{})
	c := findCheck(t, rows, "open files (soft/hard)")
	if c["value"] == "/" {
		t.Fatal(`两个未读取的rlimit被渲染成"/"而不是(unreadable)`)
	}
	if c["value"] != "(unreadable)" {
		t.Errorf("未读取的rlimit应显示(unreadable)，得到%v", c["value"])
	}
}

func TestHostChecks_NilReport(t *testing.T) {
	if rows := hostChecks(nil); rows != nil {
		t.Errorf("nil report不应产生检查，得到%d", len(rows))
	}
}

// 除非存在host_info，否则面板是隐藏的，因此跳过的host-info运行
// 不会在Overview中留下空壳。
func TestTemplate_HostPanelIsConditional(t *testing.T) {
	for _, want := range []string{`id="host-block"`, "const hi=DATA.host_info;", "if(!hi)return;",
		"tbl-host-os", "tbl-host-tunables", "tbl-host-procs", "host-notes"} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("主机面板缺少%q", want)
		}
	}
}

// gib()必须区分“collector无法读取”（null/空）与真正的零。
// 在每台禁用swap的主机上swap_total_bytes都是0，将其渲染成“—”
// 会让读者以为该值未知，而实际上这是关于swap最有用的信息。
// 真值判断（b ? ... : '—'）会把两者混为一谈。
func TestTemplate_GibRendersZeroAsZero(t *testing.T) {
	want := `const gib=b=>(b==null||b==='')?'—':(Number(b)/1073741824).toFixed(2)+' GiB';`
	if !strings.Contains(htmlTemplate, want) {
		t.Errorf("gib辅助函数缺失或已变更；期望%q", want)
	}
	if strings.Contains(htmlTemplate, `const gib=b=>b?`) {
		t.Error("gib使用真值判断，因此真实的0字节读数会被渲染为缺失")
	}
}
