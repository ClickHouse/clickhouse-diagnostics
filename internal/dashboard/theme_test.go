package dashboard

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// dashboard 的主题基于 Click UI token 层。这些测试守护那些建立成本高、却容易被下意识改回去的属性。

func TestTemplate_UsesClickUITokens(t *testing.T) {
	for _, want := range []string{
		"--click-global-color-background-default",
		"--click-global-color-text-muted",
		"--click-global-color-stroke-default",
		`:root[data-cui-theme="dark"]`,
		"prefers-color-scheme:dark",
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("模板丢失了 Click UI token 层：缺少 %q", want)
		}
	}
}

// 下面的每个十六进制值都是 Click UI token，五槽顺序是通过对候选排序运行
// 调色板校验器选出的。手动改动其中任何一个都会使其失效：相邻 CVD dE 20.1，
// 正常视觉 dE 31.3，两种模式均如此。
// 深色 token 声明了两次（OS media query + 显式的 toggle 标记），并且已经漂移过一次：
// --link 只落在 media 块中，因此切换后的深色主题保留了难以辨认的浅色链接颜色。
func TestTemplate_DarkTokenBlocksDoNotDrift(t *testing.T) {
	if got := strings.Count(htmlTemplate, "--link:#A1BEF7"); got != 2 {
		t.Errorf("--link 的深色覆盖必须同时出现在两个深色作用域中（media query + data-cui-theme），实际找到 %d", got)
	}
}

func TestTemplate_ValidatedPalette(t *testing.T) {
	for _, hex := range []string{"#089B83", "#AA00FF", "#B28800", "#CC0099", "#959900"} {
		if !strings.Contains(htmlTemplate, hex) {
			t.Errorf("分类槽位 %s 缺失——更改调色板前请重新运行 validate_palette.js", hex)
		}
	}
	// 这组颜色刻意与模式无关；这正是主题切换可以是一次 Chart.update() 而不是重建的原因。
	if strings.Contains(htmlTemplate, "C[i%C.length]") {
		t.Error("调色板正在循环取色：生成的第六种色相在 CVD 下无法区分——请折叠进 OTHER")
	}
	// Click UI 之前旧调色板遗留的 Material 颜色。
	for _, stale := range []string{"#4CAF50", "#2196F3", "#E91E63", "#f44336", "#FC4F05", "#FFB627", "#9C27B0"} {
		if strings.Contains(htmlTemplate, stale) {
			t.Errorf("非 token 颜色 %s 又出现在模板中", stale)
		}
	}
}

func TestTemplate_NoDualAxisOrPieCharts(t *testing.T) {
	// 一个图上两个 y 轴会任意对齐，并凭空制造出数据中并不存在的相关性。
	if strings.Contains(htmlTemplate, "yAxisID") {
		t.Error("双轴图又回来了：请拆成两个单轴图")
	}
	// 环形图把调色板限制在三个全配对安全的槽位，并隐藏接近的数值。
	for _, bad := range []string{"type:'doughnut'", "type:'pie'"} {
		if strings.Contains(htmlTemplate, bad) {
			t.Errorf("%s 又回来了：比较量级请使用柱状图", bad)
		}
	}
}

// 柱形填充必须是实心的。调色板校验的对比度环节假设标记不透明；
// 带 alpha 的填充在表面上更浅，可能低于 3:1 的下限，还会让网格线透过数据显现。
func TestTemplate_BarFillsAreOpaque(t *testing.T) {
	for _, bad := range []string{"alpha(C[0]", "alpha(C[1]", "alpha(C[2]", "alpha(STATUS."} {
		if strings.Contains(htmlTemplate, bad) {
			t.Errorf("半透明标记填充 %q——调色板的校验基于实心十六进制色", bad)
		}
	}
}

// hash 是读者想从这些面板中复制出来的东西——它是你用来 grep query_log 的内容——
// 因此绝不能为了显示而截断。
func TestTemplate_ShowsFullQueryHash(t *testing.T) {
	if strings.Contains(htmlTemplate, "shortHash") {
		t.Error("hash 又被截断了；请显示完整的 normalized_query_hash")
	}
	if !strings.Contains(htmlTemplate, "function fullHash(") {
		t.Error("缺少 fullHash 辅助函数")
	}
	// SQL 未知时，标签必须回退到完整的 hash。
	if !strings.Contains(htmlTemplate, "return fullHash(r.hash)") {
		t.Error("sample_query 缺失时 queryLabel 必须回退到完整 hash")
	}
	for _, want := range []string{"function queryLabel(", "function bindQueryPeek(", "peek-slow-queries", "peek-heavy-reads"} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("查询文本入口缺失：%q", want)
		}
	}
}

// normalized_query_hash 是 UInt64。作为 JSON 数字发送时会被 JavaScript
// 四舍五入超过 2^53，显示的 hash 会悄无声息地出错。
func TestQueryPanelSQL_HashIsAString(t *testing.T) {
	for name, sql := range map[string]string{
		"querySlowSQL":  (&Generator{mode: "onprem"}).querySlowSQL(),
		"queryHeavySQL": (&Generator{mode: "onprem"}).queryHeavySQL(),
	} {
		if !strings.Contains(sql, "toString(normalized_query_hash) AS hash") {
			t.Errorf("%s 必须把 hash 转为字符串，否则 JS 会对其进行四舍五入：\n%s", name, sql)
		}
		if !strings.Contains(sql, "AS sample_query") {
			t.Errorf("%s 应携带具有代表性的查询文本", name)
		}
	}

	// query_heavy 聚合开销，因此必须只统计 QueryFinish，否则每条
	// QueryStart 行（read_bytes=0）都会稀释平均值和排名。
	heavy := (&Generator{mode: "onprem"}).queryHeavySQL()
	if !strings.Contains(heavy, "AND type = 'QueryFinish'") {
		t.Error("queryHeavySQL 必须过滤到 QueryFinish，否则 QueryStart 行会稀释每个平均值")
	}

	// query_slow 统计错误，因此异常行必须在其窗口内——
	// 失败永远不会被记录为 QueryFinish（与 queryByUserSQL 曾有过的同类缺陷）。
	slow := (&Generator{mode: "onprem"}).querySlowSQL()
	for _, want := range []string{"ExceptionWhileProcessing", "ExceptionBeforeStart",
		"countIf(type = 'QueryFinish') AS executions"} {
		if !strings.Contains(slow, want) {
			t.Errorf("querySlowSQL 缺少 %q——其错误列在结构上将永远是 0", want)
		}
	}
	if strings.Contains(slow, "avgIf(") {
		t.Error("querySlowSQL 必须使用 sum/greatest，而不是 avgIf：对全失败形态，avgIf 会得到 nan")
	}
}

// gov 从不发布 dashboard（见 cmd.dashboardDecision），但脱敏仍然保留作为
// 纵深防御：query_log.query 是针对 queries.gov/*.sql 所哈希的那些名称的原始 SQL。
func TestSampleQueryCol_GovIsRedacted(t *testing.T) {
	if got := (&Generator{mode: "gov"}).sampleQueryCol(); !strings.HasPrefix(got, "''") {
		t.Errorf("gov 不得选择原始查询文本，得到 %q", got)
	}
	if got := (&Generator{mode: "onprem"}).sampleQueryCol(); !strings.Contains(got, "any(query)") {
		t.Errorf("非 gov 应采样查询文本，得到 %q", got)
	}
}

// header 作为一条测量出的带整体吸顶。
//
// 以前 header 和 nav 分别吸顶，nav 固定在一个必须等于 header 高度的硬编码
// top:53px 上。但实际并不相等——header 实测约 74px——所以页面一旦滚动，
// header 就盖住 nav 顶部 21px，标签被切成两半。三个独立常量都在猜同一高度
// （nav 顶部、分节 scroll-margin、滚动监听阈值）；现在三者都源自一个测量值。
// 此后 nav 变成了一个固定侧栏（见 TestTemplate_Sidebar），挂在同一个测量值上，
// 锚点偏移和滚动监听阈值仍然跟随该测量出的带高度。
func TestTemplate_TopbarSticksAsOneBand(t *testing.T) {
	if !strings.Contains(htmlTemplate, ".topbar{position:sticky;top:0;") {
		t.Error("header 必须包裹在一个吸顶的 .topbar 中")
	}
	if !strings.Contains(htmlTemplate, `<div class="topbar">`) {
		t.Error("标记中缺少 .topbar 包裹层")
	}

	// 任何组件都不得重新引入针对该带高度的硬编码偏移。
	for _, banned := range []string{"top:53px", "top:57px", "scroll-margin-top:110px", "top<=130"} {
		// 跳过说明性注释，它有意提到了旧值。
		for _, line := range strings.Split(htmlTemplate, "\n") {
			if strings.Contains(line, banned) && !strings.Contains(line, "hardcoded") {
				t.Errorf("硬编码的带偏移 %q 又回来了：%q", banned, strings.TrimSpace(line))
			}
		}
	}

	// 锚点偏移和滚动监听都读取测量出的高度。
	if !strings.Contains(htmlTemplate, "scroll-margin-top:calc(var(--topbar-h") {
		t.Error("分节 scroll-margin-top 必须源自 --topbar-h")
	}
	// 通过观察来测量，而不是测一次：该带会在初始化后增长（hdr-meta 的第二行、
	// 可选导航链接取消隐藏、webfont 替换）并在 resize 时变化。
	if !strings.Contains(htmlTemplate, "new ResizeObserver(measureTopbar)") {
		t.Error("--topbar-h 必须通过 ResizeObserver 重新测量，而不是只设置一次")
	}
	// 阈值是测量出的高度加一个小容差：锚点跳转会把标题正好落在
	// scroll-margin-top，那里的亚像素舍入不得翻转比较结果。余量值可以自由
	// 更改；但从 topbarH 推导它不可以改。
	if !strings.Contains(htmlTemplate, "const line=topbarH+") {
		t.Error("滚动监听阈值必须跟随测量出的带高度")
	}
}

// 模板默认会转义，而这三处曾没有转义。它们都不可利用——mode 是经过校验的
// CLI 枚举，每个 tile() 调用方传入的都是数字或版本字符串——但约定的价值只在
// 于执行它的东西，而 host notes 确实是由 OS 错误和路径组成的自由文本。
//
// 以精确的插值形态来断言，而不是用 "contains esc("，因为失败模式是未来的
// 某次编辑从其中一处删掉 esc()，而另外两处保留调用、文件仍然能通过 grep。
func TestTemplate_InterpolatesThroughEsc(t *testing.T) {
	for _, want := range []string{
		// header 徽章——mode chip
		`'<span class="badge badge-'+esc(DATA.mode)+'">'+esc(DATA.mode)+'</span>'`,
		// stat tile 辅助函数——在辅助函数内转义，而不是每个调用点
		`+'">'+esc(v)+'</div><div class="lbl">'+esc(l)+'</div></div>'`,
		// host facts 备注——自由文本，拼接前逐个元素转义
		`notes.map(esc).join('; ')`,
		// query-analysis 窗口标签——目前是 Go 格式化的 RFC3339，但
		// 约定在汇点成立，而不是取决于谁恰好填充了 DATA
		`>窗口 '+esc(DATA.qa_from||'')+' → '+esc(DATA.qa_to||'')+'</span></span>'`,
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("innerHTML 位置不再转义其插值：缺少 %q", want)
		}
	}

	// 原始形式必须消失，而不仅仅是被转义后的孪生形式一并存在。
	for _, forbidden := range []string{
		"+DATA.mode+",
		"+'\">'+v+'</div>",
		"notes.join('; ')",
		"+(DATA.qa_from||'')+",
	} {
		if strings.Contains(htmlTemplate, forbidden) {
			t.Errorf("未转义的插值又回来了：%q", forbidden)
		}
	}
}

// dashboard 在 Go 中自行构建 SQL，因此 collector 的 leftUTF8 防护
// （internal/query/archive_shape_test.go）覆盖不到它——而这个分支上五个截断
// 位置中有三个就在这里，而不是在 .sql 文件中。
//
// left() 按字节计数，因此在多字节字符内部截断会留下无效 UTF-8。json.Marshal
// 会把它降级为 U+FFFD 而不是破坏页面，这正是它需要测试的原因：这里的损害是
// 无声的，而把同样的表达式粘贴进 collector 会直接损坏归档。
func TestGeneratorSQL_TruncatesWithLeftUTF8(t *testing.T) {
	src, err := os.ReadFile("generator.go")
	if err != nil {
		t.Fatalf("读取 generator.go：%v", err)
	}
	// 先去掉所有 leftUTF8(："left" 是 "leftUTF8" 的前缀，因此仅靠单词边界会
	// 同时匹配两者。然后剥离注释——这些行本身就在讨论 left() 与 leftUTF8()，
	// 不能被当成调用点。
	bare := regexp.MustCompile(`\bleft\s*\(`)
	for i, line := range strings.Split(string(src), "\n") {
		if j := strings.Index(line, "//"); j >= 0 {
			line = line[:j]
		}
		if bare.MatchString(strings.ReplaceAll(line, "leftUTF8(", "")) {
			t.Errorf("generator.go:%d 使用按字节的 left() 截断——请使用 leftUTF8()：%q",
				i+1, strings.TrimSpace(line))
		}
	}
}

// 评审发现：模板 head 中带有两个 DOCTYPE 声明。浏览器能容忍，但按 HTML 规范
// 第二个是解析错误，也是严格的校验器或下游转换器会踩到的问题。
func TestTemplate_SingleDoctype(t *testing.T) {
	for name, tpl := range map[string]string{"dashboard": htmlTemplate, "schema graph": schemaGraphHead} {
		if n := strings.Count(tpl, "<!DOCTYPE"); n != 1 {
			t.Errorf("%s 模板有 %d 个 DOCTYPE 声明，期望 1 个", name, n)
		}
	}
}
