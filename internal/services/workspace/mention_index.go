package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/sandbox"
)

const (
	defaultMentionIndexMaxPaths     = 100000
	defaultMentionIndexMaxBlobBytes = 8 << 20
)

var mentionIndexIgnoredDirNames = map[string]struct{}{
	".git":         {},
	".next":        {},
	".nuxt":        {},
	".pnpm-store":  {},
	".turbo":       {},
	".venv":        {},
	"__pycache__":  {},
	"build":        {},
	"coverage":     {},
	"dist":         {},
	"node_modules": {},
	"target":       {},
	"vendor":       {},
	"venv":         {},
}

type MentionIndexEntry struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

type MentionIndex struct {
	Entries    []MentionIndexEntry `json:"entries"`
	Truncated  bool                `json:"truncated,omitempty"`
	EntryCount int                 `json:"entry_count,omitempty"`
}

type MentionIndexBuildConfig struct {
	MaxPaths     int
	MaxBlobBytes int
}

type recursiveMentionIndexReader interface {
	ListDirRecursive(ctx context.Context, maxEntries int, ignoredDirNames []string) ([]sandbox.FileEntry, error)
}

func (c MentionIndexBuildConfig) withDefaults() MentionIndexBuildConfig {
	if c.MaxPaths <= 0 {
		c.MaxPaths = defaultMentionIndexMaxPaths
	}
	if c.MaxBlobBytes <= 0 {
		c.MaxBlobBytes = defaultMentionIndexMaxBlobBytes
	}
	return c
}

func BuildMentionIndex(ctx context.Context, reader Reader) (MentionIndex, error) {
	return BuildMentionIndexWithConfig(ctx, reader, MentionIndexBuildConfig{})
}

func BuildMentionIndexWithConfig(ctx context.Context, reader Reader, cfg MentionIndexBuildConfig) (MentionIndex, error) {
	cfg = cfg.withDefaults()
	if reader == nil {
		return MentionIndex{}, errors.New("mention index reader is required")
	}
	if recursiveReader, ok := reader.(recursiveMentionIndexReader); ok {
		entries, err := recursiveReader.ListDirRecursive(ctx, cfg.MaxPaths, mentionIndexIgnoredDirNameList())
		if err != nil {
			return MentionIndex{}, fmt.Errorf("list mention index recursively: %w", err)
		}
		return buildMentionIndexFromEntries(ctx, entries, cfg)
	}

	index := MentionIndex{Entries: make([]MentionIndexEntry, 0, 256)}
	queue := []string{"."}
	seenDirs := map[string]struct{}{".": {}}

	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return MentionIndex{}, err
		}

		dirPath := queue[0]
		queue = queue[1:]

		entries, err := reader.ListDir(ctx, dirPath)
		if err != nil {
			return MentionIndex{}, fmt.Errorf("list mention index directory %s: %w", dirPath, err)
		}

		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return MentionIndex{}, err
			}

			cleanPath := strings.Trim(path.Clean(strings.TrimSpace(entry.Path)), "/")
			if cleanPath == "" || cleanPath == "." {
				continue
			}
			if mentionIndexShouldIgnorePath(cleanPath) {
				continue
			}

			kind := mentionIndexKindForEntryType(entry.Type)
			if kind == "" {
				continue
			}

			index.Entries = append(index.Entries, MentionIndexEntry{
				Kind: kind,
				Path: cleanPath,
			})
			if len(index.Entries) >= cfg.MaxPaths {
				index.Truncated = true
				index.EntryCount = len(index.Entries)
				sortMentionIndexEntries(index.Entries)
				return clampMentionIndexBlob(index, cfg.MaxBlobBytes)
			}

			if kind == "directory" {
				if _, ok := seenDirs[cleanPath]; ok {
					continue
				}
				seenDirs[cleanPath] = struct{}{}
				queue = append(queue, cleanPath)
			}
		}
	}

	sortMentionIndexEntries(index.Entries)
	index.EntryCount = len(index.Entries)
	return clampMentionIndexBlob(index, cfg.MaxBlobBytes)
}

func buildMentionIndexFromEntries(ctx context.Context, entries []sandbox.FileEntry, cfg MentionIndexBuildConfig) (MentionIndex, error) {
	index := MentionIndex{Entries: make([]MentionIndexEntry, 0, len(entries))}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return MentionIndex{}, err
		}

		cleanPath := strings.Trim(path.Clean(strings.TrimSpace(entry.Path)), "/")
		if cleanPath == "" || cleanPath == "." || mentionIndexShouldIgnorePath(cleanPath) {
			continue
		}

		kind := mentionIndexKindForEntryType(entry.Type)
		if kind == "" {
			continue
		}
		index.Entries = append(index.Entries, MentionIndexEntry{
			Kind: kind,
			Path: cleanPath,
		})
		if len(index.Entries) >= cfg.MaxPaths {
			index.Truncated = true
			index.EntryCount = len(index.Entries)
			sortMentionIndexEntries(index.Entries)
			return clampMentionIndexBlob(index, cfg.MaxBlobBytes)
		}
	}

	sortMentionIndexEntries(index.Entries)
	index.EntryCount = len(index.Entries)
	return clampMentionIndexBlob(index, cfg.MaxBlobBytes)
}

func mentionIndexIgnoredDirNameList() []string {
	names := make([]string, 0, len(mentionIndexIgnoredDirNames))
	for name := range mentionIndexIgnoredDirNames {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func mentionIndexShouldIgnorePath(cleanPath string) bool {
	for _, part := range strings.Split(cleanPath, "/") {
		if _, ok := mentionIndexIgnoredDirNames[part]; ok {
			return true
		}
	}
	return false
}

func mentionIndexKindForEntryType(entryType string) string {
	switch strings.ToLower(strings.TrimSpace(entryType)) {
	case "file":
		return "file"
	case "dir", "directory":
		return "directory"
	default:
		return ""
	}
}

func sortMentionIndexEntries(entries []MentionIndexEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Path == entries[j].Path {
			return entries[i].Kind < entries[j].Kind
		}
		return entries[i].Path < entries[j].Path
	})
}

func clampMentionIndexBlob(index MentionIndex, maxBlobBytes int) (MentionIndex, error) {
	if maxBlobBytes <= 0 {
		return index, nil
	}
	encoded, err := json.Marshal(index)
	if err != nil {
		return MentionIndex{}, fmt.Errorf("marshal mention index: %w", err)
	}
	if len(encoded) <= maxBlobBytes {
		return index, nil
	}

	index.Truncated = true
	for len(index.Entries) > 0 {
		index.Entries = index.Entries[:len(index.Entries)-1]
		index.EntryCount = len(index.Entries)
		encoded, err = json.Marshal(index)
		if err != nil {
			return MentionIndex{}, fmt.Errorf("marshal truncated mention index: %w", err)
		}
		if len(encoded) <= maxBlobBytes {
			return index, nil
		}
	}

	return index, nil
}

// SessionMentionIndexStaleCacheKey identifies the most recently built index
// for a session regardless of which container, snapshot, turn, or workspace
// generation produced it. The exact key (SessionMentionIndexCacheKey) churns
// on every completed turn, so the picker would otherwise pay a full rebuild
// each time the composer is opened; this alias lets the handler serve the
// previous turn's index immediately while a background refresh repopulates
// the exact key.
func SessionMentionIndexStaleCacheKey(session *models.Session) string {
	if session == nil {
		return "session-mention-index:v1:unknown:latest"
	}
	return fmt.Sprintf("session-mention-index:v1:%s:%s:latest", session.OrgID, session.ID)
}

func SessionMentionIndexCacheKey(session *models.Session) string {
	if session == nil {
		return "session-mention-index:v1:unknown"
	}
	if session.SnapshotKey != nil && *session.SnapshotKey != "" {
		return fmt.Sprintf("session-mention-index:v1:%s:%s:snapshot:%s", session.OrgID, session.ID, *session.SnapshotKey)
	}
	containerID := ""
	if session.ContainerID != nil {
		containerID = *session.ContainerID
	}
	return fmt.Sprintf("session-mention-index:v1:%s:%s:live:%s:turn:%d:workspace:%d", session.OrgID, session.ID, containerID, session.CurrentTurn, session.WorkspaceGeneration)
}
