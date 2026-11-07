package config

import "testing"

// 促成这项功能的场景：ClickHouse 自带的 config.xml 在注释里
// 包含完整的 <zookeeper> 示例，因此保留注释的扫描器
// 会在一台根本没有 Keeper 的服务器上找到三个 Keeper 主机。
func TestStripComments(t *testing.T) {
	const stock = `<clickhouse>
    <logger><log>/var/log/clickhouse-server/clickhouse-server.log</log></logger>
    <!-- ZooKeeper is used to store metadata about replicas.
      -->
    <!--
    <zookeeper>
        <node><host>example1</host><port>2181</port></node>
        <node><host>example2</host><port>2181</port></node>
    </zookeeper>
    -->
    <zookeeper>
        <node><host>keeper-a</host><port>9181</port></node>
    </zookeeper>
</clickhouse>`
	got := string(StripComments([]byte(stock)))
	for _, gone := range []string{"example1", "example2", "ZooKeeper is used"} {
		if contains(got, gone) {
			t.Errorf("注释内容残留：%q", gone)
		}
	}
	for _, kept := range []string{"keeper-a", "clickhouse-server.log"} {
		if !contains(got, kept) {
			t.Errorf("真实配置被移除：%q", kept)
		}
	}
	if got := string(StripComments([]byte("<a>1</a>"))); got != "<a>1</a>" {
		t.Errorf("不含注释的文件必须保持不变，得到 %q", got)
	}
	if got := string(StripComments(nil)); got != "" {
		t.Errorf("nil 输入：得到 %q", got)
	}
}

func contains(h, n string) bool {
	return len(n) > 0 && len(h) >= len(n) && (func() bool {
		for i := 0; i+len(n) <= len(h); i++ {
			if h[i:i+len(n)] == n {
				return true
			}
		}
		return false
	})()
}
