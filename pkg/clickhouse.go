package pkg

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"time"
)

// DefaultQueryTimeout 在服务端限制每条查询。它刻意
// 低于 HTTP 客户端超时：当服务器执行该限制时，
// 查询会以干净的 Code: 159 失败并落入客户的
// query_log，而客户端超时只会断开连接，
// 让服务器继续执行。
const DefaultQueryTimeout = 240 * time.Second

// clientTimeoutMargin 是 HTTP 客户端比服务端限制多等的时间，
// 使服务器总能赢得竞争，我们得到真实错误，
// 而不是静默断开。
const clientTimeoutMargin = 60 * time.Second

// ClickHouseClient 表示一个 ClickHouse HTTP 客户端
type ClickHouseClient struct {
	protocol   string
	host       string
	port       string
	username   string
	password   string
	httpClient *http.Client

	// queryTimeout 作为 max_execution_time 随每个请求发送。零
	// 表示无服务端限制，此时 HTTP 客户端也保持无上限，
	// 使两者保持一致。
	queryTimeout time.Duration
	// margin 是 HTTP 客户端比 queryTimeout 多等的时间。
	margin time.Duration

	// dry-run 状态。dryRun 为 true 时，ExecuteQuery 会把 SQL
	// 及其将访问的 system 表打印到 dryOut，然后返回
	// 空结果而不联系服务器。dryEstimate 会增加一次
	// 真实的 EXPLAIN ESTIMATE 往返（只读、仅元数据），
	// 使运维人员无需执行查询本身就能看到扫描量。
	//
	// ExecuteQueryReal 绕过此拦截——用于
	// 版本探测、查询分析预检（以便取得真实的
	// 派生值模板到后续 SQL），以及
	// EXPLAIN ESTIMATE 往返本身。
	dryRun      bool
	dryOut      io.Writer
	dryEstimate bool
	dryCounter  int

	// settings 是在固定设置（SetSetting）之后随每个请求发送的
	// 额外查询设置，按首次设置的顺序排列。
	settings []setting
}

type setting struct{ name, value string }

// RemoveSetting 移除通过 SetSetting 添加的设置（不存在时为无操作）。
func (c *ClickHouseClient) RemoveSetting(name string) {
	for i := range c.settings {
		if c.settings[i].name == name {
			c.settings = append(c.settings[:i], c.settings[i+1:]...)
			return
		}
	}
}

// SetSetting 从此刻起向每个请求添加一个查询设置，若已设置
// 则替换其值。与固定设置一样，它由服务器连同 readonly=1
// 一起应用，因此只有 readonly profile 允许更改的设置
// 才应放在这里。
func (c *ClickHouseClient) SetSetting(name, value string) {
	for i := range c.settings {
		if c.settings[i].name == name {
			c.settings[i].value = value
			return
		}
	}
	c.settings = append(c.settings, setting{name, value})
}

// NewClickHouseClient 创建一个新的 ClickHouse 客户端
func NewClickHouseClient(protocol, host, port, username, password string) *ClickHouseClient {
	return &ClickHouseClient{
		protocol:   protocol,
		host:       host,
		port:       port,
		username:   username,
		password:   password,
		httpClient: &http.Client{Timeout: DefaultQueryTimeout + clientTimeoutMargin},
		margin:     clientTimeoutMargin,

		queryTimeout: DefaultQueryTimeout,
	}
}

// SetQueryTimeout 设置服务端 max_execution_time，并同步移动
// HTTP 客户端超时，保持客户端存活时间长于服务端限制的不变量。
// 零时长会同时禁用两者：服务器无上限地执行查询，
// 客户端无限等待，只有在运维人员明确要求时
// 才合理。
func (c *ClickHouseClient) SetQueryTimeout(d time.Duration) {
	c.queryTimeout = d
	if d <= 0 {
		c.queryTimeout = 0
		c.httpClient.Timeout = 0
		return
	}
	c.httpClient.Timeout = d + c.margin
}

// SetClientMargin 更改 HTTP 客户端比服务端限制多等的时间
// （默认 60 s）并应用到当前超时。essential 采集会使用几秒：
// 卡在连接 Keeper 的服务器可能忽略 max_execution_time，此时
// 每个请求一分钟的余量就是每个请求损失一分钟。
func (c *ClickHouseClient) SetClientMargin(d time.Duration) {
	c.margin = d
	c.SetQueryTimeout(c.queryTimeout)
}

// QueryTimeout 是当前服务端每条查询的限制（0 = 无）。
func (c *ClickHouseClient) QueryTimeout() time.Duration { return c.queryTimeout }

// SetDryRun 启用 dry-run 模式。启用期间，ExecuteQuery 不会
// 联系服务器——每条查询都会打印到 out（连同其
// system 表引用），并向调用方返回空结果，
// 使下游解析不会失败。
//
// 如果 explainEstimate 为 true，每个被拦截的 SELECT 还会
// 对服务器触发真实的 `EXPLAIN ESTIMATE <query>`。那是
// 仅元数据的查询——返回查询 WOULD 扫描的行 / mark / part，
// 但不读取任何数据 part。它遵守 readonly=1。
func (c *ClickHouseClient) SetDryRun(out io.Writer, explainEstimate bool) {
	c.dryRun = true
	c.dryOut = out
	c.dryEstimate = explainEstimate
	c.dryCounter = 0
}

// IsDryRun 报告客户端是否处于 dry-run 模式。会打印自身进度消息的
// 调用方（例如查询执行器、分析采集器）用它来抑制
// "successfully saved …" 行，因为这些行出现在 dry-run 输出中
// 会造成误导。
func (c *ClickHouseClient) IsDryRun() bool { return c.dryRun }

// ExecuteQuery 对 ClickHouse 服务器执行查询，或在 dry-run 模式下
// 拦截它。
func (c *ClickHouseClient) ExecuteQuery(query string) (string, error) {
	if c.dryRun {
		return c.dryRunIntercept(query)
	}
	return c.executeReal(query)
}

// ExecuteQueryReal 绕过 dry-run 拦截。用于那些即使在 dry-run 中
// 也必须到达服务器的查询：版本探测（以选择正确的查询变体）、
// 查询分析预检（从 --query-id 推导 normalized_query_hash
// 及反向推导，使打印的 SQL 具有真实值而不是未绑定的
// `{query_id}` 标记），以及 EXPLAIN ESTIMATE 往返本身。
//
// 调用方可以将其用于任何应独立于 dry-run 模式运行的
// 只读元数据获取。
func (c *ClickHouseClient) ExecuteQueryReal(query string) (string, error) {
	return c.executeReal(query)
}

// ExecuteQueryWithFormat 执行查询并以 JSONCompact 格式返回结果。
// 查询绝不能包含 FORMAT 子句。
func (c *ClickHouseClient) ExecuteQueryWithFormat(query string) (string, error) {
	return c.ExecuteQuery(query + "\nFORMAT JSONCompact")
}

// executeReal 是实际的 HTTP 往返。绕过 dry-run，因此可以从
// dryRunIntercept 内部使用（进行 EXPLAIN ESTIMATE
// 元数据获取）而不会递归。
func (c *ClickHouseClient) executeReal(query string) (string, error) {
	return c.executeWith(c.httpClient, query)
}

// ExecuteQueryFreshConnection 通过一个随后关闭的全新 TCP/TLS 连接
// 运行查询，绕过共享客户端的 keep-alive 连接池。
//
// 为节点身份探测而存在。默认 transport 会保持到服务器的
// 连接存活，而负载均衡器会把连接固定到一个后端——
// 因此在三副本云端点上，通过共享客户端进行的三次 hostName() 探测
// 都回答为同一个 replica，而普通 curl（每次调用一个连接）
// 显示它在轮转。只有每次探测使用新连接才能看到
// 均衡器。不适用于采集器：每条查询一次新 TLS 握手
// 的代价会高于查询本身。
func (c *ClickHouseClient) ExecuteQueryFreshConnection(query string) (string, error) {
	if c.dryRun {
		return c.dryRunIntercept(query)
	}
	return c.executeWith(freshConnectionClient(c.httpClient.Timeout), query)
}

// probeTimeout 限制一次新连接探测。这些探测是启动时
// 顺序运行的简单查询，因此它们有自己的简短总体上限，
// 而不是采集器的查询超时——后者可能无上限
// （-query-timeout 0），会让一个接受连接后卡住的后端
// 无限期拖住整个运行。
const probeTimeout = 30 * time.Second

// freshConnectionClient 是一个 http.Client，其 transport 每个请求
// 打开一个连接且从不复用。从默认 transport 克隆而来，
// 使其他每个请求拥有的连接级限制——30 s 拨号超时和
// 10 s TLS 握手超时——继续生效；零值 Transport 会丢掉
// 这两者。HTTP/2 保持关闭：多路复用的 h2 连接正是
// 探测必须避免的复用。
func freshConnectionClient(clientTimeout time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DisableKeepAlives = true
	tr.ForceAttemptHTTP2 = false
	tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	timeout := probeTimeout
	if clientTimeout > 0 && clientTimeout < timeout {
		timeout = clientTimeout
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}

// executeWith 是对给定客户端执行的 HTTP 往返；executeReal
// 传入共享 keep-alive 客户端，ExecuteQueryFreshConnection 传入
// 一次性客户端。
func (c *ClickHouseClient) executeWith(httpClient *http.Client, query string) (string, error) {
	// 构建带 readonly 设置的 URL，以防止写操作。
	//
	// output_format_json_quote_64bit_integers=1 被固定，而不是留给
	// 服务器默认值。JSON 数字在大多数解析器中是 IEEE-754 double，
	// 因此未加引号的、超过 2^53 的 UInt64 在读取时会被静默舍入——
	// JavaScript 会把 18446744073709551615 变成
	// 18446744073709552000，任何做算术的 jq 表达式也一样。
	// 真实列会碰到这一点：normalized_query_hash 和每个
	// cityHash64 值都是满宽度 UInt64。该设置在 readonly=1 下可写，
	// 因此用户 profile 可以关掉它；固定它才是
	// 保证成立的原因。带引号的整数在每个解析器中都精确，
	// dashboard 也已预期这种形式（parseUInt64）。
	//
	// max_execution_time 在 SERVER 端限制查询。没有它，
	// 唯一的限制就是 HTTP 客户端超时，而两者没有任何
	// 关系：客户端放弃时，Go 关闭连接并转向下一条查询，
	// 而服务器仍在执行。在慢集群上这会累积被放弃的查询，
	// 每条仍在消耗 CPU 和内存，而采集器又叠加更多。
	//
	// cancel_http_readonly_queries_on_client_close 让服务器在我们断开时
	// 丢弃查询。它只在 readonly>0 时生效，
	// 因此应与 readonly=1 放在一起。注意：目前它在 HTTPS 上
	// 不会触发——在优雅关闭时，服务器的存活探测会看到 TLS
	// close_notify 字节，并断定对端仍然存在
	// （ClickHouse/ClickHouse#96737）。因此它在纯 http 上有帮助，
	// 而 max_execution_time 仍是我们实际依赖的限制。
	//
	// enable_http_compression 让服务器 gzip 压缩响应。Go 的
	// transport 已经发送 Accept-Encoding: gzip 并透明解压——
	// 但只在我们自己不设置该头部时。如果有人手动添加它，
	// 透明解压会关闭，原始 gzip 会落入输出文件。
	//
	// 所有这些都由服务器作为一批设置变更连同 readonly=1
	// 一起应用，因此把 readonly 列在前面不会阻止它们
	// （src/Server/HTTPHandler.cpp 构建单个
	// SettingsChanges 并只检查一次约束）。
	maxExecSeconds := int64((c.queryTimeout + time.Second - 1) / time.Second) // 向上取整以避免截断
	url := fmt.Sprintf("%s://%s:%s/?readonly=1&output_format_json_quote_64bit_integers=1"+
		"&max_execution_time=%d&cancel_http_readonly_queries_on_client_close=1&enable_http_compression=1",
		c.protocol, c.host, c.port, maxExecSeconds)
	for _, st := range c.settings {
		url += "&" + neturl.QueryEscape(st.name) + "=" + neturl.QueryEscape(st.value)
	}

	// 创建请求
	req, err := http.NewRequest("POST", url, bytes.NewBufferString(query))
	if err != nil {
		return "", fmt.Errorf("创建请求失败：%w", err)
	}

	// 设置头部
	req.Header.Set("Content-Type", "text/plain")

	// 添加 Basic 认证
	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	// 执行请求
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("执行请求失败：%w", err)
	}
	defer resp.Body.Close()

	// 检查响应状态
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP 状态非 200：%d，响应体：%s", resp.StatusCode, body)
	}

	// 读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取响应失败：%w", err)
	}

	return string(body), nil
}

// dryRunIntercept 打印将要发送的查询，并（如果已配置）
// 为其返回真实的 EXPLAIN ESTIMATE。返回的有效载荷是空结果，
// 使尝试解析响应的调用方不会崩溃。
func (c *ClickHouseClient) dryRunIntercept(query string) (string, error) {
	c.dryCounter++
	tables := ExtractTables(query)
	tablesStr := "(no system.* references — function-only)"
	if len(tables) > 0 {
		tablesStr = strings.Join(tables, ", ")
	}

	fmt.Fprintf(c.dryOut, "\n[%d]\n", c.dryCounter)
	fmt.Fprintf(c.dryOut, "    Tables: %s\n", tablesStr)
	fmt.Fprintf(c.dryOut, "    SQL:\n%s\n", indentBlock(query, "      "))

	if c.dryEstimate && isExplainable(query) {
		// 追加 `FORMAT PrettyCompactMonoBlock`，使结果渲染为
		// 运维人员期望的框线表格：
		//   ┌─database─┬─table─┬─parts─┬─rows─┬─marks─┐
		//   │ default  │ ttt   │     1 │  128 │     8 │
		//   └──────────┴───────┴───────┴──────┴───────┘
		// 没有显式 FORMAT 时，HTTP 默认是 TabSeparated，
		// 会打印 `default\tttt\t1\t128\t8` 且无表头——
		// 即使估算有数据，一眼看去也像“空”。PrettyCompactMonoBlock
		// 对于没有 MergeTree part 可估算的 system.* 查询，
		// 也会渲染空表（仅表头 + 分隔线），使运维人员
		// 明确看到“没有 part”，而不是一行空白。
		est, err := c.executeReal("EXPLAIN ESTIMATE " + stripTrailingFormat(query) + " FORMAT PrettyCompactMonoBlock")
		switch {
		case err != nil:
			fmt.Fprintf(c.dryOut, "    EXPLAIN ESTIMATE: (error: %v)\n", err)
		case isEmptyEstimate(est):
			// 表头 + 分隔线但没有数据行——规划器没有找到
			// 可扫描的内容。最常见的是虚拟 system 表
			// （例如 system.dictionaries）或谓词裁掉所有 part 的
			// 查询。明确说明胜过空白框。
			fmt.Fprintln(c.dryOut, "    EXPLAIN ESTIMATE: this table is empty")
		default:
			fmt.Fprintf(c.dryOut, "    EXPLAIN ESTIMATE:\n%s\n", indentBlock(strings.TrimRight(est, "\n"), "      "))
		}
	}
	return emptyResponseFor(query), nil
}

// isEmptyEstimate 报告 PrettyCompactMonoBlock 格式的
// EXPLAIN ESTIMATE 结果是否不含数据行。没有匹配时，规划器
// 只返回顶部的 "┌──┐" 和底部的 "└──┘" 分隔线。
// 数据行总是带 "│" 主体，因此任何（去除前导空白后）
// 以 "│" 开头的行都意味着有内容。
func isEmptyEstimate(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimLeft(line, " \t\r\n0123456789.")
		if strings.HasPrefix(t, "│") {
			return false
		}
	}
	return true
}

// indentBlock 为 s 的每一行加上 prefix。
func indentBlock(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// isExplainable 在查询是 EXPLAIN ESTIMATE 能分析的 SELECT 或 WITH
// 时返回 true。先剥离开头的 SQL 行注释和块注释，
// 因为 queries.query_analysis/ 中的许多 .sql 文件在
// SELECT 上方以多行 `-- description` 开头——
// 不剥离注释的话，isExplainable 会把 `--` 当作第一个 token，
// 从而完全跳过 EXPLAIN ESTIMATE。
func isExplainable(query string) bool {
	trimmed := stripLeadingSQLComments(query)
	trimmed = strings.TrimLeft(strings.TrimSpace(trimmed), "(")
	up := strings.ToUpper(trimmed)
	return strings.HasPrefix(up, "SELECT") || strings.HasPrefix(up, "WITH")
}

// stripLeadingSQLComments 从 s 中移除任何前导空白、`-- line`
// 注释和 `/* block */` 注释——即第一个真实 SQL 关键词之前的
// 所有内容。isExplainable 使用它，以便
// 正确识别头部被文档注释包裹的 SELECT/WITH 查询。
func stripLeadingSQLComments(s string) string {
	for {
		s = strings.TrimLeft(s, " \t\r\n")
		switch {
		case strings.HasPrefix(s, "--"):
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = s[i+1:]
				continue
			}
			return ""
		case strings.HasPrefix(s, "/*"):
			if i := strings.Index(s, "*/"); i >= 0 {
				s = s[i+2:]
				continue
			}
			return ""
		default:
			return s
		}
	}
}

// stripTrailingFormat 移除末尾的 `FORMAT <name>` 子句，
// 使 EXPLAIN ESTIMATE 能干净地为查询加前缀。
func stripTrailingFormat(query string) string {
	t := strings.TrimRight(query, " \t\r\n;")
	idx := strings.LastIndex(strings.ToUpper(t), "FORMAT")
	if idx == -1 {
		return t
	}
	tail := strings.TrimSpace(t[idx:])
	parts := strings.Fields(tail)
	if len(parts) == 2 && strings.EqualFold(parts[0], "FORMAT") {
		return strings.TrimRight(t[:idx], " \t\r\n")
	}
	return t
}

// emptyResponseFor 返回与调用方所请求形态匹配的可解析空结果。
func emptyResponseFor(query string) string {
	if strings.Contains(strings.ToUpper(query), "FORMAT JSONCOMPACT") {
		return `{"meta":[],"data":[],"rows":0}`
	}
	return ""
}

// GetConnectionInfo 返回用于展示的连接信息
func (c *ClickHouseClient) GetConnectionInfo() string {
	return fmt.Sprintf("%s://%s:%s", c.protocol, c.host, c.port)
}
