package linear

import (
	"reflect"
	"testing"
)

func TestScanInputs_URL(t *testing.T) {
	t.Parallel()
	hits := ScanInputs([]string{"please check https://linear.app/acs/issue/ACS-1234/some-slug today"}, nil)
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	if hits[0].Identifier != "ACS-1234" || hits[0].Workspace != "acs" || hits[0].Source != DetectionSourceURL {
		t.Fatalf("unexpected hit: %+v", hits[0])
	}
}

// TestScanInputs_URLWithQueryStringAndAnchor pins the URL regex behavior on
// Linear deep links that carry trailing query strings, anchors, or both.
// The regex's optional `(?:/[^\s)]*)?` segment requires a leading `/`, so
// `?...` and `#...` segments do not consume the key — the prefix match
// against `…/issue/<KEY>` is what counts.
func TestScanInputs_URLWithQueryStringAndAnchor(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"see https://linear.app/acs/issue/ACS-1?foo=bar":               "ACS-1",
		"see https://linear.app/acs/issue/ACS-1#comment-42":            "ACS-1",
		"see https://linear.app/acs/issue/ACS-1/some-slug?ref=team":    "ACS-1",
		"see https://linear.app/acs/issue/ACS-1/some-slug#comment-42":  "ACS-1",
		"https://linear.app/acs/issue/ACS-1234, please review":         "ACS-1234",
		"https://linear.app/acs/issue/ACS-1234/already-prefixed-slug)": "ACS-1234",
	}
	for input, want := range cases {
		hits := ScanInputs([]string{input}, nil)
		if len(hits) != 1 {
			t.Fatalf("input %q: expected 1 hit, got %d (%+v)", input, len(hits), hits)
		}
		if hits[0].Identifier != want {
			t.Errorf("input %q: expected identifier %q, got %q", input, want, hits[0].Identifier)
		}
		if hits[0].Workspace != "acs" {
			t.Errorf("input %q: expected workspace %q, got %q", input, "acs", hits[0].Workspace)
		}
		if hits[0].Source != DetectionSourceURL {
			t.Errorf("input %q: expected URL source, got %s", input, hits[0].Source)
		}
	}
}

func TestScanInputs_BareIdentifierRequiresAllowlist(t *testing.T) {
	t.Parallel()
	hits := ScanInputs([]string{"see ACS-1234 and JIRA-99"}, nil)
	if len(hits) != 0 {
		t.Fatalf("bare identifier without allowlist must not match (got %+v)", hits)
	}

	hits = ScanInputs([]string{"see ACS-1234 and JIRA-99"}, map[string]bool{"ACS": true})
	if len(hits) != 1 || hits[0].Identifier != "ACS-1234" {
		t.Fatalf("expected only ACS-1234 to match, got %+v", hits)
	}
	if hits[0].Source != DetectionSourceIdentifier {
		t.Fatalf("bare-identifier hit should carry identifier source, got %s", hits[0].Source)
	}
}

func TestScanInputs_OrderingPrimaryFirst(t *testing.T) {
	t.Parallel()
	allow := map[string]bool{"ACS": true, "ENG": true}
	hits := ScanInputs([]string{
		"first ENG-2 then ACS-1",
		"link https://linear.app/acs/issue/ACS-1",
	}, allow)
	if len(hits) != 2 {
		t.Fatalf("expected 2 unique hits (ACS-1 + ENG-2), got %d: %+v", len(hits), hits)
	}
	if hits[0].Identifier != "ENG-2" {
		t.Fatalf("first match by position should be ENG-2, got %s", hits[0].Identifier)
	}
	if hits[1].Identifier != "ACS-1" {
		t.Fatalf("second match should be ACS-1, got %s", hits[1].Identifier)
	}
	// URL hit should win provenance over the bare ACS-1 even though the
	// bare hit appears later in the combined input.
	if hits[1].Source != DetectionSourceIdentifier && hits[1].Source != DetectionSourceURL {
		t.Fatalf("ACS-1 should come from URL or identifier, got %s", hits[1].Source)
	}
}

func TestScanInputs_DedupesAcrossInputs(t *testing.T) {
	t.Parallel()
	allow := map[string]bool{"ACS": true}
	hits := ScanInputs([]string{
		"first ACS-7",
		"https://linear.app/acs/issue/ACS-7",
	}, allow)
	if len(hits) != 1 {
		t.Fatalf("identical key across inputs should dedupe, got %+v", hits)
	}
}

func TestSourceInputsHash_Stable(t *testing.T) {
	t.Parallel()
	a := SourceInputsHash([]string{"hello", "world"})
	b := SourceInputsHash([]string{"hello", "world"})
	if a != b {
		t.Fatalf("hash unstable: %s vs %s", a, b)
	}
	c := SourceInputsHash([]string{"hello", "world!"})
	if a == c {
		t.Fatal("different inputs should produce different hashes")
	}
}

func TestKeyPrefix(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"ACS-1234":  "ACS",
		"ENG-2":     "ENG",
		"NOT_A_KEY": "", // no hyphen → no prefix
		"":          "",
		"-1":        "", // hyphen at position 0 → no prefix
	}
	for in, want := range cases {
		got := keyPrefix(in)
		if got != want {
			t.Errorf("keyPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestScanInputs_MultipleBareIdentifiersSameTeamKey pins that all distinct
// bare identifiers under one team key are detected, not just the first.
// This is the common multi-issue session case ("addresses ACS-1, ACS-2,
// and ACS-3"); a regex that consumed the prefix once would silently drop
// related links.
func TestScanInputs_MultipleBareIdentifiersSameTeamKey(t *testing.T) {
	t.Parallel()
	allow := map[string]bool{"ACS": true}
	hits := ScanInputs([]string{"addresses ACS-1, ACS-2, and ACS-3"}, allow)
	if len(hits) != 3 {
		t.Fatalf("expected 3 hits for ACS-1/ACS-2/ACS-3, got %d: %+v", len(hits), hits)
	}
	wantOrder := []string{"ACS-1", "ACS-2", "ACS-3"}
	for i, h := range hits {
		if h.Identifier != wantOrder[i] {
			t.Errorf("hit[%d]: expected %q, got %q", i, wantOrder[i], h.Identifier)
		}
		if h.Source != DetectionSourceIdentifier {
			t.Errorf("hit[%d]: expected identifier source, got %s", i, h.Source)
		}
	}
}

// TestScanInputs_URLWinsOverBareForSameKey pins the provenance contract
// when the same key shows up both as a bare identifier and as a URL: URL
// wins, its workspace slug is preserved, and we don't double-count.
//
// The current implementation runs the URL pass first and skips the bare
// pass for already-seen keys, so URL provenance survives regardless of
// whether the bare appears earlier in the text. Tests both directions to
// document that.
func TestScanInputs_URLWinsOverBareForSameKey(t *testing.T) {
	t.Parallel()
	allow := map[string]bool{"ACS": true}

	t.Run("bare appears first then URL", func(t *testing.T) {
		t.Parallel()
		hits := ScanInputs([]string{"see ACS-1 first then https://linear.app/acs/issue/ACS-1"}, allow)
		if len(hits) != 1 {
			t.Fatalf("same key bare+URL must dedupe to one hit, got %d: %+v", len(hits), hits)
		}
		if hits[0].Source != DetectionSourceURL {
			t.Errorf("URL provenance must win, got %s", hits[0].Source)
		}
		if hits[0].Workspace != "acs" {
			t.Errorf("workspace from URL must be preserved, got %q", hits[0].Workspace)
		}
	})

	t.Run("URL appears first then bare", func(t *testing.T) {
		t.Parallel()
		hits := ScanInputs([]string{"https://linear.app/acs/issue/ACS-1 then bare ACS-1 again"}, allow)
		if len(hits) != 1 {
			t.Fatalf("same key URL+bare must dedupe to one hit, got %d: %+v", len(hits), hits)
		}
		if hits[0].Source != DetectionSourceURL {
			t.Errorf("URL provenance must win, got %s", hits[0].Source)
		}
		if hits[0].Workspace != "acs" {
			t.Errorf("workspace from URL must be preserved, got %q", hits[0].Workspace)
		}
	})
}

// TestScanInputs_PrefixShapeBoundaries pins the regex's accepted prefix
// shape: 1–10 uppercase chars, first char alphabetic, remainder alnum or
// underscore. Linear itself enforces these rules at the workspace level,
// so the tests double as a contract check on what we'll consume.
func TestScanInputs_PrefixShapeBoundaries(t *testing.T) {
	t.Parallel()

	// Single-letter prefix is allowed by Linear (rare but legal). The bare-
	// identifier path still gates on the team-key allowlist, so a stray
	// "A-1" that isn't a real team key still won't match.
	t.Run("single-letter prefix matches when allowlisted", func(t *testing.T) {
		t.Parallel()
		hits := ScanInputs([]string{"see A-1 today"}, map[string]bool{"A": true})
		if len(hits) != 1 || hits[0].Identifier != "A-1" {
			t.Fatalf("single-letter prefix in allowlist must match, got %+v", hits)
		}
	})

	t.Run("single-letter prefix without allowlist drops", func(t *testing.T) {
		t.Parallel()
		hits := ScanInputs([]string{"see A-1 today"}, map[string]bool{})
		if len(hits) != 0 {
			t.Fatalf("single-letter prefix without allowlist must drop, got %+v", hits)
		}
	})

	t.Run("eleven-char prefix is rejected by regex", func(t *testing.T) {
		t.Parallel()
		// 11 chars exceeds the {0,9} repeater (1 + 9 = 10 max). Even with
		// the allowlist set, the regex shouldn't fire so the gate never
		// runs.
		hits := ScanInputs([]string{"see ABCDEFGHIJK-1 today"}, map[string]bool{"ABCDEFGHIJK": true})
		if len(hits) != 0 {
			t.Fatalf("11-char prefix should not match the regex, got %+v", hits)
		}
	})

	t.Run("lowercase prefix is rejected", func(t *testing.T) {
		t.Parallel()
		hits := ScanInputs([]string{"see acs-1 today"}, map[string]bool{"ACS": true, "acs": true})
		if len(hits) != 0 {
			t.Fatalf("lowercase prefix must not match (Linear keys are uppercase), got %+v", hits)
		}
	})

	t.Run("alnum prefix character is rejected boundary", func(t *testing.T) {
		t.Parallel()
		// "xACS-1" must not match because the leading boundary in the
		// regex excludes alphanumerics. Without the boundary, embedded
		// keys in arbitrary identifiers (filenames, hashes) would false-
		// positive.
		hits := ScanInputs([]string{"see xACS-1 today"}, map[string]bool{"ACS": true})
		if len(hits) != 0 {
			t.Fatalf("identifier preceded by an alphanumeric must not match, got %+v", hits)
		}
	})

	t.Run("punctuation-bounded identifier matches", func(t *testing.T) {
		t.Parallel()
		// Real-world inputs commonly have punctuation around the key:
		// parentheses, colons, brackets. The boundary class must allow
		// these so legitimate references in PR/issue text still detect.
		cases := []string{
			"(ACS-1)",
			"[ACS-1]",
			"key:ACS-1.",
			"ACS-1!",
			"\"ACS-1\"",
		}
		for _, input := range cases {
			hits := ScanInputs([]string{input}, map[string]bool{"ACS": true})
			if len(hits) != 1 || hits[0].Identifier != "ACS-1" {
				t.Errorf("input %q: expected ACS-1 detection, got %+v", input, hits)
			}
		}
	})
}

func TestScanInputs_EmptyInputs(t *testing.T) {
	t.Parallel()
	got := ScanInputs(nil, map[string]bool{"ACS": true})
	if !reflect.DeepEqual(got, []Detected(nil)) {
		t.Fatalf("nil inputs should return nil, got %+v", got)
	}
	got = ScanInputs([]string{"", "", ""}, map[string]bool{"ACS": true})
	if len(got) != 0 {
		t.Fatalf("empty strings should produce no hits, got %+v", got)
	}
}
