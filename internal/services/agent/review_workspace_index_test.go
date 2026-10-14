package agent

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReviewVerificationIgnoresOriginalIndexMasksAndStatCaches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, flag   string
		forge, dirty bool
	}{
		{name: "unchanged source"},
		{name: "assume unchanged mask", flag: "--assume-unchanged", dirty: true},
		{name: "skip worktree mask", flag: "--skip-worktree", dirty: true},
		{name: "forged same-size stat cache", forge: true, dirty: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "", false)
			o := f.orchestrator()
			require.NoError(t, o.hydrateCodeReviewTrees(context.Background(), f.session, f.sandbox(), f.remote, "", o.logger), "index fixture should have complete pinned local source")
			changed := filepath.Join(f.workDir, "changed.go")
			if tt.flag != "" {
				out, err := exec.Command("/usr/bin/git", "-C", f.workDir, "update-index", tt.flag, "changed.go").CombinedOutput()
				require.NoError(t, err, "fixture should set the original mutable index flag: %s", out)
			}
			if tt.dirty {
				original, err := os.ReadFile(changed)
				require.NoError(t, err, "tracked fixture source should be readable")
				modified := strings.Replace(string(original), "PR head", "XX head", 1)
				require.NotEqual(t, string(original), modified, "same-length source edit must change material tracked bytes")
				require.NoError(t, os.WriteFile(changed, []byte(modified), 0o600), "fixture should modify tracked implementation while preserving size")
			}
			index := filepath.Join(f.workDir, ".git/index")
			if tt.forge {
				stamp := time.Now().Add(-time.Hour)
				require.NoError(t, os.Chtimes(changed, stamp, stamp), "non-racy source timestamp should precede the forged index")
				info, err := os.Stat(changed)
				require.NoError(t, err, "fixture should obtain its own stat metadata")
				data, err := os.ReadFile(index)
				require.NoError(t, err, "original SHA1 index should be readable")
				require.Equal(t, uint32(2), binary.BigEndian.Uint32(data[4:8]), "isolated platform clone should use standard v2 index layout")
				position := bytes.Index(data, []byte("changed.go\x00")) - 62
				require.GreaterOrEqual(t, position, 12, "fixture must locate the tracked index entry")
				status := reflect.ValueOf(info.Sys()).Elem()
				ctime := status.FieldByName("Ctim")
				if !ctime.IsValid() {
					ctime = status.FieldByName("Ctimespec")
				}
				require.True(t, ctime.IsValid(), "Unix fixture must expose actual ctime fields")
				binary.BigEndian.PutUint32(data[position:position+4], uint32(ctime.FieldByName("Sec").Int()))
				binary.BigEndian.PutUint32(data[position+4:position+8], uint32(ctime.FieldByName("Nsec").Int()))
				binary.BigEndian.PutUint32(data[position+8:position+12], uint32(info.ModTime().Unix()))
				binary.BigEndian.PutUint32(data[position+12:position+16], uint32(info.ModTime().Nanosecond()))
				checksum := sha1.Sum(data[:len(data)-20])
				copy(data[len(data)-20:], checksum[:])
				require.NoError(t, os.WriteFile(index, data, 0o600), "fixture should forge only mutable index stat caches with a valid checksum")
			}
			out, err := exec.Command("/usr/bin/git", "-C", f.workDir, "diff", "--quiet", "HEAD", "--").CombinedOutput()
			require.NoError(t, err, "ordinary index-based diff control should pass despite a material masked edit: %s", out)
			before, err := os.ReadFile(index)
			require.NoError(t, err, "original index bytes should be captured after the ordinary control")
			beforeInfo, err := os.Stat(index)
			require.NoError(t, err, "original index timestamp should be captured")
			err = o.execReviewEvidence(context.Background(), f.sandbox(), reviewPinnedCheckoutVerificationScript(f.workDir, f.base, f.head), "")
			if tt.dirty {
				require.Error(t, err, "owned fresh index must detect tracked implementation edits hidden by original flags/stat caches")
			} else {
				require.NoError(t, err, "owned fresh index must accept unchanged tracked source")
			}
			after, err := os.ReadFile(index)
			require.NoError(t, err, "original index must remain readable after verification")
			afterInfo, err := os.Stat(index)
			require.NoError(t, err, "original index timestamp must remain readable")
			require.Equal(t, before, after, "verification must preserve the original index byte for byte")
			require.Equal(t, beforeInfo.ModTime(), afterInfo.ModTime(), "verification must not refresh or rewrite the original index stat cache")
		})
	}
}
