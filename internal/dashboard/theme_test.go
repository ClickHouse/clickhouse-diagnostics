package dashboard

import (
	"strings"
	"testing"
)

// The dashboard is themed off the Click UI token layer. These guard the
// properties that were expensive to establish and are easy to undo by reflex.

func TestTemplate_UsesClickUITokens(t *testing.T) {
	for _, want := range []string{
		"--click-global-color-background-default",
		"--click-global-color-text-muted",
		"--click-global-color-stroke-default",
		`:root[data-cui-theme="dark"]`,
		"prefers-color-scheme:dark",
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("template lost the Click UI token layer: missing %q", want)
		}
	}
}

// Every hex below is a Click UI token and the five-slot order was chosen by
// running the palette validator over candidate orderings. Editing one by hand
// invalidates that: adjacent CVD dE 20.1 and normal-vision dE 31.3, both modes.
// The dark tokens are declared twice (OS media query + explicit toggle stamp)
// and have already drifted once: --link landed only in the media block, so a
// toggled dark theme kept the unreadable light link colour.
func TestTemplate_DarkTokenBlocksDoNotDrift(t *testing.T) {
	if got := strings.Count(htmlTemplate, "--link:#A1BEF7"); got != 2 {
		t.Errorf("--link dark override must appear in BOTH dark scopes (media query + data-cui-theme), found %d", got)
	}
}

func TestTemplate_ValidatedPalette(t *testing.T) {
	for _, hex := range []string{"#089B83", "#AA00FF", "#B28800", "#CC0099", "#959900"} {
		if !strings.Contains(htmlTemplate, hex) {
			t.Errorf("categorical slot %s missing — re-run validate_palette.js before changing the palette", hex)
		}
	}
	// The set is deliberately mode-invariant; that is what lets a theme switch
	// be a Chart.update() instead of a rebuild.
	if strings.Contains(htmlTemplate, "C[i%C.length]") {
		t.Error("palette is being cycled: a generated 6th hue is indistinguishable under CVD — fold into OTHER instead")
	}
	// Material leftovers from the pre-Click-UI palette.
	for _, stale := range []string{"#4CAF50", "#2196F3", "#E91E63", "#f44336", "#FC4F05", "#FFB627", "#9C27B0"} {
		if strings.Contains(htmlTemplate, stale) {
			t.Errorf("non-token colour %s is back in the template", stale)
		}
	}
}

func TestTemplate_NoDualAxisOrPieCharts(t *testing.T) {
	// Two y-scales on one plot align arbitrarily and invent a correlation the
	// data does not contain.
	if strings.Contains(htmlTemplate, "yAxisID") {
		t.Error("a dual-axis chart is back: split it into two single-axis charts instead")
	}
	// Donuts cap the palette at three all-pairs-safe slots and hide close values.
	for _, bad := range []string{"type:'doughnut'", "type:'pie'"} {
		if strings.Contains(htmlTemplate, bad) {
			t.Errorf("%s is back: use a bar for magnitude comparison", bad)
		}
	}
}

// Bar fills must be solid. The contrast leg of the palette validation assumes
// opaque marks; an alpha fill sits lighter over the surface and can drop below
// the 3:1 floor, and it lets the gridlines show through the data.
func TestTemplate_BarFillsAreOpaque(t *testing.T) {
	for _, bad := range []string{"alpha(C[0]", "alpha(C[1]", "alpha(C[2]", "alpha(STATUS."} {
		if strings.Contains(htmlTemplate, bad) {
			t.Errorf("translucent mark fill %q — the palette was validated on solid hexes", bad)
		}
	}
}

// The hash is the one thing a reader wants to copy out of these panels — it is
// what you grep query_log with — so it must never be truncated for display.
func TestTemplate_ShowsFullQueryHash(t *testing.T) {
	if strings.Contains(htmlTemplate, "shortHash") {
		t.Error("the hash is being truncated again; show the full normalized_query_hash")
	}
	if !strings.Contains(htmlTemplate, "function fullHash(") {
		t.Error("fullHash helper missing")
	}
	// When the SQL is unknown the label must fall back to the whole hash.
	if !strings.Contains(htmlTemplate, "return fullHash(r.hash)") {
		t.Error("queryLabel must fall back to the full hash when sample_query is absent")
	}
	for _, want := range []string{"function queryLabel(", "function bindQueryPeek(", "peek-slow-queries", "peek-heavy-reads"} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("query-text affordance missing: %q", want)
		}
	}
}

// normalized_query_hash is a UInt64. Sent as a JSON number it would be rounded
// past 2^53 by JavaScript and the displayed hash would silently be wrong.
func TestQueryPanelSQL_HashIsAString(t *testing.T) {
	for name, sql := range map[string]string{
		"querySlowSQL":  (&Generator{mode: "onprem"}).querySlowSQL(),
		"queryHeavySQL": (&Generator{mode: "onprem"}).queryHeavySQL(),
	} {
		if !strings.Contains(sql, "toString(normalized_query_hash) AS hash") {
			t.Errorf("%s must stringify the hash or JS will round it:\n%s", name, sql)
		}
		if !strings.Contains(sql, "AS sample_query") {
			t.Errorf("%s should carry a representative query text", name)
		}
	}

	// query_heavy aggregates cost, so it must be QueryFinish-only or every
	// QueryStart row (read_bytes=0) dilutes the averages and the ranking.
	heavy := (&Generator{mode: "onprem"}).queryHeavySQL()
	if !strings.Contains(heavy, "AND type = 'QueryFinish'") {
		t.Error("queryHeavySQL must filter to QueryFinish or QueryStart rows dilute every average")
	}

	// query_slow counts errors, so exception rows must be IN its window —
	// failures are never logged as QueryFinish (same defect class as
	// queryByUserSQL had).
	slow := (&Generator{mode: "onprem"}).querySlowSQL()
	for _, want := range []string{"ExceptionWhileProcessing", "ExceptionBeforeStart",
		"countIf(type = 'QueryFinish') AS executions"} {
		if !strings.Contains(slow, want) {
			t.Errorf("querySlowSQL missing %q — its errors column would be structurally always 0", want)
		}
	}
	if strings.Contains(slow, "avgIf(") {
		t.Error("querySlowSQL must use sum/greatest, not avgIf: avgIf yields nan for an all-failure shape")
	}
}

// Gov never ships a dashboard (see cmd.dashboardDecision), but the redaction
// stays as defence in depth: query_log.query is the raw SQL against the very
// names queries.gov/*.sql hashes.
func TestSampleQueryCol_GovIsRedacted(t *testing.T) {
	if got := (&Generator{mode: "gov"}).sampleQueryCol(); !strings.HasPrefix(got, "''") {
		t.Errorf("gov must not select raw query text, got %q", got)
	}
	if got := (&Generator{mode: "onprem"}).sampleQueryCol(); !strings.Contains(got, "any(query)") {
		t.Errorf("non-gov should sample the query text, got %q", got)
	}
}

// Header and nav must stick as ONE band.
//
// They used to stick separately, with the nav pinned at a hardcoded top:53px
// that had to equal the header's height. It did not — the header measures
// ~74px — so once the page scrolled the header covered the top 21px of the
// nav and its labels were sliced in half. Three separate constants were
// guessing that same height (nav top, section scroll-margin, the scroll-spy
// threshold); all three are now derived from one measured value.
func TestTemplate_TopbarSticksAsOneBand(t *testing.T) {
	if !strings.Contains(htmlTemplate, ".topbar{position:sticky;top:0;") {
		t.Error("header and nav must be wrapped in one sticky .topbar")
	}
	if !strings.Contains(htmlTemplate, `<div class="topbar">`) {
		t.Error("the .topbar wrapper is missing from the markup")
	}

	// No component may re-introduce a hardcoded offset for the band's height.
	for _, banned := range []string{"top:53px", "top:57px", "scroll-margin-top:110px", "top<=130"} {
		// Skip the explanatory comment, which names the old value on purpose.
		for _, line := range strings.Split(htmlTemplate, "\n") {
			if strings.Contains(line, banned) && !strings.Contains(line, "hardcoded") {
				t.Errorf("hardcoded band offset %q is back: %q", banned, strings.TrimSpace(line))
			}
		}
	}

	// Anchor offsets and the scroll-spy both read the measured height.
	if !strings.Contains(htmlTemplate, "scroll-margin-top:calc(var(--topbar-h") {
		t.Error("section scroll-margin-top must derive from --topbar-h")
	}
	// Measured by observation, not once: the band grows after init (hdr-meta's
	// second line, optional nav links un-hiding, webfont swap) and on resize.
	if !strings.Contains(htmlTemplate, "new ResizeObserver(measureTopbar)") {
		t.Error("--topbar-h must be re-measured via ResizeObserver, not set once")
	}
	if !strings.Contains(htmlTemplate, "const line=topbarH+8;") {
		t.Error("the scroll-spy threshold must follow the measured band height")
	}
}

// The template escapes by default, and these three sites are the ones that
// did not. None was exploitable — mode is a validated CLI enum, and every
// tile() caller passes a number or a version string — but the contract is
// worth only what enforces it, and host notes are genuinely free text built
// from OS errors and paths.
//
// Asserted as exact interpolation shapes rather than "contains esc(", because
// the failure mode is a future edit dropping esc() from one of them while the
// other two keep the call and the file still greps clean.
func TestTemplate_InterpolatesThroughEsc(t *testing.T) {
	for _, want := range []string{
		// header badge — the mode chip
		`'<span class="badge badge-'+esc(DATA.mode)+'">'+esc(DATA.mode)+'</span>'`,
		// stat tile helper — escape at the helper, not per call site
		`+'">'+esc(v)+'</div><div class="lbl">'+esc(l)+'</div></div>'`,
		// host facts notes — free text, escaped per element before joining
		`notes.map(esc).join('; ')`,
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("innerHTML site no longer escapes its interpolation: missing %q", want)
		}
	}

	// The raw forms must be gone, not merely joined by an escaped twin.
	for _, forbidden := range []string{
		"+DATA.mode+",
		"+'\">'+v+'</div>",
		"notes.join('; ')",
	} {
		if strings.Contains(htmlTemplate, forbidden) {
			t.Errorf("unescaped interpolation is back: %q", forbidden)
		}
	}
}
