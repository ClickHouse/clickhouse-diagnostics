// Package docs 不包含代码：它的测试保证 README 与 docs/ 中的页面链接正确——
// 每个相对链接都指向存在的文件，每个 #anchor 都指向该文件中的现有标题。
package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

var (
	reLink    = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	reHeading = regexp.MustCompile(`^#{1,6}\s+(.*)$`)
)

// anchors 返回 Markdown 文件的标题锚点，采用 GitHub 风格：转小写、
// 去掉标点、空格转连字符、重复标题依次编号 -1、-2。
func anchors(t *testing.T, path string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	out, seen, fence := map[string]bool{}, map[string]int{}, false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "```") {
			fence = !fence
			continue
		}
		m := reHeading.FindStringSubmatch(line)
		if fence || m == nil {
			continue
		}
		var b strings.Builder
		for _, r := range strings.ToLower(strings.TrimSpace(m[1])) {
			switch {
			case r == ' ':
				b.WriteRune('-')
			case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
				b.WriteRune(r)
			}
		}
		s := b.String()
		if n := seen[s]; n > 0 {
			out[s+"-"+strconv.Itoa(n)] = true
		} else {
			out[s] = true
		}
		seen[s]++
	}
	return out
}

// links 返回代码块之外的链接。
func links(t *testing.T, path string) []string {
	raw, _ := os.ReadFile(path)
	var out []string
	fence := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "```") {
			fence = !fence
			continue
		}
		if fence {
			continue
		}
		for _, m := range reLink.FindAllStringSubmatch(line, -1) {
			out = append(out, m[1])
		}
	}
	return out
}

func TestDocsLinksResolve(t *testing.T) {
	root := "../.."
	files := []string{"README.md", "AGENTS.md", "cluster-collect/README.md", "airgap/README.md"}
	pages, _ := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	if len(pages) < 10 {
		t.Fatalf("docs/ has %d pages", len(pages))
	}
	for _, p := range pages {
		rel, _ := filepath.Rel(root, p)
		files = append(files, rel)
	}
	cache := map[string]map[string]bool{}
	checked := 0
	for _, f := range files {
		src := filepath.Join(root, f)
		for _, l := range links(t, src) {
			if regexp.MustCompile(`^[a-z]+:`).MatchString(l) {
				continue // http(s), mailto
			}
			target, anchor, _ := strings.Cut(l, "#")
			dest := src
			if target != "" {
				dest = filepath.Join(filepath.Dir(src), target)
				if _, err := os.Stat(dest); err != nil {
					t.Errorf("%s: link %q: no such file", f, l)
					continue
				}
			}
			checked++
			if anchor == "" || !strings.HasSuffix(dest, ".md") {
				continue
			}
			if cache[dest] == nil {
				cache[dest] = anchors(t, dest)
			}
			if !cache[dest][anchor] {
				t.Errorf("%s: link %q: no heading #%s in %s", f, l, anchor, filepath.Base(dest))
			}
		}
	}
	if checked < 50 {
		t.Fatalf("checked only %d links", checked)
	}
}

// 发布归档会随二进制分发 Makefile 的 DATA_DIRS、README 和 AGENTS.md。
// README 链接到的每个顶层路径都必须包含在其中，否则下载的发行版会
// 指向它并不包含的文件。
func TestReleasePayloadHoldsReadmeLinks(t *testing.T) {
	mk, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^DATA_DIRS=(.*)$`).FindSubmatch(mk)
	if m == nil {
		t.Fatal("no DATA_DIRS in the Makefile")
	}
	shipped := map[string]bool{"README.md": true, "AGENTS.md": true}
	for _, d := range strings.Fields(string(m[1])) {
		shipped[d] = true
	}
	for _, l := range links(t, "../../README.md") {
		if regexp.MustCompile(`^[a-z]+:`).MatchString(l) || strings.HasPrefix(l, "#") {
			continue
		}
		target, _, _ := strings.Cut(l, "#")
		top := strings.Split(filepath.ToSlash(filepath.Clean(target)), "/")[0]
		if !shipped[top] {
			t.Errorf("README links to %q, but %s is not in the release archive (Makefile DATA_DIRS)", l, top)
		}
	}
}
