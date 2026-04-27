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
