package config

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The configuration merge report. ClickHouse builds the configuration it
// runs by merging config.d/ (and conf.d/) over config.xml and users.d/ over
// users.xml, and writes the result to preprocessed_configs/. When two files
// set the same element differently, the sources alone cannot say which value
// the server took; the merged file can. The report lists every such
// conflict with each file's value and the running one, and — when the merged
// file was written after the server started — the restart-only server
// settings whose new value is not live yet.
//
// It reads the sanitised copies in the bundle's configuration/ folder, never
// the originals, so every value it prints has already been scrubbed.

// ServerSetting is one row of system.server_settings.
type ServerSetting struct {
	Name, Value, ChangeableWithoutRestart string
}

// leaves of one configuration file: element path (below the root element,
// "/"-joined) to its values, and the paths that sit under a repeated
// sibling — a <node> list, a cluster's <replica>s — which are lists, not
// conflicts.
type leaves struct {
	values map[string][]string
	lists  map[string]bool
}

// noted attributes change what a value means, so they travel with it.
var notedAttrs = []string{"from_env", "from_zk", "incl", "replace", "remove"}

func readLeaves(path string) (*leaves, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := xml.NewDecoder(bytes.NewReader(StripComments(raw)))
	dec.Strict = false
	lv := &leaves{values: map[string][]string{}, lists: map[string]bool{}}
	type frame struct {
		name     string
		text     strings.Builder
		children int
		counts   map[string]int
		repeated bool
		attrs    string
	}
	var stack []*frame
	pathOf := func() (string, bool) {
		var parts []string
		rep := false
		for i, f := range stack {
			if i == 0 {
				continue // the root element: <clickhouse> or <yandex>
			}
			parts = append(parts, f.name)
			rep = rep || f.repeated
		}
		return strings.Join(parts, "/"), rep
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			f := &frame{name: t.Name.Local, counts: map[string]int{}}
			if n := len(stack); n > 0 {
				parent := stack[n-1]
				parent.children++
				parent.counts[f.name]++
				f.repeated = parent.counts[f.name] > 1
			}
			for _, a := range t.Attr {
				for _, n := range notedAttrs {
					if a.Name.Local == n {
						f.attrs += fmt.Sprintf(" [%s=%s]", n, a.Value)
					}
				}
			}
			stack = append(stack, f)
		case xml.CharData:
			if n := len(stack); n > 0 {
				stack[n-1].text.Write(t)
			}
		case xml.EndElement:
			n := len(stack)
			if n == 0 {
				continue
			}
			f := stack[n-1]
			if n > 1 && f.children == 0 {
				p, rep := pathOf()
				if rep {
					lv.lists[p] = true
				}
				lv.values[p] = append(lv.values[p], strings.TrimSpace(f.text.String())+f.attrs)
			} else if n > 1 && f.attrs != "" {
				p, _ := pathOf()
				lv.values[p] = append(lv.values[p], "(element)"+f.attrs)
			}
			stack = stack[:n-1]
		}
	}
	for p, v := range lv.values {
		if len(v) > 1 {
			lv.lists[p] = true // repeated within one file
		}
	}
	return lv, nil
}

// group names which merged file a source file ends up in: config.d/ and
// conf.d/ merge over config.xml, users.d/ over users.xml. "" for a file the
// server does not merge (another top-level file, a directory of its own).
func group(rel string) string {
	r := filepath.ToSlash(rel)
	switch {
	case r == "users.xml" || r == "users.yaml" || r == "users.yml" || strings.HasPrefix(r, "users.d/"):
		return "users"
	case r == "config.xml" || r == "config.yaml" || r == "config.yml" ||
		strings.HasPrefix(r, "config.d/") || strings.HasPrefix(r, "conf.d/"):
		return "config"
	}
	return ""
}

// Conflict is one element set differently by more than one file.
type Conflict struct {
	Group, Path string
	Files       []string // in merge order
	Values      []string
	Running     string // "" when the merged file does not have it or was not collected
	SetBy       string // the file whose value is the running one
}

// MergeReport compares the collected sources with the collected merged
// files. collectedDir is the bundle's configuration/ folder; merged files
// are in its preprocessed/ subfolder. mergedWritten holds each merged
// file's modification time by group ("config", "users"): the server
// rewrites config.xml and users.xml on separate reloads. sourceTimes are
// the originals' modification times by path relative to collectedDir (the
// copies are new files); with the server's start time they tell what
// changed since the last restart, and whether the last reload picked it up.
func MergeReport(collectedDir string, settings []ServerSetting, mergedWritten map[string]time.Time, serverStart time.Time, sourceTimes map[string]time.Time) string {
	var b strings.Builder
	b.WriteString("# ClickHouse configuration merge report\n")
	b.WriteString("#\n# The server runs the merged files in preprocessed/ (its preprocessed_configs/).\n")
	b.WriteString("# Values are the sanitised ones in this bundle (credentials show REMOVED).\n\n")

	sources := SourceFiles(collectedDir)
	merged := map[string]*leaves{}
	for _, g := range []string{"config", "users"} {
		if lv, err := readLeaves(filepath.Join(collectedDir, "preprocessed", g+".xml")); err == nil {
			merged[g] = lv
		}
	}
	if len(merged) == 0 {
		b.WriteString("merged files: not collected (preprocessed_configs/ was not readable or absent) — conflicts below cannot say which value runs\n")
	} else {
		fmt.Fprintf(&b, "merged files: preprocessed/config.xml, preprocessed/users.xml\n")
	}

	// loaded: the merged file of its group is not older than the source, so
	// the source can have contributed to it. Unknown times: assume it did.
	loaded := func(g, rel string) bool {
		mw, st := mergedWritten[g], sourceTimes[rel]
		return mw.IsZero() || st.IsZero() || !st.After(mw)
	}

	type def struct{ file, value string }
	defs := map[string]map[string][]def{} // group → path → definitions in merge order
	lists := map[string]bool{}
	var skipped []string
	for _, f := range sources {
		rel, _ := filepath.Rel(collectedDir, f)
		if !strings.HasSuffix(strings.ToLower(f), ".xml") && !strings.HasSuffix(strings.ToLower(f), ".conf") {
			skipped = append(skipped, rel)
			continue
		}
		lv, err := readLeaves(f)
		if err != nil {
			continue
		}
		g := group(rel)
		if g == "" {
			continue
		}
		if defs[g] == nil {
			defs[g] = map[string][]def{}
		}
		for p, vs := range lv.values {
			if lv.lists[p] {
				lists[g+"\x00"+p] = true
			}
			defs[g][p] = append(defs[g][p], def{rel, vs[0]})
		}
	}
	// A path repeated in the merged file is a list too, even when every
	// source file contributes one entry.
	for g, m := range merged {
		for p := range m.lists {
			lists[g+"\x00"+p] = true
		}
	}

	// effective: the loaded file whose definition of a path is the running
	// one — the last loaded file in merge order that sets the merged value.
	// A definition through a substitution (from_env, from_zk, incl) cannot be
	// compared with the resolved merged value, so it counts as setting it.
	effective := map[string]map[string]string{}
	var conflicts []Conflict
	identical := 0
	for _, g := range []string{"config", "users"} {
		effective[g] = map[string]string{}
		for p, ds := range defs[g] {
			if m := merged[g]; m != nil && !lists[g+"\x00"+p] && len(m.values[p]) == 1 {
				for i := len(ds) - 1; i >= 0; i-- {
					if (ds[i].value == m.values[p][0] || substituted(ds[i].value)) && loaded(g, ds[i].file) {
						effective[g][p] = ds[i].file
						break
					}
				}
			}
			if len(ds) < 2 || lists[g+"\x00"+p] {
				continue
			}
			distinct := map[string]bool{}
			for _, d := range ds {
				distinct[d.value] = true
			}
			if len(distinct) < 2 {
				identical++
				continue
			}
			c := Conflict{Group: g, Path: p}
			for _, d := range ds {
				c.Files = append(c.Files, d.file)
				c.Values = append(c.Values, d.value)
			}
			if m := merged[g]; m != nil && len(m.values[p]) == 1 {
				c.Running = m.values[p][0]
				c.SetBy = effective[g][p]
			}
			conflicts = append(conflicts, c)
		}
	}
	sort.Slice(conflicts, func(i, j int) bool {
		if conflicts[i].Group != conflicts[j].Group {
			return conflicts[i].Group < conflicts[j].Group
		}
		return conflicts[i].Path < conflicts[j].Path
	})

	fmt.Fprintf(&b, "\n## Settings defined in more than one file with different values: %d\n", len(conflicts))
	if len(conflicts) == 0 {
		b.WriteString("none\n")
	}
	for _, c := range conflicts {
		fmt.Fprintf(&b, "\n%s: %s\n", c.Group, c.Path)
		for i := range c.Files {
			fmt.Fprintf(&b, "  %-44s = %s\n", c.Files[i], c.Values[i])
		}
		switch {
		case c.Running == "":
			fmt.Fprintf(&b, "  %-44s = ?\n", "running (merged file)")
		case c.SetBy != "":
			fmt.Fprintf(&b, "  %-44s = %s   ← from %s\n", "running (merged file)", c.Running, c.SetBy)
		default:
			fmt.Fprintf(&b, "  %-44s = %s   ← matches no source file the merged config includes\n", "running (merged file)", c.Running)
		}
	}
	if identical > 0 {
		fmt.Fprintf(&b, "\n(%d more element(s) defined in more than one file with the same value.)\n", identical)
	}
	if len(skipped) > 0 {
		fmt.Fprintf(&b, "\nNot compared (YAML): %s\n", strings.Join(skipped, ", "))
	}

	writeChanged(&b, merged, effective, settings, mergedWritten, serverStart, sourceTimes)
	return b.String()
}

// writeChanged reports the source files changed since the server started.
// A file newer than its group's merged file was not picked up by the last
// reload (a failed reload leaves the old merged file in place). A
// restart-only server setting whose running value comes from a changed file
// may not be live yet: the file's time cannot prove the value itself
// changed, so it is reported as possible, and only for running values —
// never for a value another file overrides. system.server_settings cannot
// settle it: it shows a new value after a reload even when the setting
// only takes effect on restart.
func writeChanged(b *strings.Builder, merged map[string]*leaves, effective map[string]map[string]string, settings []ServerSetting, mergedWritten map[string]time.Time, serverStart time.Time, sourceTimes map[string]time.Time) {
	const f = "2006-01-02 15:04:05 UTC"
	b.WriteString("\n## Changed since the server started\n")
	if serverStart.IsZero() || len(sourceTimes) == 0 {
		b.WriteString("not checked: the server's start time or the files' times are unknown\n")
		return
	}
	fmt.Fprintf(b, "server started %s\n", serverStart.UTC().Format(f))
	for _, g := range []string{"config", "users"} {
		if mw := mergedWritten[g]; merged[g] != nil && !mw.IsZero() && mw.Before(serverStart.Add(-time.Minute)) {
			fmt.Fprintf(b, "the merged %s.xml is older than the server's start (written %s): the server did not rewrite it when it started — the sources may have failed to load; check the server log for a configuration error\n",
				g, mw.UTC().Format(f))
		}
	}
	var changed []string
	for rel, mt := range sourceTimes {
		if mt.After(serverStart) && group(rel) != "" {
			changed = append(changed, rel)
		}
	}
	sort.Strings(changed)
	if len(changed) == 0 {
		b.WriteString("none: no configuration file changed since the server started\n")
		return
	}
	restartOnly := map[string]bool{}
	for _, s := range settings {
		if s.ChangeableWithoutRestart == "No" {
			restartOnly[s.Name] = true
		}
	}
	for _, rel := range changed {
		g, mt := group(rel), sourceTimes[rel]
		mw := mergedWritten[g]
		note := ""
		switch {
		case merged[g] == nil || mw.IsZero():
		case mw.Before(mt):
			note = fmt.Sprintf(" — NOT in the merged %s.xml (written %s): the last reload did not pick it up; check the server log for a config error", g, mw.UTC().Format(f))
		default:
			note = fmt.Sprintf(" — in the merged %s.xml", g)
		}
		fmt.Fprintf(b, "  %-44s changed %s%s\n", rel, mt.UTC().Format(f), note)
		// Restart-only settings are server settings: config group only,
		// and only the running values this file provides.
		if g != "config" || merged[g] == nil || len(restartOnly) == 0 {
			continue
		}
		var names []string
		for p, file := range effective[g] {
			if file == rel && restartOnly[p] {
				names = append(names, p)
			}
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(b, "    %-42s = %s — running value from a file changed since the start: if it is new, it takes effect only after a restart\n", n, merged[g].values[n][0])
		}
	}
	if len(settings) == 0 {
		b.WriteString("(system.server_settings was not collected: restart-only settings are not named)\n")
	}
}

// substituted reports whether a definition takes its value from a
// substitution (from_env, from_zk, incl) the merged file has resolved.
func substituted(v string) bool {
	return strings.Contains(v, "[from_env=") || strings.Contains(v, "[from_zk=") || strings.Contains(v, "[incl=")
}
