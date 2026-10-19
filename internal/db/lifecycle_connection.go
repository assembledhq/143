package db

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/assembledhq/143/internal/models"
)

// LifecycleConnectionApplicationName labels a configured role and binary build,
// not a process instance or compatible writer. Startup labels are observational;
// in particular, transaction-pooling proxies need not preserve backend identity.
// Unknown roles return an empty option, preserving existing connection naming.
func LifecycleConnectionApplicationName(role models.LifecycleWriterRole, buildSHA string) string {
	if role.Validate() != nil {
		return ""
	}
	return "143:" + string(role) + ":" + lifecycleConnectionBuildLabel(buildSHA)
}

func lifecycleConnectionBuildLabel(buildSHA string) string {
	switch buildSHA {
	case "":
		return "unknown"
	case "dev":
		return "dev"
	}
	// Current binaries inject the full 40-hex git SHA. Even the longest role
	// plus this exact build fits PostgreSQL's 63-byte application_name limit.
	if len(buildSHA) == 40 {
		if _, err := hex.DecodeString(buildSHA); err == nil {
			return buildSHA
		}
	}
	// Arbitrary metadata may contain paths, controls or credentials. Do not
	// sanitize it into a misleading raw build identity or let PostgreSQL
	// silently truncate it. This marked 128-bit fingerprint is not the full
	// build value, nor an authorization or collision-free provenance proof.
	digest := sha256.Sum256([]byte(buildSHA))
	return "sha256-" + hex.EncodeToString(digest[:16])
}
