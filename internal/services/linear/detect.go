// Package linear owns Linear-specific session-linking work.
//
// Detection, issue upsert, attachment/comment writes, state-sync transitions,
// team-key caching, and coexistence checks live behind this single service
// boundary. Without it, the feature drifts into multiple interpretations of
// "what is linked?", "what did we already post?", and "when do we suppress
// updates?" — see design doc 62 §"Linear session-linking should be one owned
// service boundary."
//
// Webhook ingestion remains the source of fresh external issue state but
// shares the same upsert helper exposed here so detection-vs-webhook races
// resolve to one consistent row.
package linear

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
)

// Detected is a single detection hit produced by ScanInputs.
type Detected struct {
	// Identifier is the Linear key like "ACS-1234".
	Identifier string
	// Workspace is the Linear workspace slug, when the source was a URL.
	// Bare-identifier matches leave this empty; resolution against the
	// org's connected workspace happens during issue upsert.
	Workspace string
	// Source describes how the reference was found. Used by the operator
	// debug surface so we can answer "why did this link?".
	Source DetectionSource
	// Position is the byte offset of the match inside the concatenated
	// input. Smaller positions sort earlier — primary is the first match.
	Position int
}

type DetectionSource string

const (
	DetectionSourceURL             DetectionSource = "url"
	DetectionSourceIdentifier      DetectionSource = "identifier"
	DetectionSourceReferencePicker DetectionSource = "reference_picker"
	DetectionSourceMidSession      DetectionSource = "mid_session_proposal"
)

// linearURLPattern matches Linear deep links. The optional trailing path
// (`/whatever-slug`) is allowed because Linear adds slug suffixes.
var linearURLPattern = regexp.MustCompile(
	`https?://linear\.app/(?P<workspace>[^/\s]+)/issue/(?P<key>[A-Z][A-Z0-9_]{0,9}-[0-9]+)(?:/[^\s)]*)?`,
)

// linearBareIdentifierPattern matches keys like "ACS-1234". The leading
// boundary keeps it from gluing onto adjacent identifiers; the prefix
// upper-case + alnum constraint matches Linear's own identifier rules.
var linearBareIdentifierPattern = regexp.MustCompile(
	`(?:^|[^A-Za-z0-9_/-])(?P<key>[A-Z][A-Z0-9_]{0,9}-[0-9]+)\b`,
)

// ScanInputs produces a deterministic, deduplicated, position-ordered slice
// of detected Linear references across the bounded inputs the design doc
// allows: turn-0 message body, user-set session title, titles of text/link
// references, user-set branch name. We do not scan attachment OCR, file
// contents, or agent output.
//
// teamKeys is the per-org cache of known Linear team keys. A bare identifier
// only becomes a candidate when its prefix is in this allowlist — without it,
// JIRA keys, AWS resource IDs, and internal codes would all false-positive.
//
// inputs are concatenated with newlines so a URL and a bare identifier across
// two fields keep distinct positions.
func ScanInputs(inputs []string, teamKeys map[string]bool) []Detected {
	if len(inputs) == 0 {
		return nil
	}

	// Concatenate inputs preserving relative order so position-based primary
	// resolution stays stable.
	var sb strings.Builder
	for i, in := range inputs {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(in)
	}
	combined := sb.String()

	hits := make([]Detected, 0)
	seen := make(map[string]int)

	for _, m := range linearURLPattern.FindAllStringSubmatchIndex(combined, -1) {
		workspace := combined[m[2]:m[3]]
		key := combined[m[4]:m[5]]
		if existing, ok := seen[key]; ok {
			if m[0] < hits[existing].Position {
				hits[existing].Position = m[0]
				hits[existing].Source = DetectionSourceURL
				hits[existing].Workspace = workspace
			}
			continue
		}
		seen[key] = len(hits)
		hits = append(hits, Detected{
			Identifier: key,
			Workspace:  workspace,
			Source:     DetectionSourceURL,
			Position:   m[0],
		})
	}

	for _, m := range linearBareIdentifierPattern.FindAllStringSubmatchIndex(combined, -1) {
		key := combined[m[2]:m[3]]
		prefix := keyPrefix(key)
		// Allowlist gate. URL hits already passed because they came in via
		// linear.app/<workspace>/issue/, which is its own evidence; the bare
		// identifier needs the team-key cache.
		if _, ok := seen[key]; ok {
			// Already detected via URL — keep URL provenance, don't downgrade.
			continue
		}
		if !teamKeys[prefix] {
			continue
		}
		seen[key] = len(hits)
		hits = append(hits, Detected{
			Identifier: key,
			Source:     DetectionSourceIdentifier,
			Position:   m[2],
		})
	}

	sort.SliceStable(hits, func(i, j int) bool {
		return hits[i].Position < hits[j].Position
	})
	return hits
}

// MightContainLinearRef returns true when a string syntactically looks like
// it could carry a Linear ref (URL or bare key). Intentionally laxer than
// ScanInputs — it has no allowlist gate — so callers can use it as a cheap
// "should we even try detection?" hint without leaking team-key state.
//
// Shares the underlying regex sources with ScanInputs so the two stay in
// lockstep: any tweak to URL- or identifier-shape detection benefits the
// hint automatically.
func MightContainLinearRef(s string) bool {
	if s == "" {
		return false
	}
	if linearURLPattern.MatchString(s) {
		return true
	}
	return linearBareIdentifierPattern.MatchString(s)
}

func keyPrefix(key string) string {
	idx := strings.IndexByte(key, '-')
	if idx <= 0 {
		return ""
	}
	return key[:idx]
}

// SourceInputsHash produces a stable hash for an ordered list of input
// strings so the link_linear_issue worker can dedupe re-runs on
// (session_id, source_inputs_hash).
func SourceInputsHash(inputs []string) string {
	h := sha256.New()
	for _, in := range inputs {
		h.Write([]byte(in))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}
