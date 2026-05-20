package models

import (
	"crypto/sha256"
	"fmt"
)

// IssueFingerprint returns the canonical dedupe fingerprint for an external
// issue. Keep every ingestion and provider-specific upsert path on this helper
// so both unique indexes on issues agree about what "same issue" means.
func IssueFingerprint(source IssueSource, externalID string) string {
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("%s:%s", source, externalID)))
	return fmt.Sprintf("%x", h.Sum(nil))[:32]
}
