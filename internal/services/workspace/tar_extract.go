package workspace

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog"
)

// Sane caps for tar extraction. These exist because workspace snapshots come
// from our own pipeline today, but the reader still treats them as untrusted
// input — a buggy or hostile tar must not be able to fill the disk or place
// files outside the destination directory.
const (
	// maxTotalExtractedBytes caps the total decompressed size of regular-file
	// payloads written to disk. Tuned well above any real workspace we expect
	// to ship; the purpose is decompression-bomb defense, not workspace-size
	// enforcement.
	maxTotalExtractedBytes int64 = 2 << 30 // 2 GiB

	// maxPerEntryBytes caps the size of any individual file within the tar.
	maxPerEntryBytes int64 = 256 << 20 // 256 MiB

	// maxDecompressedStreamBytes caps the total bytes the decompressor may
	// produce — including tar metadata, padding, and entries that we end up
	// skipping. Without this, a hostile gzip stream that explodes through
	// tar headers (millions of zero-byte entries, gigabytes of padding, etc.)
	// would never trip the per-file or total payload caps and could pin the
	// extraction loop indefinitely. Set higher than maxTotalExtractedBytes
	// to leave headroom for legitimate tar overhead.
	maxDecompressedStreamBytes int64 = 4 << 30 // 4 GiB
)

// errOversize is returned when an extraction would exceed one of the caps.
var errOversize = errors.New("tar extraction exceeded size cap")

// extractTarGz reads a gzipped tar from src and writes the entries into
// destDir. Returns the total decompressed bytes written. Hostile entries
// (absolute paths, '..' traversal, symlinks, oversized files) are rejected
// or skipped; the function does not stop on a single bad entry unless the
// total size cap is hit, so a single garbage symlink doesn't poison the
// rest of the archive.
//
// Only regular files and directories are materialized. Symlinks, hard
// links, devices, and FIFOs are all skipped — the file-context API only
// needs to read regular file content, so accepting symlinks would be a
// security risk for no benefit.
//
// The decompressed stream is bounded by maxDecompressedStreamBytes so a
// hostile gzip cannot produce unbounded tar metadata before the per-file
// and total-payload caps would catch it. logger is used to emit debug
// records for skipped entries; passing zerolog.Nop() is fine.
func extractTarGz(src io.Reader, destDir string, logger zerolog.Logger) (int64, error) {
	if err := os.MkdirAll(destDir, 0o750); err != nil {
		return 0, fmt.Errorf("mkdir dest: %w", err)
	}

	gzr, err := gzip.NewReader(src)
	if err != nil {
		return 0, fmt.Errorf("open gzip: %w", err)
	}
	defer gzr.Close()

	// Wrap the decompressor in a counted reader that surfaces errOversize
	// when the cap is exceeded. The error propagates through tar.Reader's
	// next call (header read, body read, or padding skip) and breaks the
	// loop with a clear cause.
	capped := &cappedReader{r: gzr, capLimit: maxDecompressedStreamBytes}
	tr := tar.NewReader(capped)
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return total, fmt.Errorf("read tar header: %w", err)
		}

		clean, ok := safeTarEntryName(hdr.Name)
		if !ok {
			logger.Debug().Str("entry", hdr.Name).Msg("snapshot extract: skipping unsafe tar entry name")
			continue
		}

		dest := filepath.Join(destDir, clean)

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o750); err != nil {
				return total, fmt.Errorf("mkdir %s: %w", clean, err)
			}
		case tar.TypeReg:
			if hdr.Size > maxPerEntryBytes {
				// Oversize single file — skip rather than abort the whole
				// extraction, so one pathological file doesn't break review
				// for the rest of the workspace. tar.Reader.Next discards
				// any unread bytes from the current entry automatically,
				// and the cappedReader bounds total decompressor output —
				// so we don't need an explicit io.Copy here. (An explicit
				// unbounded io.Copy on the tar reader would also trip
				// gosec G110, since gosec can't see the cappedReader cap.)
				logger.Debug().
					Str("entry", clean).
					Int64("size_bytes", hdr.Size).
					Int64("cap", maxPerEntryBytes).
					Msg("snapshot extract: skipping oversize file")
				continue
			}
			if total+hdr.Size > maxTotalExtractedBytes {
				return total, fmt.Errorf("snapshot %w (would exceed %d bytes)", errOversize, maxTotalExtractedBytes)
			}
			n, err := writeTarFile(tr, dest)
			if err != nil {
				return total, err
			}
			total += n
		default:
			// Symlinks, hardlinks, devices, FIFOs — skip silently aside from
			// a debug log so operators can see when a snapshot has unexpected
			// entry types without spamming production logs.
			logger.Debug().
				Str("entry", clean).
				Str("typeflag", string(hdr.Typeflag)).
				Msg("snapshot extract: skipping non-regular tar entry")
		}
	}

	return total, nil
}

// cappedReader wraps an io.Reader and refuses to deliver bytes past
// capLimit, returning errOversize on any subsequent Read. Used to
// bound the decompressed tar stream end-to-end (metadata + payload +
// padding) so a hostile gzip cannot pin the extraction loop with
// cheap-to-decompress data that never reaches a regular-file payload
// check.
//
// Returning (n, err) with n == len(p) would let io.ReadFull-style
// callers swallow the error (it considers the read complete). To make
// the cap reliably observable, we shrink p before delegating to the
// underlying reader so the *next* Read returns (0, error).
type cappedReader struct {
	r        io.Reader
	n        int64
	capLimit int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.n >= c.capLimit {
		return 0, fmt.Errorf("decompressed stream exceeded %d bytes: %w", c.capLimit, errOversize)
	}
	remaining := c.capLimit - c.n
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// cappedWriter wraps an io.Writer and refuses to write bytes past capLimit.
// Used while staging compressed snapshot objects so a bad or unexpectedly
// large object cannot fill the cache disk before extraction caps can run.
type cappedWriter struct {
	w        io.Writer
	n        int64
	capLimit int64
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if c.n >= c.capLimit {
		return 0, fmt.Errorf("compressed snapshot exceeded %d bytes: %w", c.capLimit, errOversize)
	}
	remaining := c.capLimit - c.n
	oversize := int64(len(p)) > remaining
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	if err != nil {
		return n, err
	}
	if oversize {
		return n, fmt.Errorf("compressed snapshot exceeded %d bytes: %w", c.capLimit, errOversize)
	}
	return n, nil
}

// safeTarEntryName accepts a raw tar entry name and returns a cleaned,
// destination-relative path plus ok=true if the entry is safe to extract.
// Rejects absolute paths, NUL bytes, and any input containing a ".."
// segment — even one that filepath.Clean would collapse to an innocent
// in-tree path (e.g. "workspace/../escape" → "escape"). The extraction
// directory should reflect what the snapshot producer claimed; resolving
// traversal segments silently would let a malicious producer place files
// outside the directory it appears to be writing to.
func safeTarEntryName(raw string) (string, bool) {
	if raw == "" {
		return "", false
	}
	if strings.ContainsRune(raw, 0) {
		return "", false
	}
	// Walk the segments before cleaning so traversal intent is rejected
	// independently of how Clean would normalize the result.
	slashed := filepath.ToSlash(raw)
	for _, seg := range strings.Split(slashed, "/") {
		if seg == ".." {
			return "", false
		}
	}
	clean := filepath.ToSlash(filepath.Clean(raw))
	if clean == "." || clean == "/" {
		return "", false
	}
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "/") {
		return "", false
	}
	return clean, true
}

// writeTarFile copies the contents of the current tar entry into dest.
// Caps to maxPerEntryBytes via io.LimitReader so a header that lies about
// its size still cannot blow past the cap. The tar header's mode is
// intentionally ignored — review surface should never materialize
// executable bits on host disk, since a reader that strays past the
// workspace dir could otherwise hand the OS a runnable binary.
func writeTarFile(r io.Reader, dest string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return 0, fmt.Errorf("mkdir parent of %s: %w", dest, err)
	}
	// dest is filepath.Join(destDir, clean) where clean has been validated
	// by safeTarEntryName to reject absolute paths, NUL bytes, and any ".."
	// segments — so it cannot escape destDir.
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- dest is bounded under destDir; filename comes from safeTarEntryName
	if err != nil {
		return 0, fmt.Errorf("create %s: %w", dest, err)
	}
	defer f.Close()

	n, err := io.Copy(f, io.LimitReader(r, maxPerEntryBytes))
	if err != nil {
		return n, fmt.Errorf("write %s: %w", dest, err)
	}
	return n, nil
}
