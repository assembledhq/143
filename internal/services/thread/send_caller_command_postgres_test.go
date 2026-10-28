package thread

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// This proof uses actual PostgreSQL JSONB encoding/decoding, not JSON indentation
// as a substitute. It performs no owner, service, admission, or intent writes.
func TestCallerSendCommandPostgresJSONBRoundTrip(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	pool := testpostgres.NewPool(t, ctx, "../../../migrations", 1)
	require.Equal(t, int32(1), pool.Config().MaxConns, "the independent codec proof must use at most one disposable-database connection")
	tests := []struct {
		name    string
		purpose CallerSendPurpose
		prefix  string
		shape   string
	}{}
	for _, purpose := range []struct {
		name    string
		purpose CallerSendPurpose
		prefix  string
	}{
		{name: "reviewer dispatch", purpose: CallerSendCodeReviewReviewerDispatch, prefix: "cr-dispatch:v1:"},
		{name: "initial loop send", purpose: CallerSendReviewLoopInitialSend, prefix: "rl-startup:v1:"},
	} {
		for _, shape := range []string{"complete effective input", "nil collections and absent pointers", "empty collections and absent pointers", "present empty continuation pointer"} {
			tests = append(tests, struct {
				name          string
				purpose       CallerSendPurpose
				prefix, shape string
			}{
				name: purpose.name + "/" + shape, purpose: purpose.purpose, prefix: purpose.prefix, shape: shape,
			})
		}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			caseCtx, caseCancel := context.WithTimeout(ctx, 15*time.Second)
			defer caseCancel()
			// Supply every independent owner field before constructing or
			// reading command bytes. No identity is recovered from JSON.
			ownerID := uuid.New()
			owner := CallerSendOwnerScope{Purpose: tt.purpose, OwnerID: ownerID,
				OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(),
				ClientMessageID: tt.prefix + ownerID.String()}
			input := callerSendCommandPGInput(owner, tt.shape)
			expectedInput := callerSendCommandPGExpectedInput(input, owner.ClientMessageID)
			command, err := NewCallerSendCommand(owner.Purpose, owner.OwnerID, input)
			require.NoError(t, err, "construct the original command only from the independently retained owner and complete effective input")
			canonical, err := command.Encode()
			require.NoError(t, err, "retain the original canonical bytes before PostgreSQL changes JSON presentation")
			originalDigest := command.Digest()
			require.Equal(t, expectedInput, command.SendInput(), "expected input must cover every field without deriving expected semantics from the retained command")
			var rawJSON []byte
			var jsonbText string
			var storedBytes int
			require.NoError(t, pool.QueryRow(caseCtx, `SELECT $1::jsonb,($1::jsonb)::text,octet_length(($1::jsonb)::text)`, json.RawMessage(canonical)).Scan(&rawJSON, &jsonbText, &storedBytes), "send canonical JSON through the real pgx JSONB parameter and scan both raw JSON and PostgreSQL text presentation")
			require.Equal(t, []byte(jsonbText), rawJSON, "driver raw JSON and PostgreSQL JSONB text must contain the same actual stored presentation")
			require.Equal(t, len([]byte(jsonbText)), storedBytes, "measure actual PostgreSQL presentation bytes rather than compact Go JSON length")
			require.LessOrEqual(t, storedBytes, CallerSendCommandMaxEncodedBytes, "these finite synthetic round trips must fit the decoder's explicit raw JSON limit")
			require.NotEqual(t, canonical, rawJSON, "the real JSONB path must exercise reordered/spaced presentation instead of returning canonical bytes unchanged")
			for _, representation := range []struct {
				name string
				raw  []byte
			}{
				{name: "driver raw JSON", raw: rawJSON}, {name: "PostgreSQL text", raw: []byte(jsonbText)},
			} {
				restored, decodeErr := DecodeCallerSendCommand(representation.raw, originalDigest, owner)
				require.NoError(t, decodeErr, "actual "+representation.name+" must restore using the original digest and independent owner scope")
				restoredCanonical, encodeErr := restored.Encode()
				require.NoError(t, encodeErr, "a real JSONB restoration must retain an initialized command")
				require.Equal(t, canonical, restoredCanonical, "JSONB key order, spacing and string presentation must not change exact canonical semantics")
				require.Equal(t, originalDigest, restored.Digest(), "restoration must preserve the original digest, never adopt a new digest of database presentation")
				require.Equal(t, expectedInput, restored.SendInput(), "every effective option, nested field, collection order and optional pointer presence must survive actual JSONB")
				require.Equal(t, owner.OwnerID, restored.OwnerID(), "the restored owner must be the independently retained owner, not one parsed from the payload")
				require.Equal(t, owner.Purpose, restored.Purpose(), "the independently supplied closed namespace must survive the round trip")
				require.Equal(t, owner.ClientMessageID, restored.ClientMessageID(), "the exact original owner-derived key must survive without minting another identity")
				proof, proofErr := restored.Proof()
				require.NoError(t, proofErr, "restored complete semantics should produce the same private correlation proof")
				expectedProof := &CallerSendCommandProof{version: 1, purpose: owner.Purpose, ownerID: owner.OwnerID, digest: originalDigest}
				require.Equal(t, expectedProof, proof, "the full opaque proof must retain original version, namespace, owner and semantic digest")
				withProof := restored.SendInput()
				withProof.CallerCommand = proof
				require.NoError(t, ValidateCallerSendCommandProof(withProof), "the restored proof must match complete effective input without adding authority")
				refrozen, refreezeErr := NewCallerSendCommand(owner.Purpose, owner.OwnerID, withProof)
				require.NoError(t, refreezeErr, "attaching private nonsemantic transport must not change JSONB-restored semantics")
				refrozenCanonical, refreezeEncodeErr := refrozen.Encode()
				require.NoError(t, refreezeEncodeErr, "refrozen input should encode without incorporating its own proof")
				require.Equal(t, canonical, refrozenCanonical, "the opaque proof must remain excluded from exact canonical bytes after the database round trip")
				require.Equal(t, originalDigest, refrozen.Digest(), "the proof's self-digest must remain nonsemantic")
			}
			if tt.shape == "nil collections and absent pointers" || tt.shape == "empty collections and absent pointers" {
				counterpart := callerSendCommandPGInput(owner, "nil collections and absent pointers")
				if tt.shape == "nil collections and absent pointers" {
					counterpart = callerSendCommandPGInput(owner, "empty collections and absent pointers")
				}
				paired, pairErr := NewCallerSendCommand(owner.Purpose, owner.OwnerID, counterpart)
				require.NoError(t, pairErr, "nil and empty collection inputs must both remain valid under the same original owner")
				pairedCanonical, pairEncodeErr := paired.Encode()
				require.NoError(t, pairEncodeErr, "the normalized collection counterpart must retain a canonical envelope")
				require.Equal(t, canonical, pairedCanonical, "nil and empty collections must normalize to the same exact array encoding")
				require.Equal(t, originalDigest, paired.Digest(), "only empty-collection normalization may unify these otherwise identical inputs")
			}
			if tt.shape == "present empty continuation pointer" {
				absent := input
				absent.ContinuationDedupeKeyOverride = nil
				paired, pairErr := NewCallerSendCommand(owner.Purpose, owner.OwnerID, absent)
				require.NoError(t, pairErr, "an absent continuation option must remain a separately valid semantic input")
				require.NotEqual(t, originalDigest, paired.Digest(), "absent and present-empty pointers must remain distinct after actual JSONB round trip")
			}
			if tt.shape == "complete effective input" {
				var changedJSON string
				require.NoError(t, pool.QueryRow(caseCtx, `SELECT jsonb_set($1::jsonb,'{message}',to_jsonb('Synthetic changed command'::text))::text`, json.RawMessage(canonical)).Scan(&changedJSON), "change one synthetic semantic field through actual PostgreSQL JSONB without changing retained identity or digest")
				changed, changedErr := DecodeCallerSendCommand([]byte(changedJSON), originalDigest, owner)
				require.Error(t, changedErr, "actual JSONB content mutation must not be accepted under the original retained digest")
				require.Equal(t, CallerSendCommand{}, changed, "failed semantic verification must not return a replacement command or digest")
			}
		})
	}
}

func callerSendCommandPGInput(owner CallerSendOwnerScope, shape string) SendMessageInput {
	input := SendMessageInput{OrgID: owner.OrgID, SessionID: owner.SessionID, ThreadID: owner.ThreadID,
		LegacyChangesetPolicy: LegacyChangesetUnchanged, ClientMessageID: owner.ClientMessageID,
		Message: "  Synthetic JSONB command\nwith exact whitespace, <tag>&, café, 🧪 and \u2028 separators.  "}
	if shape == "empty collections and absent pointers" || shape == "present empty continuation pointer" {
		input.Images, input.References, input.Commands, input.ResolveReviewCommentIDs = []string{}, models.SessionInputReferences{}, models.SessionInputCommands{}, []uuid.UUID{}
	}
	if shape == "present empty continuation pointer" {
		empty := ""
		input.ContinuationDedupeKeyOverride = &empty
	}
	if shape != "complete effective input" {
		return input
	}
	user, changeset, continuation := uuid.New(), uuid.New(), "  Synthetic retained continuation key  "
	input.UserID, input.ChangesetID, input.ContinuationDedupeKeyOverride = &user, &changeset, &continuation
	input.PlanMode = true
	input.Images = []string{"synthetic-image:first", "synthetic-image:second"}
	input.References = models.SessionInputReferences{
		{Kind: models.SessionInputReferenceKindFile, Token: "@first", Path: "synthetic/first.go", ID: "synthetic-reference", Display: "First synthetic file"},
		{Kind: models.SessionInputReferenceKindApp, Token: "@app", ID: "synthetic-app", Display: "Synthetic app"},
	}
	input.Commands = models.SessionInputCommands{
		{Kind: "command", AgentType: models.AgentTypeCodex, Name: "review", Token: "/review", Display: "Synthetic review", Description: "Synthetic description", Arguments: "  exact arguments\n ", Source: models.SessionInputCommandSourceBuiltin},
		{Kind: "command", AgentType: models.AgentTypeClaudeCode, Name: "check", Token: "/check", Display: "Synthetic check", Source: models.SessionInputCommandSourceProject},
	}
	input.MessageSource = models.SessionMessageSourceCodeReview
	input.PostSuccessAction, input.PostSuccessAuthorMode = "push_pr_changes", "user"
	return input
}

func callerSendCommandPGExpectedInput(input SendMessageInput, key string) SendMessageInput {
	input.ClientMessageID = key
	input.Images = append([]string{}, input.Images...)
	input.References = append(models.SessionInputReferences{}, input.References...)
	input.Commands = append(models.SessionInputCommands{}, input.Commands...)
	input.ResolveReviewCommentIDs = append([]uuid.UUID{}, input.ResolveReviewCommentIDs...)
	input.CallerCommand = nil
	return input
}
