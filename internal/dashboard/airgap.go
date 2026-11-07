package dashboard

import (
	_ "embed"
	"strings"
)

// chartJS是Chart.js 4.4.3（来自npm tarball的dist/chart.umd.js）。只有
// -airgap使用它：默认情况下dashboard仍从CDN加载同一版本。
// airgap_test.go固定了它的校验和。
//
//go:embed assets/chart.umd.js
var chartJS string

// chartJSLicense是Chart.js的MIT许可证。它作为注释内联在
// 库之前，因为dashboard.html会单独分发，而MIT要求
// 每份副本都附带完整的许可声明。
//
//go:embed assets/LICENSE.chartjs.md
var chartJSLicense string

// chartJSCDNTag是-airgap要替换的htmlTemplate行。
const chartJSCDNTag = `<script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.3/dist/chart.umd.min.js"></script>`

const (
	cdnFooter    = "需要 Chart.js CDN"
	airgapFooter = "已内嵌 Chart.js 4.4.3（air-gap 构建，无需网络）"
)

// WithAirgap让Generate把Chart.js内联到dashboard.html中，而不是
// 从CDN加载，这样页面无需网络访问即可绘制图表。见
// airgap/README.md。返回接收者以便链式调用。
func (g *Generator) WithAirgap(on bool) *Generator {
	g.airgap = on
	return g
}

// inlineChartJS把CDN的<script>标签替换为内嵌库。
func inlineChartJS(html string) string {
	inline := "<script>\n/*!\n" + chartJSLicense + "\n*/\n" + chartJS + "\n</script>"
	html = strings.Replace(html, chartJSCDNTag, inline, 1)
	return strings.Replace(html, cdnFooter, airgapFooter, 1)
}
