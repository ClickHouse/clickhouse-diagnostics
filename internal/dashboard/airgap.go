package dashboard

import (
	_ "embed"
	"strings"
)

// chartJS is Chart.js 4.4.3 (dist/chart.umd.js from the npm tarball, MIT
// licence in assets/LICENSE.chartjs.md; its banner keeps the notice in every
// page that inlines it). Only -airgap uses it: by default the dashboard still
// loads the same release from the CDN. airgap_test.go pins its checksum.
//
//go:embed assets/chart.umd.js
var chartJS string

// chartJSCDNTag is the htmlTemplate line -airgap replaces.
const chartJSCDNTag = `<script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.3/dist/chart.umd.min.js"></script>`

const (
	cdnFooter    = "requires Chart.js CDN"
	airgapFooter = "Chart.js 4.4.3 embedded (air-gap build, no network needed)"
)

// WithAirgap makes Generate inline Chart.js into dashboard.html instead of
// loading it from the CDN, so the page draws its charts with no network
// access. See airgap/README.md. Returns the receiver for chaining.
func (g *Generator) WithAirgap(on bool) *Generator {
	g.airgap = on
	return g
}

// inlineChartJS swaps the CDN <script> tag for the embedded library.
func inlineChartJS(html string) string {
	html = strings.Replace(html, chartJSCDNTag, "<script>\n"+chartJS+"\n</script>", 1)
	return strings.Replace(html, cdnFooter, airgapFooter, 1)
}
