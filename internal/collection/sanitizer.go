package collection

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Sanitizer removes credentials from ClickHouse XML / YAML config files
// before they are bundled into the support archive. It combines:
//
//  1. Structural redaction — proper XML / YAML parsing, walking the tree,
//     replacing values whose tag / key name matches the sensitive list.
//     This handles multi-line values, nested elements, and any future
//     credential-bearing tag whose name contains a recognised fragment
//     (e.g. `<my_custom_password>`).
//
//  2. Heuristic redaction — pattern matching for credential-shaped
//     byte sequences (AWS access keys, JWTs, PEM blocks, long hex /
//     base64). Applied to comments and free text that the structural
//     pass cannot reach, so a credential pasted into an XML comment or
//     a non-sensitive tag is still scrubbed.
//
// On a parse error the sanitizer fails closed — it returns the error
// to its caller, who must then decide not to ship the file. This is the
// safety property the previous regex-only implementation lacked.
type Sanitizer struct{}

// NewSanitizer returns a Sanitizer. The struct is stateless; the
// constructor exists for symmetry with the rest of the codebase.
func NewSanitizer() *Sanitizer { return &Sanitizer{} }

// SanitizeXMLContent removes credentials from a ClickHouse XML config
// using structural redaction + heuristics. Returns (sanitised content,
// number of redactions applied, error). On parse error the original
// content is returned unchanged so the caller can log it; the caller
// MUST still treat the error as a hard skip (do not ship the file).
func (s *Sanitizer) SanitizeXMLContent(content []byte) ([]byte, int, error) {
	out, n, err := redactXML(content)
	if err != nil {
		return content, 0, err
	}
	// Defence-in-depth: scan the redacted output for credential-shaped
	// values the structural pass could not reach (comments, free text
	// under non-sensitive tags, attribute values for tags we did not
	// recognise).
	final, extra := RedactCredentialsInText(string(out))
	return []byte(final), n + extra, nil
}

// SanitizeYAMLContent removes credentials from a YAML config using
// yaml.Node tree walking + heuristics. Same fail-closed contract as
// SanitizeXMLContent.
func (s *Sanitizer) SanitizeYAMLContent(content []byte) ([]byte, int, error) {
	out, n, err := redactYAML(content)
	if err != nil {
		return content, 0, err
	}
	final, extra := RedactCredentialsInText(string(out))
	return []byte(final), n + extra, nil
}

// IsXMLFile reports whether path/content should be treated as XML.
// Uses the file extension first, then the `<?xml` prolog, then a
// best-effort parse probe (catches extensionless includes).
func (s *Sanitizer) IsXMLFile(path string, content []byte) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".xml", ".xsd", ".svg", ".config":
		return true
	}
	if bytes.HasPrefix(bytes.TrimSpace(content), []byte("<?xml")) {
		return true
	}
	// Cheap parse probe — accept anything with a recognisable root tag.
	dec := xml.NewDecoder(bytes.NewReader(content))
	dec.Strict = false
	for i := 0; i < 8; i++ {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		if _, ok := tok.(xml.StartElement); ok {
			return true
		}
	}
	return false
}

// IsYAMLFile reports whether path is a YAML file by extension.
func (s *Sanitizer) IsYAMLFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return true
	}
	return false
}

// ShouldSanitize gates whether a file is a candidate for sanitisation
// at all. keepPasswords short-circuits to false so callers can opt out
// for self-review workflows.
func (s *Sanitizer) ShouldSanitize(path string, content []byte, keepPasswords bool) bool {
	if keepPasswords {
		return false
	}
	return s.IsXMLFile(path, content) || s.IsYAMLFile(path)
}

// ── XML ──────────────────────────────────────────────────────────────

// redactXML stream-parses content, finds byte ranges whose text content
// belongs to a sensitive tag, and rewrites those ranges. Attribute
// values whose attribute name is sensitive are redacted in place.
// Returns the rewritten bytes, redaction count, and any parse error.
func redactXML(content []byte) ([]byte, int, error) {
	dec := xml.NewDecoder(bytes.NewReader(content))
	dec.Strict = false

	type span struct {
		start, end int
	}
	var (
		redactSpans []span
		stack       []string
		prev        int64
		count       int
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, fmt.Errorf("xml parse: %w", err)
		}
		cur := dec.InputOffset()

		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)

			// Attribute redaction: rewrite the raw start-tag bytes
			// [prev, cur) so values of sensitive attributes are
			// replaced with REMOVED. This handles password="..."
			// and password='...' without trusting tag order.
			if hasSensitiveAttr(t.Attr) {
				rewritten := redactAttrs(content[prev:cur])
				if !bytes.Equal(rewritten, content[prev:cur]) {
					// Splice the rewrite into content. We do this
					// inline rather than via the span list because
					// the rewrite is the same length-or-shorter
					// substitution and keeps offsets stable for
					// later tokens — `xml.Decoder` continues to
					// read from the original bytes, so we only
					// patch the OUTPUT here.
					redactSpans = append(redactSpans, span{int(prev), int(cur)})
					attrRewrites[span{int(prev), int(cur)}] = rewritten
					count++
				}
			}

		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}

		case xml.CharData:
			if len(stack) == 0 {
				break
			}
			top := stack[len(stack)-1]
			if !isSensitiveName(top) {
				break
			}
			// Skip pure-whitespace runs so we keep indentation intact.
			if len(bytes.TrimSpace(t)) == 0 {
				break
			}
			redactSpans = append(redactSpans, span{int(prev), int(cur)})
			count++
		}
		prev = cur
	}

	if len(redactSpans) == 0 {
		return content, 0, nil
	}

	// Sort + de-dup spans (shouldn't overlap given our walk, but be safe).
	sort.Slice(redactSpans, func(i, j int) bool { return redactSpans[i].start < redactSpans[j].start })
	var out bytes.Buffer
	last := 0
	for _, sp := range redactSpans {
		if sp.start < last {
			continue
		}
		out.Write(content[last:sp.start])
		if rw, ok := attrRewrites[sp]; ok {
			out.Write(rw)
		} else {
			// For element text: preserve the surrounding whitespace
			// to keep indentation, just replace the non-whitespace
			// run with the redaction sentinel.
			orig := content[sp.start:sp.end]
			out.WriteString(replaceContentPreserveWS(string(orig)))
		}
		last = sp.end
	}
	out.Write(content[last:])
	// Reset the attribute rewrite map for next call.
	for k := range attrRewrites {
		delete(attrRewrites, k)
	}
	return out.Bytes(), count, nil
}

// attrRewrites is a per-call scratch map populated inside redactXML
// (sequential, single-goroutine call site) for splicing rewritten
// start-tag byte ranges into the output. Keyed by span to map a
// recorded redaction back to its rewritten replacement.
var attrRewrites = map[struct{ start, end int }]([]byte){}

// replaceContentPreserveWS substitutes the non-whitespace portion of
// text with the redaction sentinel, keeping the leading and trailing
// whitespace runs (so multi-line, indented tag bodies stay readable
// after sanitisation).
func replaceContentPreserveWS(s string) string {
	leading := s[:len(s)-len(strings.TrimLeft(s, " \t\r\n"))]
	trailing := s[len(strings.TrimRight(s, " \t\r\n")):]
	return leading + redacted + trailing
}

// hasSensitiveAttr returns true if any attribute in attrs has a
// sensitive name.
func hasSensitiveAttr(attrs []xml.Attr) bool {
	for _, a := range attrs {
		if isSensitiveName(a.Name.Local) {
			return true
		}
	}
	return false
}

// reXMLAttr matches `name="value"` or `name='value'` inside a start
// tag. Capture groups: 1=name, 2=opening quote, 3=value, 4=closing
// quote. The closing quote is captured to keep it from being
// re-introduced by the replacement.
var reXMLAttr = regexp.MustCompile(`(\w[\w.\-]*)=(["'])([^"']*)(["'])`)

// redactAttrs scans a single XML start-tag byte range and replaces
// values of sensitive attributes with the redaction sentinel.
func redactAttrs(tag []byte) []byte {
	return reXMLAttr.ReplaceAllFunc(tag, func(m []byte) []byte {
		sub := reXMLAttr.FindSubmatch(m)
		if len(sub) != 5 {
			return m
		}
		if !isSensitiveName(string(sub[1])) {
			return m
		}
		// name="value"  →  name="REMOVED"
		out := make([]byte, 0, len(sub[1])+len(redacted)+4)
		out = append(out, sub[1]...)
		out = append(out, '=')
		out = append(out, sub[2]...)
		out = append(out, []byte(redacted)...)
		out = append(out, sub[4]...)
		return out
	})
}

// ── YAML ─────────────────────────────────────────────────────────────

// redactYAML parses content as YAML and walks the resulting node tree,
// replacing scalar values whose key name is sensitive. Comments are
// scrubbed with the heuristic pass. Returns the marshalled output,
// redaction count, and any parse / marshal error.
func redactYAML(content []byte) ([]byte, int, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err != nil {
		return nil, 0, fmt.Errorf("yaml parse: %w", err)
	}
	count := 0
	walkYAMLNode(&root, false, &count)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return nil, 0, fmt.Errorf("yaml marshal: %w", err)
	}
	enc.Close()
	return buf.Bytes(), count, nil
}

// walkYAMLNode descends into n. parentIsSensitive is true when the
// node is a value (scalar or container) whose mapping key was
// sensitive — under such nodes every nested scalar is redacted.
//
// Comment fields on every node also get a heuristic pass: a value
// pasted into a YAML comment (`# token: abcdef...`) does not have a
// key the structural walk can see, but the heuristic catches it.
func walkYAMLNode(n *yaml.Node, parentIsSensitive bool, count *int) {
	if n == nil {
		return
	}
	scrubCommentsOn(n, count)

	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			walkYAMLNode(c, false, count)
		}

	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i]
			val := n.Content[i+1]
			scrubCommentsOn(key, count)
			isSensitive := isSensitiveName(key.Value)
			if isSensitive && val.Kind == yaml.ScalarNode {
				if val.Value != redacted {
					val.Value = redacted
					val.Tag = "!!str"
					val.Style = 0
					*count++
				}
				continue
			}
			walkYAMLNode(val, isSensitive, count)
		}

	case yaml.SequenceNode:
		for _, c := range n.Content {
			walkYAMLNode(c, parentIsSensitive, count)
		}

	case yaml.ScalarNode:
		if parentIsSensitive && n.Value != redacted {
			n.Value = redacted
			n.Tag = "!!str"
			n.Style = 0
			*count++
		}
	}
}

func scrubCommentsOn(n *yaml.Node, count *int) {
	if n == nil {
		return
	}
	if n.LineComment != "" {
		s, c := RedactCredentialsInText(n.LineComment)
		n.LineComment = s
		*count += c
	}
	if n.HeadComment != "" {
		s, c := RedactCredentialsInText(n.HeadComment)
		n.HeadComment = s
		*count += c
	}
	if n.FootComment != "" {
		s, c := RedactCredentialsInText(n.FootComment)
		n.FootComment = s
		*count += c
	}
}
