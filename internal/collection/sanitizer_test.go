package collection

import (
	"strings"
	"testing"
)

func TestSanitizeXMLContent_NamedTagsAndFragments(t *testing.T) {
	s := NewSanitizer()
	in := []byte(`<clickhouse>
  <user>
    <password>plaintextPassword123</password>
    <password_sha256_hex>e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855</password_sha256_hex>
    <my_custom_password>fragmentMatch</my_custom_password>
    <api_token>tok_fragmentMatch</api_token>
    <quota>default</quota>
  </user>
</clickhouse>`)
	out, count, err := s.SanitizeXMLContent(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count == 0 {
		t.Fatal("expected at least one redaction, got zero")
	}
	o := string(out)
	for _, leaked := range []string{
		"plaintextPassword123",
		"fragmentMatch",
		"tok_fragmentMatch",
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	} {
		if strings.Contains(o, leaked) {
			t.Errorf("leaked %q in sanitised output:\n%s", leaked, o)
		}
	}
	if !strings.Contains(o, "<quota>default</quota>") {
		t.Errorf("non-sensitive content was wrongly redacted:\n%s", o)
	}
}

func TestSanitizeXMLContent_MultiLineTagValue(t *testing.T) {
	// The previous regex-only implementation missed multi-line tag
	// content because the patterns lacked the (?s) flag. The parser
	// catches it regardless of how the value is wrapped.
	s := NewSanitizer()
	in := []byte("<password>line one\nline two\nline three</password>")
	out, count, err := s.SanitizeXMLContent(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count == 0 {
		t.Fatal("expected redaction, got zero")
	}
	o := string(out)
	for _, leak := range []string{"line one", "line two", "line three"} {
		if strings.Contains(o, leak) {
			t.Errorf("multi-line value not fully redacted; found %q in:\n%s", leak, o)
		}
	}
}

func TestSanitizeXMLContent_AttributeValues(t *testing.T) {
	s := NewSanitizer()
	in := []byte(`<root>
  <user name="alice" password="hunter2"/>
  <s3 access_key_id="AKIAIOSFODNN7EXAMPLE" secret_access_key="wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"/>
</root>`)
	out, _, err := s.SanitizeXMLContent(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	o := string(out)
	for _, leak := range []string{"hunter2", "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"} {
		if strings.Contains(o, leak) {
			t.Errorf("attribute value %q leaked:\n%s", leak, o)
		}
	}
	if !strings.Contains(o, `name="alice"`) {
		t.Errorf("non-sensitive attribute was wrongly redacted:\n%s", o)
	}
}

func TestSanitizeXMLContent_HeuristicsInComments(t *testing.T) {
	// A credential pasted into an XML comment — no structural tag for
	// the parser to grab onto. The heuristic pass catches it.
	s := NewSanitizer()
	in := []byte(`<root>
  <!-- earlier we used AKIAIOSFODNN7EXAMPLE for this -->
  <!-- the old token was eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxIn0.dummysig -->
</root>`)
	out, _, err := s.SanitizeXMLContent(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	o := string(out)
	if strings.Contains(o, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("AWS key in comment not redacted:\n%s", o)
	}
	if strings.Contains(o, "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9") {
		t.Errorf("JWT in comment not redacted:\n%s", o)
	}
}

func TestSanitizeXMLContent_FailsClosedOnParseError(t *testing.T) {
	s := NewSanitizer()
	in := []byte(`<root><password>oops</mismatched></root>`)
	_, _, err := s.SanitizeXMLContent(in)
	if err == nil {
		t.Fatal("expected parse error on mismatched tags, got nil")
	}
}

func TestSanitizeXMLContent_URLBasicAuth(t *testing.T) {
	s := NewSanitizer()
	in := []byte(`<root>
  <remote_servers>postgresql://chuser:supersecretpw@db.internal:5432/diag</remote_servers>
</root>`)
	out, _, err := s.SanitizeXMLContent(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	o := string(out)
	if strings.Contains(o, "supersecretpw") {
		t.Errorf("URL basic-auth password not redacted:\n%s", o)
	}
	if !strings.Contains(o, "db.internal") {
		t.Errorf("hostname stripped along with password (should be preserved):\n%s", o)
	}
}

func TestSanitizeYAMLContent_KeysAndComments(t *testing.T) {
	s := NewSanitizer()
	in := []byte(`user:
  name: alice
  password: hunter2
  api_token: tk_abcdef0123456789
  notes: "nothing sensitive"
# previous AKIAIOSFODNN7EXAMPLE rotated 2026-01-01
storage:
  account_key: deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef
  region: us-east-1
`)
	out, count, err := s.SanitizeYAMLContent(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count == 0 {
		t.Fatal("expected redactions, got zero")
	}
	o := string(out)
	for _, leak := range []string{"hunter2", "tk_abcdef0123456789", "AKIAIOSFODNN7EXAMPLE", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"} {
		if strings.Contains(o, leak) {
			t.Errorf("leaked %q:\n%s", leak, o)
		}
	}
	if !strings.Contains(o, "alice") || !strings.Contains(o, "us-east-1") {
		t.Errorf("non-sensitive values were wrongly redacted:\n%s", o)
	}
}

func TestSanitizeYAMLContent_FailsClosedOnParseError(t *testing.T) {
	s := NewSanitizer()
	in := []byte("password: [bad: ]: bracket")
	_, _, err := s.SanitizeYAMLContent(in)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
}

func TestRedactCredentialsInText_PEM(t *testing.T) {
	in := `before
-----BEGIN RSA PRIVATE KEY-----
MIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX6Ppy1tPf9Cnzj4p4WGeKLs1Pt8Qu
-----END RSA PRIVATE KEY-----
after`
	out, n := RedactCredentialsInText(in)
	if n == 0 {
		t.Fatal("PEM block not redacted")
	}
	if strings.Contains(out, "MIIBOgIBAAJBAKj34") {
		t.Errorf("PEM body leaked:\n%s", out)
	}
	if !strings.Contains(out, "before") || !strings.Contains(out, "after") {
		t.Errorf("surrounding text mangled:\n%s", out)
	}
}

func TestRedactCredentialsInText_AWSAndJWT(t *testing.T) {
	in := "key=AKIAIOSFODNN7EXAMPLE token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxIn0.dummysig tail"
	out, n := RedactCredentialsInText(in)
	if n < 2 {
		t.Fatalf("expected 2+ redactions, got %d", n)
	}
	if strings.Contains(out, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("AWS key leaked:\n%s", out)
	}
	if strings.Contains(out, "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9") {
		t.Errorf("JWT leaked:\n%s", out)
	}
	if !strings.Contains(out, "tail") {
		t.Errorf("trailing text dropped:\n%s", out)
	}
}

func TestRedactCredentialsInText_KeywordValueDisclosure(t *testing.T) {
	cases := []struct {
		in        string
		redact    []string
		keep      []string
		wantCount int
	}{
		{
			in:        `<!-- TODO: rotate old admin password "oldHunter2_2025" before next quarter -->`,
			redact:    []string{"oldHunter2_2025"},
			keep:      []string{"rotate old admin", "before next quarter"},
			wantCount: 1,
		},
		{
			in:        `legacy_secret: hunter2`,
			redact:    []string{"hunter2"},
			keep:      []string{"legacy_secret"},
			wantCount: 1,
		},
		{
			in:        `api_key = abc123XYZ`,
			redact:    []string{"abc123XYZ"},
			keep:      []string{"api_key"},
			wantCount: 1,
		},
		{
			// Prose — no quote, no `:`, no `=`. Must NOT match.
			in:        "password requirements: 8 characters minimum",
			redact:    nil,
			keep:      []string{"password requirements"},
			wantCount: 0,
		},
		{
			in:        "rotate the secret quarterly",
			redact:    nil,
			keep:      []string{"rotate the secret quarterly"},
			wantCount: 0,
		},
	}
	for _, tc := range cases {
		out, n := RedactCredentialsInText(tc.in)
		if n != tc.wantCount {
			t.Errorf("input=%q: got %d redactions, want %d (out=%q)", tc.in, n, tc.wantCount, out)
		}
		for _, leak := range tc.redact {
			if strings.Contains(out, leak) {
				t.Errorf("input=%q: leaked %q in output %q", tc.in, leak, out)
			}
		}
		for _, keep := range tc.keep {
			if !strings.Contains(out, keep) {
				t.Errorf("input=%q: lost expected substring %q in output %q", tc.in, keep, out)
			}
		}
	}
}

func TestIsSensitiveName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"password", true},
		{"PASSWORD", true},
		{"my_password_v2", true},
		{"api_key", true},
		{"service_account", true},
		{"client_id", true},
		{"username", false},
		{"hostname", false},
		{"region", false},
		{"quota", false},
		{"max_connections", false},
	}
	for _, tc := range cases {
		if got := isSensitiveName(tc.name); got != tc.want {
			t.Errorf("isSensitiveName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
