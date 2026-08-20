package paths

import (
	"strings"
	"testing"
)

// The Synology and partial-dir exclusions must precede any include rule,
// because rsync applies filters in order and the first match wins.
func TestIgnoredRulesComeFirst(t *testing.T) {
	for name, rules := range map[string][]string{
		"content":  ContentFilters(),
		"metadata": MetadataFilters(),
		"plain":    PlainFilters(),
	} {
		t.Run(name, func(t *testing.T) {
			if len(rules) < 2 {
				t.Fatalf("expected at least two rules, got %v", rules)
			}
			if !strings.Contains(rules[0], "@eaDir") {
				t.Errorf("first rule %q does not exclude @eaDir", rules[0])
			}
			if !strings.Contains(rules[1], PartialDir) {
				t.Errorf("second rule %q does not exclude the partial dir", rules[1])
			}
			for i, r := range rules {
				if strings.HasPrefix(r, "+ ") && i < 2 {
					t.Errorf("include rule %q appears before the exclusions", r)
				}
			}
		})
	}
}

// Content filters exclude metadata, anchored so that only depth-two
// directories are affected.
func TestContentFiltersExcludeAnchoredMetadata(t *testing.T) {
	got := strings.Join(ContentFilters(), "\n")
	for _, want := range []string{
		"- /*/gamelist.xml",
		"- /*/images/",
		"- /*/videos/",
		"- /*/manuals/",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("content filters missing %q\ngot:\n%s", want, got)
		}
	}
	if strings.Contains(got, "- images/") {
		t.Error("content filters contain an unanchored images rule")
	}
}

// Metadata filters are include-only, ending in a catch-all exclusion.
func TestMetadataFiltersAreIncludeOnly(t *testing.T) {
	rules := MetadataFilters()
	last := rules[len(rules)-1]
	if last != "- *" {
		t.Errorf("last metadata rule = %q, want %q", last, "- *")
	}
	got := strings.Join(rules, "\n")
	for _, want := range []string{
		"+ /*/",
		"+ /*/gamelist.xml",
		"+ /*/images/",
		"+ /*/images/**",
		"+ /*/videos/",
		"+ /*/videos/**",
		"+ /*/manuals/",
		"+ /*/manuals/**",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("metadata filters missing %q\ngot:\n%s", want, got)
		}
	}
}

// Plain filters carry no metadata rules at all, for trees with no split.
func TestPlainFiltersHaveNoMetadataRules(t *testing.T) {
	got := strings.Join(PlainFilters(), "\n")
	if strings.Contains(got, "gamelist") || strings.Contains(got, "images") {
		t.Errorf("plain filters should not mention metadata: %s", got)
	}
}
