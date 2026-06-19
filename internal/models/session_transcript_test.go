package models

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTranscriptWindowPosition_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     TranscriptWindowPosition
		expectErr bool
	}{
		{name: "latest", value: TranscriptWindowPositionLatest},
		{name: "older", value: TranscriptWindowPositionOlder},
		{name: "newer", value: TranscriptWindowPositionNewer},
		{name: "around", value: TranscriptWindowPositionAround},
		{name: "empty string", value: TranscriptWindowPosition(""), expectErr: true},
		{name: "bogus", value: TranscriptWindowPosition("bogus"), expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown window positions")
				return
			}
			require.NoError(t, err, "Validate should accept known window positions")
		})
	}
}

func TestTranscriptEntryKind_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     TranscriptEntryKind
		expectErr bool
	}{
		{name: "message", value: TranscriptEntryKindMessage},
		{name: "tool_use", value: TranscriptEntryKindToolUse},
		{name: "tool_result", value: TranscriptEntryKindToolResult},
		{name: "log", value: TranscriptEntryKindLog},
		{name: "human_input", value: TranscriptEntryKindHumanInput},
		{name: "milestone", value: TranscriptEntryKindMilestone},
		{name: "checkpoint", value: TranscriptEntryKindCheckpoint},
		{name: "unknown", value: TranscriptEntryKind("unknown"), expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown entry kinds")
				return
			}
			require.NoError(t, err, "Validate should accept known entry kinds")
		})
	}
}

func TestTranscriptCursor_Encode(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	threadID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	c := TranscriptCursor{
		Version:    1,
		OrgID:      orgID,
		ThreadID:   threadID,
		TurnNumber: 42,
		EntryID:    "msg_123",
	}

	encoded, err := c.Encode()
	require.NoError(t, err, "Encode should not return an error for a valid cursor")
	require.NotEmpty(t, encoded, "Encode should return a non-empty string")

	decoded, err := DecodeTranscriptCursor(encoded, orgID, threadID)
	require.NoError(t, err, "DecodeTranscriptCursor should succeed on a freshly encoded cursor")
	require.Equal(t, c.Version, decoded.Version)
	require.Equal(t, c.OrgID, decoded.OrgID)
	require.Equal(t, c.ThreadID, decoded.ThreadID)
	require.Equal(t, c.TurnNumber, decoded.TurnNumber)
	require.Equal(t, c.EntryID, decoded.EntryID, "decoded cursor should preserve the entry anchor")
}

func TestDecodeTranscriptCursor(t *testing.T) {
	t.Parallel()

	orgA := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	orgB := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	threadA := uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc")
	threadB := uuid.MustParse("dddddddd-dddd-dddd-dddd-dddddddddddd")

	validCursor := TranscriptCursor{
		Version:    1,
		OrgID:      orgA,
		ThreadID:   threadA,
		TurnNumber: 7,
		EntryID:    "msg_7",
	}
	validEncoded, err := validCursor.Encode()
	require.NoError(t, err)

	wrongVersionCursor := TranscriptCursor{
		Version:    2,
		OrgID:      orgA,
		ThreadID:   threadA,
		TurnNumber: 1,
		EntryID:    "msg_1",
	}
	wrongVersionEncoded, err := wrongVersionCursor.Encode()
	require.NoError(t, err)

	orgMismatchCursor := TranscriptCursor{
		Version:    1,
		OrgID:      orgA,
		ThreadID:   threadA,
		TurnNumber: 1,
		EntryID:    "msg_1",
	}
	orgMismatchEncoded, err := orgMismatchCursor.Encode()
	require.NoError(t, err)

	threadMismatchCursor := TranscriptCursor{
		Version:    1,
		OrgID:      orgA,
		ThreadID:   threadA,
		TurnNumber: 1,
		EntryID:    "msg_1",
	}
	threadMismatchEncoded, err := threadMismatchCursor.Encode()
	require.NoError(t, err)

	tests := []struct {
		name        string
		raw         string
		orgID       uuid.UUID
		threadID    uuid.UUID
		expectErr   bool
		errContains string
	}{
		{
			name:     "valid round-trip",
			raw:      validEncoded,
			orgID:    orgA,
			threadID: threadA,
		},
		{
			name:        "invalid base64",
			raw:         "!!!",
			orgID:       orgA,
			threadID:    threadA,
			expectErr:   true,
			errContains: "decode",
		},
		{
			name:        "wrong version",
			raw:         wrongVersionEncoded,
			orgID:       orgA,
			threadID:    threadA,
			expectErr:   true,
			errContains: "version",
		},
		{
			name:        "org mismatch",
			raw:         orgMismatchEncoded,
			orgID:       orgB,
			threadID:    threadA,
			expectErr:   true,
			errContains: "org",
		},
		{
			name:        "thread mismatch",
			raw:         threadMismatchEncoded,
			orgID:       orgA,
			threadID:    threadB,
			expectErr:   true,
			errContains: "thread",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := DecodeTranscriptCursor(tt.raw, tt.orgID, tt.threadID)
			if tt.expectErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.orgID, got.OrgID)
			require.Equal(t, tt.threadID, got.ThreadID)
			require.Equal(t, validCursor.EntryID, got.EntryID, "decoded cursor should include the stable transcript entry id")
		})
	}
}

func TestTranscriptCursor_Encode_Deterministic(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("55555555-5555-5555-5555-555555555555")
	threadID := uuid.MustParse("66666666-6666-6666-6666-666666666666")

	c := TranscriptCursor{
		Version:    1,
		OrgID:      orgID,
		ThreadID:   threadID,
		TurnNumber: 99,
		EntryID:    "log_99",
	}

	first, err := c.Encode()
	require.NoError(t, err)

	second, err := c.Encode()
	require.NoError(t, err)

	require.Equal(t, first, second, "Encode should produce identical output for the same cursor")
}

// Ensure the encoded format is valid base64url with no padding, as documented.
func TestTranscriptCursor_Encode_IsRawBase64URL(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("77777777-7777-7777-7777-777777777777")
	threadID := uuid.MustParse("88888888-8888-8888-8888-888888888888")

	c := TranscriptCursor{
		Version:    1,
		OrgID:      orgID,
		ThreadID:   threadID,
		TurnNumber: 1,
		EntryID:    "msg_1",
	}

	encoded, err := c.Encode()
	require.NoError(t, err)

	// Must not contain padding characters.
	require.NotContains(t, encoded, "=", "encoded cursor must not contain padding")

	// Must be decodable as raw (no-padding) base64url.
	b, err := base64.RawURLEncoding.DecodeString(encoded)
	require.NoError(t, err, "encoded cursor must be valid raw base64url")

	// The decoded bytes must be valid JSON.
	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(b, &raw), "decoded bytes must be valid JSON")
}
