package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
)

// chartJSSHA256 pins the vendored assets/chart.umd.js: the dist/chart.umd.js
// of the npm chart.js@4.4.3 tarball (whose sha512 integrity matches the
// registry). Upgrading Chart.js means replacing the file, this checksum and
// the version in chartJSCDNTag together.
const chartJSSHA256 = "6e708cb2c2b41604db1f5dec01724856ec53ac867899a1e4e2cfb8f0ace6bed9"

var remoteAsset = regexp.MustCompile(`<(script|link)\b[^>]*\b(src|href)="https?://`)

func TestChartJSAsset_IsThePinnedRelease(t *testing.T) {
	sum := sha256.Sum256([]byte(chartJS))
	if got := hex.EncodeToString(sum[:]); got != chartJSSHA256 {
		t.Fatalf("assets/chart.umd.js sha256 = %s, want %s", got, chartJSSHA256)
	}
	// The CDN build and the embedded build must be the same release, so a
	// dashboard looks the same with and without -airgap.
	v := regexp.MustCompile(`chart\.js@([0-9.]+)/`).FindStringSubmatch(chartJSCDNTag)
	if v == nil || !strings.Contains(chartJS[:200], "Chart.js v"+v[1]+"\n") {
		t.Fatalf("CDN tag %q and embedded banner %q name different releases", chartJSCDNTag, chartJS[:60])
	}
	if strings.Contains(strings.ToLower(chartJS), "</script") {
		t.Fatal("embedded Chart.js contains </script and would end the inline <script> early")
	}
}

func TestRenderHTML_DefaultLoadsChartJSFromCDN(t *testing.T) {
	html := renderHTML(map[string]interface{}{"alerts": []interface{}{}}, false)
	if !strings.Contains(html, chartJSCDNTag) {
		t.Fatal("default dashboard no longer loads Chart.js from the CDN")
	}
	if strings.Contains(html, "Chart.js v4") {
		t.Fatal("default dashboard embeds Chart.js; only -airgap should")
	}
	if !strings.Contains(html, "requires Chart.js CDN") {
		t.Fatal("default footer changed")
	}
}

func TestRenderHTML_AirgapEmbedsChartJS(t *testing.T) {
	html := renderHTML(map[string]interface{}{"alerts": []interface{}{}, "version": "26.8.1.1"}, true)
	if m := remoteAsset.FindString(html); m != "" {
		t.Fatalf("air-gap dashboard still loads a remote asset: %s", m)
	}
	if !strings.Contains(html, "<script>\n"+chartJS) {
		t.Fatal("air-gap dashboard does not inline the vendored Chart.js")
	}
	if strings.Contains(html, "requires Chart.js CDN") {
		t.Fatal("air-gap footer still says the CDN is required")
	}
	if !strings.Contains(html, `const DATA = {`) || !strings.Contains(html, `"26.8.1.1"`) {
		t.Fatal("air-gap dashboard lost its data payload")
	}
	// Chart.js must be defined before the dashboard script that uses it.
	if strings.Index(html, chartJS) > strings.Index(html, "const DATA =") {
		t.Fatal("Chart.js is inlined after the script that calls it")
	}
}
