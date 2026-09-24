package prompts

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
)

// CodeReviewContractDigest binds raw-task scaffolding and review templates.
// Dynamic PR content has separate digests; hashing it here would prevent a
// evidence-only delta from ever qualifying for reassessment.
func CodeReviewContractDigest() (string, error) {
	paths, err := fs.Glob(templateFS, "templates/*.template")
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, path := range paths {
		raw, err := templateFS.ReadFile(path)
		if err != nil {
			return "", err
		}
		if _, err = fmt.Fprintf(h, "%d:%s:%d:", len(path), path, len(raw)); err != nil {
			return "", err
		}
		if _, err = h.Write(raw); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
