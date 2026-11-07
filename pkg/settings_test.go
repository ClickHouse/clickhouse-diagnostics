package pkg

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 额外设置与 readonly=1 一起、经过转义地随每个请求发送，
// 而固定设置仍然存在——为某个目的添加的设置（这里是
// skip_unavailable_shards，使一个宕机的 replica 不再让每个
// clusterAllReplicas 采集器失败）绝不能丢弃其他设置。
func TestSetSetting_SentOnEveryRequest(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		_, _ = w.Write([]byte("1\n"))
	}))
	defer srv.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	c := NewClickHouseClient("http", host, port, "", "")
	c.SetSetting("skip_unavailable_shards", "1")
	c.SetSetting("log_comment", "a b&c")
	c.SetSetting("skip_unavailable_shards", "1") // 幂等：设置，而不是追加两次
	for i := 0; i < 2; i++ {
		if _, err := c.ExecuteQuery("SELECT 1"); err != nil {
			t.Fatal(err)
		}
	}
	if len(queries) != 2 {
		t.Fatalf("请求数 = %d，期望 2", len(queries))
	}
	for _, q := range queries {
		for _, want := range []string{"readonly=1", "max_execution_time=", "skip_unavailable_shards=1", "log_comment=a+b%26c"} {
			if !strings.Contains(q, want) {
				t.Errorf("请求 %q 缺少 %q", q, want)
			}
		}
		if strings.Count(q, "skip_unavailable_shards=") != 1 {
			t.Errorf("设置 %q 中重复出现", q)
		}
	}
}

func TestRemoveSetting(t *testing.T) {
	c := NewClickHouseClient("http", "h", "1", "", "")
	c.SetSetting("max_threads", "2")
	c.SetSetting("priority", "10")
	c.RemoveSetting("max_threads")
	c.RemoveSetting("absent")
	if len(c.settings) != 1 || c.settings[0].name != "priority" {
		t.Fatalf("移除后的设置：%+v", c.settings)
	}
}

func TestSetClientMargin(t *testing.T) {
	c := NewClickHouseClient("http", "h", "1", "", "")
	c.SetQueryTimeout(30 * time.Second)
	if c.httpClient.Timeout != 90*time.Second {
		t.Fatalf("默认余量：%v", c.httpClient.Timeout)
	}
	c.SetClientMargin(5 * time.Second)
	if c.httpClient.Timeout != 35*time.Second {
		t.Fatalf("余量未应用：%v", c.httpClient.Timeout)
	}
	c.SetQueryTimeout(10 * time.Second)
	if c.httpClient.Timeout != 15*time.Second {
		t.Fatalf("余量未保留：%v", c.httpClient.Timeout)
	}
}
