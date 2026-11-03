// Package docs holds no code: its test keeps the README and the pages in
// docs/ linked correctly — every relative link names an existing file, and
// every #anchor an existing heading of that file.
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

// anchors returns a markdown file's heading anchors, GitHub style: lower
// case, punctuation dropped, spaces to hyphens, repeats numbered -1, -2.
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

// links outside code blocks.
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
