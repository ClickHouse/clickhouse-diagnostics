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
		`a.setAttribute('aria-current','true')`,    // state, not just colour
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
	if !strings.Contains(htmlTemplate, "nav a.active{color:var(--ink);border-bottom-color:var(--status-info)") {
		t.Error("the active nav link needs its own accent, distinct from :hover")
	}
	if strings.Contains(htmlTemplate, "nav a:hover,nav a.active{") {
		t.Error("hover and active share one rule again — the current section stops being legible")
	}
}
