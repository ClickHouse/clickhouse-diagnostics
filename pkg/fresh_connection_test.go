package pkg

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// 负载均衡器会把 keep-alive 连接固定到一个后端，因此想要
// 看到均衡器的探测必须每次都打开新连接。
// 下面的服务器会以客户端的临时端口作答——复用连接时
// 相同，新连接时不同——这正是共享客户端与
// ExecuteQueryFreshConnection 的区别。
func TestExecuteQueryFreshConnection_OpensANewConnectionPerCall(t *testing.T) {
	var mu sync.Mutex
	var remotes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		remotes = append(remotes, r.RemoteAddr)
		mu.Unlock()
		_, _ = w.Write([]byte(r.RemoteAddr + "\n"))
	}))
	defer srv.Close()
	hostPort := strings.TrimPrefix(srv.URL, "http://")
	host, port, _ := strings.Cut(hostPort, ":")
	c := NewClickHouseClient("http", host, port, "", "")

	// 共享客户端：keep-alive 会复用连接，因此服务器三次调用只看到
	// 一个远程地址——这正是掩盖均衡器的行为。
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		out, err := c.ExecuteQuery("SELECT hostName()")
		if err != nil {
			t.Fatal(err)
		}
		seen[strings.TrimSpace(out)] = true
	}
	if len(seen) != 1 {
		t.Fatalf("共享客户端应复用一条连接，却看到 %d 个远程地址：%v", len(seen), seen)
	}

	// 每次调用使用新连接：三个不同的临时端口。
	seen = map[string]bool{}
	for i := 0; i < 3; i++ {
		out, err := c.ExecuteQueryFreshConnection("SELECT hostName()")
		if err != nil {
			t.Fatal(err)
		}
		seen[strings.TrimSpace(out)] = true
	}
	if len(seen) != 3 {
		t.Fatalf("新连接探测绝不能共享连接，却看到 %d 个远程地址：%v", len(seen), seen)
	}
}

// 一次性 transport 必须保留默认 transport 的连接级限制
// （零值 Transport 既没有拨号超时也没有 TLS 握手超时），
// 保持 HTTP/1.1（h2 会多路复用——正是探测要避免的复用），
// 并且绝不超出探测上限，即使采集器的查询超时
// 无上限。
func TestFreshConnectionClient_KeepsTransportBounds(t *testing.T) {
	c := freshConnectionClient(0)
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatal("transport 不是 *http.Transport")
	}
	if !tr.DisableKeepAlives {
		t.Error("必须禁用 keep-alive")
	}
	if tr.DialContext == nil || tr.TLSHandshakeTimeout == 0 {
		t.Error("拨号和 TLS 握手限制必须来自默认 transport")
	}
	if tr.ForceAttemptHTTP2 || tr.TLSNextProto == nil || len(tr.TLSNextProto) != 0 {
		t.Error("一次性探测必须禁用 HTTP/2")
	}
	if c.Timeout != probeTimeout {
		t.Errorf("无上限的客户端超时必须回退到 probeTimeout，得到 %v", c.Timeout)
	}
	if got := freshConnectionClient(5 * time.Second).Timeout; got != 5*time.Second {
		t.Errorf("更短的客户端超时必须优先，得到 %v", got)
	}
	if got := freshConnectionClient(10 * time.Minute).Timeout; got != probeTimeout {
		t.Errorf("更长的客户端超时必须被限制为 probeTimeout，得到 %v", got)
	}
}
