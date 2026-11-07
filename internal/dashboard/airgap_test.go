package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
)

// chartJSSHA256固定vendored的assets/chart.umd.js：即npm chart.js@4.4.3
// tarball中的dist/chart.umd.js（其sha512 integrity与registry匹配）。
// 升级Chart.js意味着同时更换该文件、此校验和以及chartJSCDNTag中的版本。
const chartJSSHA256 = "6e708cb2c2b41604db1f5dec01724856ec53ac867899a1e4e2cfb8f0ace6bed9"

var remoteAsset = regexp.MustCompile(`<(script|link)\b[^>]*\b(src|href)="https?://`)

func TestChartJSAsset_IsThePinnedRelease(t *testing.T) {
	sum := sha256.Sum256([]byte(chartJS))
	if got := hex.EncodeToString(sum[:]); got != chartJSSHA256 {
		t.Fatalf("assets/chart.umd.js sha256 = %s，期望%s", got, chartJSSHA256)
	}
	// CDN构建和嵌入构建必须是同一个发布版本，这样无论是否使用-airgap，
	// dashboard看起来都一样。
	v := regexp.MustCompile(`chart\.js@([0-9.]+)/`).FindStringSubmatch(chartJSCDNTag)
	if v == nil || !strings.Contains(chartJS[:200], "Chart.js v"+v[1]+"\n") {
		t.Fatalf("CDN标签%q和嵌入横幅%q指向不同的发布版本", chartJSCDNTag, chartJS[:60])
	}
	if strings.Contains(strings.ToLower(chartJS+chartJSLicense), "</script") {
		t.Fatal("嵌入的Chart.js或其许可证包含</script，会提前结束内联<script>")
	}
	if strings.Contains(chartJSLicense, "*/") {
		t.Fatal("Chart.js许可证包含*/，会提前结束其注释")
	}
}

func TestRenderHTML_DefaultLoadsChartJSFromCDN(t *testing.T) {
	html := renderHTML(map[string]interface{}{"alerts": []interface{}{}}, false)
	if !strings.Contains(html, chartJSCDNTag) {
		t.Fatal("默认dashboard不再从CDN加载Chart.js")
	}
	if strings.Contains(html, "Chart.js v4") {
		t.Fatal("默认dashboard嵌入了Chart.js；只有-airgap才应如此")
	}
	if !strings.Contains(html, "需要 Chart.js CDN") {
		t.Fatal("默认页脚已变更")
	}
}

func TestRenderHTML_AirgapEmbedsChartJS(t *testing.T) {
	html := renderHTML(map[string]interface{}{"alerts": []interface{}{}, "version": "26.8.1.1"}, true)
	if m := remoteAsset.FindString(html); m != "" {
		t.Fatalf("air-gap dashboard仍在加载远程资源：%s", m)
	}
	if !strings.Contains(html, "*/\n"+chartJS) {
		t.Fatal("air-gap dashboard未内联vendored的Chart.js")
	}
	// MIT要求每份副本都包含完整的许可声明，而dashboard是独立传播的，
	// 所以许可证文本要和库本身放在一起。
	notice := "The above copyright notice and this permission notice shall be included"
	if i := strings.Index(html, notice); i < 0 || i > strings.Index(html, chartJS) {
		t.Fatal("air-gap dashboard在库之前缺少Chart.js MIT许可声明")
	}
	if strings.Contains(html, "需要 Chart.js CDN") {
		t.Fatal("air-gap页脚仍称需要CDN")
	}
	if !strings.Contains(html, `const DATA = {`) || !strings.Contains(html, `"26.8.1.1"`) {
		t.Fatal("air-gap dashboard丢失了数据payload")
	}
	// Chart.js必须在使用它的dashboard脚本之前定义。
	if strings.Index(html, chartJS) > strings.Index(html, "const DATA =") {
		t.Fatal("Chart.js被内联到调用它的脚本之后")
	}
}
