package dashboard

import (
	"strings"
	"testing"
)

// 吸顶导航会高亮你正在阅读的分节。它曾悄无声息地失效：每个可选面板初始都是
// display:none，未渲染的元素报告 getBoundingClientRect().top = 0，而 0 始终
// 高于阈值——于是文档中的最后一个分节在每次遍历中都胜出。在典型 bundle 中
// 那是 sec-async-inserts，它的导航链接也是隐藏的，所以在任何滚动位置都没有
// 任何东西显示为高亮。以下是修复中不得回归的部分。
func TestTemplate_NavScrollSpy(t *testing.T) {
	for _, want := range []string{
		"function spySections(){",
		"s.getClientRects().length", // 只有已渲染的分节才能胜出……
		"navFor['#'+s.id]",          // ……并且只有带导航链接的才算
		"function syncNav(){",
		"const line=topbarH+16;", // 阈值跟随测量出的顶部带
		"let cur=secs[0].id;",    // 页面顶部绝不为空
		"secs[secs.length-1].id", // 底部由最后一个分节胜出
		`window.addEventListener('scroll',syncNav,{passive:true});`,
		`window.addEventListener('resize',syncNav,{passive:true});`,
		`window.addEventListener('load',syncNav);`, // 面板在渲染后取消隐藏
		// 展开 <details> 会在没有滚动或 resize 的情况下移动它下面的每个分节；
		// 遍历必须由内容高度本身重新触发。
		`new ResizeObserver(syncNav).observe(mainEl);`,
		`document.addEventListener('toggle',syncNav,true);`,
		`a.setAttribute('aria-current','true')`, // 状态，而不仅是颜色
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("nav 滚动监听丢失了 %q", want)
		}
	}

	// 列表必须在每次遍历时重建：稍后取消隐藏的面板，或展开后移动下方
	// 所有内容的折叠块，都会改变结果。在初始化时拍一次快照，就是换了个
	// 形态的同一个 bug。
	if strings.Contains(htmlTemplate, "const secs=[...document.querySelectorAll('section[id]')];") {
		t.Error("分节又被一次性快照了——请在 syncNav 内重建")
	}

	// 在 headless 的 --virtual-time-budget 下 requestAnimationFrame 从不触发，
	// 导致这一行为无法测试。保持直接读取。
	spy := htmlTemplate[strings.Index(htmlTemplate, "function spySections(){"):]
	spy = spy[:strings.Index(spy, "// 头部")]
	if strings.Contains(spy, "requestAnimationFrame(") { // 指调用，而不是解释其缺失的注释
		t.Error("滚动监听不得依赖重绘来更新")
	}

	// 当前分节和悬停链接的外观不能相同。
	if !strings.Contains(htmlTemplate, "nav a.active{color:var(--ink);border-left-color:var(--status-info)") {
		t.Error("活动导航链接需要自己的强调色，与 :hover 区分")
	}
	if strings.Contains(htmlTemplate, "nav a:hover,nav a.active{") {
		t.Error("hover 和 active 又共用一条规则——当前分节将无法辨认")
	}
}

// TestTemplate_Sidebar：分节列表是左侧的固定侧栏，header 中有一个箭头用于
// 隐藏和显示它。它取代的横向条在 22 个分节时会溢出视口并横向滚动，
// 使靠后的分节实际上无法访问。以下是必须成立的部分：箭头为辅助技术
// 提供标签并报告其状态，状态在首次绘制前写入并被记住，内容恰好移动
// 侧栏的宽度，并且在高亮链接隐藏后由 header 指明正在阅读的分节。
func TestTemplate_Sidebar(t *testing.T) {
	for _, want := range []string{
		`<button id="nav-toggle" type="button" aria-label="隐藏分节侧栏" aria-controls="main-nav" aria-expanded="true">`,
		`<nav id="main-nav" aria-label="分节">`,
		`<span id="nav-current"></span>`,
		":root{--nav-w:", // 所有偏移都基于这一个宽度；它的值可以自由更改
		"nav{position:fixed;top:var(--topbar-h,74px);left:0;bottom:0;width:var(--nav-w);", // 位于测量出的顶部带下方
		"html.nav-collapsed nav{transform:translateX(-100%);visibility:hidden}",
		"html:not(.nav-collapsed) main{padding-left:calc(var(--nav-w) + var(--click-space-5));", // 内容被推开，而不是被覆盖
		"html.nav-collapsed #nav-toggle svg{transform:scaleX(-1)}",                              // 箭头随状态翻转
		"html.nav-collapsed #nav-current:not(:empty){display:block}",
		`localStorage.getItem("chdiag-nav")`, // 在布局前写入 <head>
		"localStorage.setItem('chdiag-nav',shown?'shown':'collapsed');",
		"toggle.setAttribute('aria-expanded',shown?'true':'false');",
		"@media (max-width:900px){nav{box-shadow:", // 窄屏：改为覆盖而不是推开
		"@media (prefers-reduced-motion:reduce){nav,main,#nav-toggle svg{transition:none}}",
		"@media print{nav,#nav-toggle{display:none}",
		"if(label && name && label.textContent!==name) label.textContent=name;",
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("侧栏导航丢失了 %q", want)
		}
	}
	// 侧栏必须位于吸顶带之外：该带为 --topbar-h 测量，侧栏挂在这个测量值上；
	// 如果放在里面，固定框会被计入带自身的高度。
	top := strings.Index(htmlTemplate, `<div class="topbar">`)
	end := strings.Index(htmlTemplate, `</div><!-- .topbar -->`)
	if top < 0 || end < 0 || strings.Contains(htmlTemplate[top:end], `<nav id="main-nav"`) {
		t.Error("分节侧栏必须是 .topbar 的兄弟节点，而不是在其内部")
	}
	// 旧横向条的 overflow-x:auto 正是它取代的 bug。
	if strings.Contains(htmlTemplate, "nav{background:var(--surface-card);border-bottom") {
		t.Error("横向导航条又回来了")
	}
}
