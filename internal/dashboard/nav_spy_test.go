package dashboard

import (
	"strings"
	"testing"
)

// The sticky nav highlights the section you are reading. It silently stopped
// doing that: every optional panel starts at display:none, a non-rendered
// element reports getBoundingClientRect().top = 0, and 0 is always above the
// threshold — so the LAST section in the document won every pass. On a
// typical bundle that is sec-async-inserts, whose nav link is hidden too, so
// nothing appeared highlighted at any scroll position. These are the parts
// of the fix that must not regress.
func TestTemplate_NavScrollSpy(t *testing.T) {
	for _, want := range []string{
		"function spySections(){",
		"s.getClientRects().length", // only rendered sections may win…
		"navFor['#'+s.id]",          // …and only those with a nav link
		"function syncNav(){",
		"const line=topbarH+16;", // threshold follows the measured band
		"let cur=secs[0].id;",    // never blank at the top of the page
		"secs[secs.length-1].id", // last section wins at the bottom
		`window.addEventListener('scroll',syncNav,{passive:true});`,
		`window.addEventListener('resize',syncNav,{passive:true});`,
		`window.addEventListener('load',syncNav);`, // panels un-hide after render
		// Opening a <details> moves every section below it without a scroll or
		// a resize; the pass must be re-triggered by the content height itself.
		`new ResizeObserver(syncNav).observe(mainEl);`,
		`document.addEventListener('toggle',syncNav,true);`,
		`a.setAttribute('aria-current','true')`, // state, not just colour
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("nav scroll spy lost %q", want)
		}
	}

	// The list must be rebuilt per pass: a panel that un-hides later, or a
	// disclosure that expands and shifts everything below it, changes the
	// answer. A snapshot taken once at init is the bug in a new shape.
	if strings.Contains(htmlTemplate, "const secs=[...document.querySelectorAll('section[id]')];") {
		t.Error("sections are snapshotted once again — rebuild them inside syncNav")
	}

	// requestAnimationFrame never fires under a headless --virtual-time-budget,
	// which made this behaviour impossible to test. Keep the read direct.
	spy := htmlTemplate[strings.Index(htmlTemplate, "function spySections(){"):]
	spy = spy[:strings.Index(spy, "// header")]
	if strings.Contains(spy, "requestAnimationFrame(") { // the call, not the comment explaining its absence
		t.Error("the scroll spy must not depend on a repaint to update")
	}

	// Current section and hovered link must not look identical.
	if !strings.Contains(htmlTemplate, "nav a.active{color:var(--ink);border-left-color:var(--status-info)") {
		t.Error("the active nav link needs its own accent, distinct from :hover")
	}
	if strings.Contains(htmlTemplate, "nav a:hover,nav a.active{") {
		t.Error("hover and active share one rule again — the current section stops being legible")
	}
}

// TestTemplate_Sidebar: the section list is a fixed sidebar on the left with
// an arrow in the header to hide and show it. The horizontal strip it
// replaced overflowed the viewport at 22 sections and scrolled sideways,
// which made the trailing sections effectively unreachable. These are the
// parts that must hold: the arrow is labelled for assistive tech and reports
// its state, the state is stamped before first paint and remembered, the
// content moves over by exactly the sidebar's width, and the header names
// the section being read once the highlighted link is hidden.
func TestTemplate_Sidebar(t *testing.T) {
	for _, want := range []string{
		`<button id="nav-toggle" type="button" aria-label="Hide section sidebar" aria-controls="main-nav" aria-expanded="true">`,
		`<nav id="main-nav" aria-label="Sections">`,
		`<span id="nav-current"></span>`,
		":root{--nav-w:", // the one width everything is offset by; its value is free to change
		"nav{position:fixed;top:var(--topbar-h,74px);left:0;bottom:0;width:var(--nav-w);", // under the measured band
		"html.nav-collapsed nav{transform:translateX(-100%);visibility:hidden}",
		"html:not(.nav-collapsed) main{padding-left:calc(var(--nav-w) + var(--click-space-5));", // content pushed, not covered
		"html.nav-collapsed #nav-toggle svg{transform:scaleX(-1)}",                              // the arrow flips with the state
		"html.nav-collapsed #nav-current:not(:empty){display:block}",
		`localStorage.getItem("chdiag-nav")`, // stamped in <head>, before layout
		"localStorage.setItem('chdiag-nav',shown?'shown':'collapsed');",
		"toggle.setAttribute('aria-expanded',shown?'true':'false');",
		"@media (max-width:900px){nav{box-shadow:", // narrow: overlay instead of push
		"@media (prefers-reduced-motion:reduce){nav,main,#nav-toggle svg{transition:none}}",
		"@media print{nav,#nav-toggle{display:none}",
		"if(label && name && label.textContent!==name) label.textContent=name;",
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("sidebar nav lost %q", want)
		}
	}
	// The sidebar must sit outside the sticky band: the band is measured for
	// --topbar-h and the sidebar hangs from that measurement; inside it the
	// fixed box would be counted in the band's own height.
	top := strings.Index(htmlTemplate, `<div class="topbar">`)
	end := strings.Index(htmlTemplate, `</div><!-- .topbar -->`)
	if top < 0 || end < 0 || strings.Contains(htmlTemplate[top:end], `<nav id="main-nav"`) {
		t.Error("the section sidebar must be a sibling of .topbar, not inside it")
	}
	// The old strip's overflow-x:auto is the bug this replaced.
	if strings.Contains(htmlTemplate, "nav{background:var(--surface-card);border-bottom") {
		t.Error("the horizontal nav strip is back")
	}
}
