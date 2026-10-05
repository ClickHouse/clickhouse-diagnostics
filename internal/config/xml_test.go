package config

import "testing"

// The shape that made this necessary: ClickHouse's stock config.xml carries a
// complete <zookeeper> example inside a comment, so a scanner that keeps
// comments finds three Keeper hosts on a server that has none.
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
			t.Errorf("comment content survived: %q", gone)
		}
	}
	for _, kept := range []string{"keeper-a", "clickhouse-server.log"} {
		if !contains(got, kept) {
			t.Errorf("real configuration was removed: %q", kept)
		}
	}
	if got := string(StripComments([]byte("<a>1</a>"))); got != "<a>1</a>" {
		t.Errorf("a file without comments must be unchanged, got %q", got)
	}
	if got := string(StripComments(nil)); got != "" {
		t.Errorf("nil input: got %q", got)
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
