package main

import (
	"errors"
	"strings"
	"testing"
)

func TestLoadBalancerSuspected(t *testing.T) {
	if loadBalancerSuspected([]string{"ch-01", "ch-01", "ch-01"}) {
		t.Error("同一个名称出现三次仍是一个节点")
	}
	if !loadBalancerSuspected([]string{"ch-09", "ch-20", "ch-09"}) {
		t.Error("两个名称说明存在负载均衡器")
	}
	if loadBalancerSuspected([]string{"", "ch-01"}) {
		t.Error("空样本（探测失败）不得被判定为第二个节点")
	}
}

func TestSameMachine(t *testing.T) {
	cases := []struct {
		host, fqdn, local string
		want              bool
	}{
		{"ch-01", "ch-01.example.internal", "ch-01.example.internal", true},
		{"ch-01", "ch-01.example.internal", "ch-01", true},
		{"ch-09", "ch-09.vm.example", "laptop.local", false},
		{"ch-01", "", "", true}, // 本地名称未知：无法判断，不告警
		// 两个共享首标签的完全限定名是两台机器。
		{"ch-01", "ch-01.site-a.example", "ch-01.site-b.example", false},
		{"ch-01.site-a.example", "ch-01.site-a.example", "ch-01.site-a.example", true},
	}
	for _, c := range cases {
		if got := sameMachine(c.host, c.fqdn, c.local); got != c.want {
			t.Errorf("sameMachine(%q,%q,%q) = %v，期望 %v", c.host, c.fqdn, c.local, got, c.want)
		}
	}
}

// 本测试防范的是那次升级处理中的两种混淆：负载均衡器以多个 replica
// 的身份应答，以及本地文件与另一个节点的系统表被一起采集。
func TestNodeWarnings(t *testing.T) {
	lb := nodeIdentity{Host: "ch-09", FQDN: "ch-09.x", Samples: []string{"ch-09", "ch-20", "ch-09"}}
	w := nodeWarnings(lb, "clickhouse-lb.example", "ch-09.x", "onprem", false)
	if len(w) != 1 || !strings.Contains(w[0], "负载均衡器") || !strings.Contains(w[0], "ch-09 和 ch-20") {
		t.Errorf("缺少负载均衡器警告：%v", w)
	}

	remote := nodeIdentity{Host: "ch-03", FQDN: "ch-03.x", Samples: []string{"ch-03", "ch-03", "ch-03"}}
	w = nodeWarnings(remote, "ch-03.x", "ops-laptop", "onprem", true)
	if len(w) != 1 || !strings.Contains(w[0], "本机（ops-laptop）") {
		t.Errorf("缺少远程主机警告：%v", w)
	}
	// 同一台机器、回环地址，或不采集本地文件：不告警。
	if w := nodeWarnings(remote, "ch-03.x", "ch-03", "onprem", true); len(w) != 0 {
		t.Errorf("同一台机器不得告警：%v", w)
	}
	if w := nodeWarnings(remote, "localhost", "ops-laptop", "onprem", true); len(w) != 0 {
		t.Errorf("回环地址不得告警：%v", w)
	}
	if w := nodeWarnings(remote, "ch-03.x", "ops-laptop", "onprem", false); len(w) != 0 {
		t.Errorf("未采集本地文件，没有可混合的内容：%v", w)
	}
	if w := nodeWarnings(nodeIdentity{Err: errors.New("no grant")}, "x", "y", "onprem", true); len(w) != 0 {
		t.Errorf("探测失败必须保持静默：%v", w)
	}
}

func TestDecideSMTCollection(t *testing.T) {
	d := decideSMTCollection(nil, false, "hint")
	if d.EffectiveMode != "cloud" || !strings.Contains(d.Note, "onprem → cloud") || !strings.Contains(d.Message, "切换到 cloud 采集") {
		t.Errorf("可用 fan-out 时必须切换：%+v", d)
	}
	d = decideSMTCollection(errors.New("Code: 497. DB::Exception: user: Not enough privileges. To execute this query, it's necessary to have the grant REMOTE ON *.*"), false, "hint")
	if d.EffectiveMode != "onprem" || !strings.Contains(d.Note, "fan-out 探测失败") || !strings.Contains(d.Message, "REMOTE") {
		t.Errorf("fan-out 失败时必须停留在单节点并说明原因：%+v", d)
	}
	d = decideSMTCollection(nil, true, "the hint")
	if d.EffectiveMode != "onprem" || !strings.Contains(d.Note, "-single-node") || d.Message != "the hint" {
		t.Errorf("-single-node 必须优先：%+v", d)
	}
}

func TestIsSharedMergeTreeAndUptime(t *testing.T) {
	for v, want := range map[string]bool{"1": true, "true\n": true, "0": false, "": false, "false": false} {
		if isSharedMergeTree(v) != want {
			t.Errorf("isSharedMergeTree(%q) = %v", v, !want)
		}
	}
	for s, want := range map[int64]string{0: "unknown", 59: "0m", 3700: "1h 1m", 90000: "1d 1h", 410798: "4d 18h"} {
		if got := humanUptime(s); got != want {
			t.Errorf("humanUptime(%d) = %q，期望 %q", s, got, want)
		}
	}
}

// 每个 ClickHouse Cloud 服务都以负载均衡端点应答，因此在 cloud 模式下
// 这是关于共享表来源的 Note，而不是 bundle 不可信的警告。在自建集群上，
// 同样的症状曾让一次升级处理读错节点整整一天，所以那里仍保持为 Warning。
func TestNodeWarnings_LoadBalancerReadsByMode(t *testing.T) {
	lb := nodeIdentity{Host: "replica-a", FQDN: "replica-a.svc", Samples: []string{"replica-a", "replica-b", "replica-a"}}

	cloud := nodeWarnings(lb, "svc.eu-central-1.aws.clickhouse.cloud", "laptop.local", "cloud", false)
	if len(cloud) != 1 {
		t.Fatalf("cloud：期望一行，实际得到 %v", cloud)
	}
	if !strings.HasPrefix(cloud[0], "Note:") {
		t.Errorf("cloud：负载均衡端点必须表现为 Note，实际得到 %q", cloud[0])
	}
	for _, unwanted := range []string{"请将 -host 指向一个 replica", "不会描述任何单一节点"} {
		if strings.Contains(cloud[0], unwanted) {
			t.Errorf("cloud：不适用于 Cloud 端点的建议仍然存在：%q", unwanted)
		}
	}
	if !strings.Contains(cloud[0], "分发") {
		t.Errorf("cloud：Note 应说明按 replica 划分的表会分发，实际得到 %q", cloud[0])
	}

	for _, mode := range []string{"onprem", "gov", ""} {
		got := nodeWarnings(lb, "ch-lb.internal", "laptop.local", mode, false)
		if len(got) != 1 || !strings.HasPrefix(got[0], "Warning:") {
			t.Errorf("mode %q：负载均衡器必须保持为 Warning，实际得到 %v", mode, got)
		}
	}

	// 混合主机警告不受 mode 影响。
	mixed := nodeWarnings(nodeIdentity{Host: "ch-01", FQDN: "ch-01.internal"}, "ch-01.internal", "laptop.local", "cloud", true)
	if len(mixed) != 1 || !strings.HasPrefix(mixed[0], "Warning:") {
		t.Errorf("来自另一台机器的 host facts 在任何 mode 下都必须保持为警告，实际得到 %v", mixed)
	}
}
