package main

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// The mode decides which queries.* tree runs, and gov is the one that hashes
// identifiers. onprem is the least protective of the three, so a fallback
// TOWARDS it is the only wrong answer available here: `-mode govv` used to
// print one line and collect an unhashed onprem bundle, and with stdin closed
// — any CI job — nothing read that line.

func TestCanonicalMode_NormalisesAndAliases(t *testing.T) {
	for in, want := range map[string]string{
		"gov": "gov", "GOV": "gov", " gov ": "gov", "\tGov\n": "gov",
		"cloud": "cloud", "CLOUD": "cloud", "ch-cloud": "cloud", "clickhouse-cloud": "cloud",
		"onprem": "onprem", "on-prem": "onprem", "on_prem": "onprem",
		"on-premise": "onprem", "on-premises": "onprem", "self-hosted": "onprem",
		// Unrecognised must be "", never a guess.
		"govv": "", "": "", "  ": "", "prod": "", "government": "",
	} {
		if got := canonicalMode(in); got != want {
			t.Errorf("canonicalMode(%q) = %q, want %q", in, got, want)
		}
	}
}

// The padded form is called out separately because it is the one that used to
// slip through: ToLower left the space, == "gov" failed, onprem was adopted.
func TestCanonicalMode_PaddedGovIsNotDowngraded(t *testing.T) {
	if got := canonicalMode(" gov"); got != "gov" {
		t.Fatalf(`canonicalMode(" gov") = %q — a padded gov must not fall through`, got)
	}
}

func TestResolveMode_ExplicitInvalidIsAnError(t *testing.T) {
	for _, raw := range []string{"govv", "onpre", "prod", " ", "cloud9"} {
		mode, _, err := resolveMode(raw, true, false, unusedPrompt(t))
		if err == nil {
			t.Errorf("resolveMode(%q, explicit) returned %q with no error — an unparseable mode must fail, not default", raw, mode)
		}
		if mode != "" {
			t.Errorf("resolveMode(%q, explicit) leaked mode %q alongside its error", raw, mode)
		}
	}
}

func TestResolveMode_SuggestsTheNearestMode(t *testing.T) {
	_, _, err := resolveMode("govv", true, false, unusedPrompt(t))
	if err == nil || !strings.Contains(err.Error(), `did you mean "gov"`) {
		t.Fatalf("expected a gov suggestion, got %v", err)
	}
	// Nothing close enough should not invent one.
	_, _, err = resolveMode("banana", true, false, unusedPrompt(t))
	if err == nil || strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("unrelated input should not be given a suggestion, got %v", err)
	}
}

func TestResolveMode_ExplicitValidPassesThrough(t *testing.T) {
	for raw, want := range map[string]string{
		"gov": "gov", " GOV ": "gov", "cloud": "cloud", "self-hosted": "onprem",
	} {
		got, notice, err := resolveMode(raw, true, false, unusedPrompt(t))
		if err != nil || got != want {
			t.Errorf("resolveMode(%q) = (%q, %v), want %q", raw, got, err, want)
		}
		if notice != "" {
			t.Errorf("resolveMode(%q) should not narrate an explicit choice, got %q", raw, notice)
		}
	}
}

// Unset and nobody to ask: the default still applies, but it is announced.
// This is the CI shape, and the notice is the only signal the operator gets.
func TestResolveMode_UnsetNonInteractiveDefaultsAndSaysSo(t *testing.T) {
	got, notice, err := resolveMode("onprem", false, false, unusedPrompt(t))
	if err != nil || got != "onprem" {
		t.Fatalf("resolveMode(unset, non-interactive) = (%q, %v), want onprem", got, err)
	}
	if !strings.Contains(notice, "onprem") || !strings.Contains(notice, "not a terminal") {
		t.Errorf("the default must be announced, got %q", notice)
	}
}

func TestResolveMode_UnsetInteractive(t *testing.T) {
	// Enter accepts the offered default.
	got, _, err := resolveMode("onprem", false, true, func() (string, error) { return "\n", nil })
	if err != nil || got != "onprem" {
		t.Errorf("empty answer should take the default, got (%q, %v)", got, err)
	}
	// A typed mode wins, aliases and padding included.
	got, _, err = resolveMode("onprem", false, true, func() (string, error) { return "  GOV \n", nil })
	if err != nil || got != "gov" {
		t.Errorf("typed answer should be honoured, got (%q, %v)", got, err)
	}
	// A typo at the prompt is an error too — same reasoning as the flag.
	if _, _, err = resolveMode("onprem", false, true, func() (string, error) { return "govv\n", nil }); err == nil {
		t.Error("a mistyped answer must not fall back to onprem")
	}
	// Ctrl-D with nothing typed is a failure, not consent to the default.
	if _, _, err = resolveMode("onprem", false, true, func() (string, error) { return "", io.EOF }); err == nil {
		t.Error("an unreadable prompt must fail rather than assume onprem")
	}
}

func TestGetQueriesDir(t *testing.T) {
	for mode, want := range map[string]string{
		"cloud": "./queries.cloud", "onprem": "./queries.onprem", "gov": "./queries.gov",
		" GOV ": "./queries.gov", "self-hosted": "./queries.onprem",
	} {
		got, err := getQueriesDir(mode)
		if err != nil || got != want {
			t.Errorf("getQueriesDir(%q) = (%q, %v), want %q", mode, got, err, want)
		}
	}
	// The old default returned ./queries.onprem here, which made this the
	// second place an unrecognised mode became an unhashed collection.
	if got, err := getQueriesDir("govv"); err == nil {
		t.Errorf("getQueriesDir(%q) must fail rather than fall back, got %q", "govv", got)
	}
}

// unusedPrompt fails the test if the resolver asks a question it should not.
func unusedPrompt(t *testing.T) func() (string, error) {
	t.Helper()
	return func() (string, error) {
		t.Error("resolveMode prompted when it should not have")
		return "", errors.New("unexpected prompt")
	}
}
