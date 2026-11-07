package main

import "testing"

// TestResolveLocalCollector 固定了两个读取本地文件系统的 collector 随 mode
// 变化的默认值。这张表就是规格：onprem 默认采集，因为工具运行在服务器上；
// cloud 不采集，因为它在别处运行；gov 在任何设置下都绝不采集。
func TestResolveLocalCollector(t *testing.T) {
	cases := []struct {
		setting  string
		mode     string
		wantSkip bool
		wantErr  bool
	}{
		// auto——本次改动的核心
		{"auto", "onprem", false, false},
		{"auto", "cloud", true, false},
		{"auto", "gov", true, false},
		{"", "onprem", false, false}, // 空值等同于 auto
		{"", "cloud", true, false},

		// 显式 on——除 gov 外处处允许
		{"on", "onprem", false, false},
		{"on", "cloud", false, false},
		{"on", "gov", true, true}, // 被拒绝，且仍然跳过

		// 显式 off——始终遵守，绝不报错
		{"off", "onprem", true, false},
		{"off", "cloud", true, false},
		{"off", "gov", true, false},

		// 容忍用户实际输入的写法
		{"ON", "onprem", false, false},
		{" auto ", "cloud", true, false},

		// mode 在解析器内部也会被规范化。调用点本应传入
		// getUserInput 之后的值，但这个函数守卫着 gov 的隐私保证——
		// "-mode GOV -host-info=on" 曾通过这个确切的漏洞把 host facts
		// 混进了 gov bundle。
		{"on", "GOV", true, true},
		{"on", " Gov ", true, true},
		{"auto", "ONPREM", false, false},
		{"auto", "Cloud", true, false},

		// 其他任何值都是用法错误，必须按失败处理（fail closed）
		{"true", "onprem", true, true},
		{"yes", "onprem", true, true},
	}

	for _, c := range cases {
		skip, err := resolveLocalCollector(c.setting, c.mode, "host-info")
		if skip != c.wantSkip {
			t.Errorf("resolveLocalCollector(%q, %q) skip = %v，期望 %v",
				c.setting, c.mode, skip, c.wantSkip)
		}
		if (err != nil) != c.wantErr {
			t.Errorf("resolveLocalCollector(%q, %q) err = %v，期望错误 %v",
				c.setting, c.mode, err, c.wantErr)
		}
	}
}

// TestResolveLocalCollector_GovNeverCollects 单独陈述表中与安全相关的
// 那一半，这样删除上面的某一行就不会悄悄削弱它。任何设置都不能让 gov 模式
// 采集 host facts 或日志文件。
func TestResolveLocalCollector_GovNeverCollects(t *testing.T) {
	for _, setting := range []string{"auto", "on", "ON", "off", "", "garbage"} {
		for _, mode := range []string{"gov", "GOV", " gov ", "Gov"} {
			for _, name := range []string{"host-info", "logs"} {
				if skip, _ := resolveLocalCollector(setting, mode, name); !skip {
					t.Errorf("gov 模式（写作 %q）采集了 %s，使用 --%s=%q；gov 绝不能"+
						"读取本地文件系统", mode, name, name, setting)
				}
			}
		}
	}
}

// TestGovWithholdsConfig：配置文件内嵌原始主机名
// （macros、remote_servers、zookeeper），因此 gov 绝不能采集它们——
// 无论 mode 的大小写如何，这正是 -mode GOV 绕过事件的教训。
func TestGovWithholdsConfig(t *testing.T) {
	for _, mode := range []string{"gov", "GOV", " Gov ", "gOV"} {
		if !govWithholdsConfig(mode) {
			t.Errorf("govWithholdsConfig(%q) = false；gov 绝不能采集配置", mode)
		}
	}
	for _, mode := range []string{"onprem", "cloud", "ONPREM", ""} {
		if govWithholdsConfig(mode) {
			t.Errorf("govWithholdsConfig(%q) = true；只有 gov 不采集配置", mode)
		}
	}
}

// TestCloudSkipsConfig：cloud 在服务器之外运行工具，因此没有显式的
// -config-dir 就没有可读内容，交互式提示也不得询问。显式目录则可重新启用；
// 其他 mode 不受影响。
func TestCloudSkipsConfig(t *testing.T) {
	for _, mode := range []string{"cloud", "CLOUD", " Cloud "} {
		if !cloudSkipsConfig(mode, "") {
			t.Errorf("cloudSkipsConfig(%q, \"\") = false；cloud 默认没有本地配置目录", mode)
		}
		if cloudSkipsConfig(mode, "/etc/clickhouse-server/config.d/") {
			t.Errorf("cloudSkipsConfig(%q, explicit dir) = true；显式的 -config-dir 必须被遵守", mode)
		}
	}
	for _, mode := range []string{"onprem", "gov", "ONPREM", ""} {
		if cloudSkipsConfig(mode, "") {
			t.Errorf("cloudSkipsConfig(%q, \"\") = true；只有 cloud 默认跳过", mode)
		}
	}
}
