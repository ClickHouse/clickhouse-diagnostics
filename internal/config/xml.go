package config

import "regexp"

// xmlComment matches an XML comment, including one spanning lines.
var xmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)

// StripComments removes XML comments from a ClickHouse configuration file so
// the commented-out examples in it are not read as configuration.
//
// This matters because ClickHouse's own stock config.xml ships entire example
// blocks inside comments — the `<zookeeper>` block naming example1 / example2 /
// example3 is in every default install — and ClickHouse's parser ignores them,
// so anything that scans those files by pattern has to ignore them too or it
// reads hosts and paths the operator never configured.
//
// It is a lexical strip, not a parse: a literal "<!--" inside an attribute or a
// text node would confuse it. Nothing in a ClickHouse config does that, and the
// alternative — a full XML parse of files that legitimately contain
// substitutions and duplicate keys — is the heavier risk.
func StripComments(b []byte) []byte {
	return xmlComment.ReplaceAll(b, nil)
}
