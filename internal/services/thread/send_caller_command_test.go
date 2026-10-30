package thread

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func callerCommandInputFixture() SendMessageInput {
	user, target, override := uuid.New(), uuid.New(), "  original continuation key  "
	return SendMessageInput{
		OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), UserID: &user, ChangesetID: &target,
		Message: "  original message\nwith exact whitespace  ", PlanMode: true,
		Images: []string{"first-image", "second-image"},
		References: models.SessionInputReferences{
			{Kind: models.SessionInputReferenceKindFile, Token: "@first", Path: "first.go", ID: "retained-id", Display: "first"},
			{Kind: models.SessionInputReferenceKindApp, Token: "@app", ID: "synthetic-app", Display: "app"},
		},
		Commands: models.SessionInputCommands{
			{Kind: "command", AgentType: models.AgentTypeCodex, Name: "review", Token: "/review", Display: "review", Description: "original description", Arguments: " exact arguments ", Source: models.SessionInputCommandSourceBuiltin},
			{Kind: "command", AgentType: models.AgentTypeClaudeCode, Name: "check", Token: "/check", Display: "check", Source: models.SessionInputCommandSourceProject},
		},
		MessageSource: models.SessionMessageSourceCodeReview, PostSuccessAction: "push_pr_changes", PostSuccessAuthorMode: "user", ContinuationDedupeKeyOverride: &override,
	}
}

func callerCommandOwner(command CallerSendCommand) CallerSendOwnerScope {
	input := command.SendInput()
	return CallerSendOwnerScope{Purpose: command.Purpose(), OwnerID: command.OwnerID(), OrgID: input.OrgID, SessionID: input.SessionID, ThreadID: input.ThreadID, ClientMessageID: input.ClientMessageID}
}

func callerCommandBytes(t *testing.T, command CallerSendCommand) []byte {
	t.Helper()
	raw, err := command.Encode()
	require.NoError(t, err, "valid frozen command should encode without allocating a new identity")
	return raw
}

func callerCommandDigest(raw []byte) string {
	digest := sha256.Sum256(append([]byte("143/caller-send-command/v1\x00"), raw...))
	return hex.EncodeToString(digest[:])
}

func TestCallerSendCommandRoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		purpose CallerSendPurpose
		minimal bool
	}{
		{name: "reviewer full command", purpose: CallerSendCodeReviewReviewerDispatch},
		{name: "initial review loop full command", purpose: CallerSendReviewLoopInitialSend},
		{name: "minimal command retains nil pointers", purpose: CallerSendReviewLoopInitialSend, minimal: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := callerCommandInputFixture()
			if tt.minimal {
				input = SendMessageInput{OrgID: input.OrgID, SessionID: input.SessionID, ThreadID: input.ThreadID, Message: " exact message "}
			}
			ownerID := uuid.New()
			command, err := NewCallerSendCommand(tt.purpose, ownerID, input)
			require.NoError(t, err, "constructor should freeze the complete effective command")
			input.ClientMessageID = command.ClientMessageID()
			input.Images = append([]string{}, input.Images...)
			input.References = append(models.SessionInputReferences{}, input.References...)
			input.Commands = append(models.SessionInputCommands{}, input.Commands...)
			input.ResolveReviewCommentIDs = []uuid.UUID{}
			require.Equal(t, input, command.SendInput(), "frozen input must retain every exact field except canonical empty collections and the owner-derived key")
			prefix := "cr-dispatch:v1:"
			if tt.purpose == CallerSendReviewLoopInitialSend {
				prefix = "rl-startup:v1:"
			}
			require.True(t, strings.HasPrefix(command.ClientMessageID(), prefix), "key must use only its closed server purpose namespace")
			keyID, err := uuid.Parse(strings.TrimPrefix(command.ClientMessageID(), prefix))
			require.NoError(t, err, "constructor should retain the supplied canonical owner UUID")
			require.NotEqual(t, uuid.Nil, keyID, "server command identity must never be zero")
			require.Equal(t, prefix+keyID.String(), command.ClientMessageID(), "retained identity must use exact canonical UUID formatting")
			require.Equal(t, ownerID, keyID, "key must name the independently supplied owner, never a newly minted UUID")
			require.Equal(t, ownerID, command.OwnerID(), "immutable command should retain its independent owner identity without a redundant semantic field")
			raw := callerCommandBytes(t, command)
			require.Equal(t, callerCommandDigest(raw), command.Digest(), "digest must be domain-separated lowercase SHA256 over the canonical typed semantic encoding")
			var reformatted bytes.Buffer
			require.NoError(t, json.Indent(&reformatted, raw, "", "  "), "simulate JSONB presentation changes without changing semantics")
			decoded, err := DecodeCallerSendCommand(reformatted.Bytes(), command.Digest(), callerCommandOwner(command))
			require.NoError(t, err, "decoder must hash re-encoded semantics rather than raw JSON presentation")
			require.Equal(t, ownerID, decoded.OwnerID(), "restore must retain the independently supplied owner without deriving one from untrusted JSON")
			require.Equal(t, input, decoded.SendInput(), "decode must restore the complete same command without reminting")
			require.Equal(t, raw, callerCommandBytes(t, decoded), "key-preserving decode must have deterministic encoding")
			require.Equal(t, command.Digest(), decoded.Digest(), "decode must retain the original exact intent digest")
		})
	}
}

func TestCallerSendCommandDigestDomainGolden(t *testing.T) {
	t.Parallel()
	canonical := []byte(`{"version":1,"purpose":"code-review-reviewer-dispatch","org_id":"00000000-0000-4000-8000-000000000001","session_id":"00000000-0000-4000-8000-000000000002","thread_id":"00000000-0000-4000-8000-000000000003","changeset_id":null,"legacy_changeset_policy":"","client_message_id":"cr-dispatch:v1:00000000-0000-4000-8000-000000000004","user_id":null,"message":"golden command","images":[],"references":[],"commands":[],"message_source":"","plan_mode":false,"resolve_review_comment_ids":[],"post_success_action":"","post_success_author_mode":"","continuation_dedupe_key_override":null}`)
	const expected = "b96494769447778797637e06f7e92948bbc977ab77ce8fe287c13ac33ab801c0"
	owner := CallerSendOwnerScope{Purpose: CallerSendCodeReviewReviewerDispatch, OwnerID: uuid.MustParse("00000000-0000-4000-8000-000000000004"),
		OrgID: uuid.MustParse("00000000-0000-4000-8000-000000000001"), SessionID: uuid.MustParse("00000000-0000-4000-8000-000000000002"), ThreadID: uuid.MustParse("00000000-0000-4000-8000-000000000003"),
		ClientMessageID: "cr-dispatch:v1:00000000-0000-4000-8000-000000000004"}
	command, err := DecodeCallerSendCommand(canonical, expected, owner)
	require.NoError(t, err, "exact v1 domain and canonical full envelope must match the independent golden digest")
	require.Equal(t, canonical, callerCommandBytes(t, command), "golden must pin exact field ordering, defaults, and complete canonical encoding")
	require.Equal(t, expected, command.Digest(), "retained digest must include the exact versioned domain and NUL separator")
	proof, err := command.Proof()
	require.NoError(t, err, "golden retained command should create explicit nonsemantic transport")
	withProof := command.SendInput()
	withProof.CallerCommand = proof
	require.NoError(t, ValidateCallerSendCommandProof(withProof), "golden complete input and attached descriptor should validate without changing semantics")
	refrozen, err := NewCallerSendCommand(owner.Purpose, owner.OwnerID, withProof)
	require.NoError(t, err, "golden input can be reconstructed with opaque transport attached")
	require.Equal(t, canonical, callerCommandBytes(t, refrozen), "attached proof must leave the exact golden canonical bytes unchanged")
	require.Equal(t, expected, refrozen.Digest(), "attached proof must leave the independent golden digest unchanged")
	plain := sha256.Sum256(canonical)
	require.NotEqual(t, hex.EncodeToString(plain[:]), command.Digest(), "a plain payload hash must not be accepted as this protocol's command digest")
	_, err = DecodeCallerSendCommand(canonical, hex.EncodeToString(plain[:]), owner)
	require.Error(t, err, "decoder must reject a non-domain-separated digest even when full input and owner match")
}

func TestCallerSendCommandCompleteInputSchema(t *testing.T) {
	t.Parallel()
	// Each future input field needs an explicit retention or restriction decision.
	expected := map[string]reflect.Type{
		"SessionID": reflect.TypeFor[uuid.UUID](), "OrgID": reflect.TypeFor[uuid.UUID](), "ThreadID": reflect.TypeFor[uuid.UUID](),
		"ChangesetID": reflect.TypeFor[*uuid.UUID](), "LegacyChangesetPolicy": reflect.TypeFor[LegacyChangesetPolicy](),
		"ClientMessageID": reflect.TypeFor[string](), "UserID": reflect.TypeFor[*uuid.UUID](), "Message": reflect.TypeFor[string](),
		"Images": reflect.TypeFor[[]string](), "References": reflect.TypeFor[models.SessionInputReferences](), "Commands": reflect.TypeFor[models.SessionInputCommands](),
		"MessageSource": reflect.TypeFor[models.SessionMessageSource](), "PlanMode": reflect.TypeFor[bool](), "ResolveReviewCommentIDs": reflect.TypeFor[[]uuid.UUID](),
		"PostSuccessAction": reflect.TypeFor[string](), "PostSuccessAuthorMode": reflect.TypeFor[string](), "ContinuationDedupeKeyOverride": reflect.TypeFor[*string](),
	}
	// This sole transport field is deliberately excluded from semantic wire
	// data and digest, rather than being a silently omitted new input field.
	excludedTransport := map[string]reflect.Type{"CallerCommand": reflect.TypeFor[*CallerSendCommandProof]()}
	for name, fieldType := range excludedTransport {
		expected[name] = fieldType
	}
	actual := map[string]reflect.Type{}
	inputType := reflect.TypeFor[SendMessageInput]()
	for i := 0; i < inputType.NumField(); i++ {
		field := inputType.Field(i)
		actual[field.Name] = field.Type
	}
	require.Equal(t, expected, actual, "codec completeness must fail when the SendMessageInput schema gains or changes a field")
	proofField, ok := inputType.FieldByName("CallerCommand")
	require.True(t, ok, "exact field classification must include the explicit nonsemantic caller transport")
	require.Equal(t, "-", proofField.Tag.Get("json"), "caller command transport must be excluded from public JSON")
	retained := callerCommandBytes(t, mustCallerCommand(t, callerCommandInputFixture()))
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(retained, &fields), "inspect explicit retained field names")
	expectedFields := []string{"version", "purpose", "org_id", "session_id", "thread_id", "changeset_id", "legacy_changeset_policy", "client_message_id", "user_id", "message", "images", "references", "commands", "message_source", "plan_mode", "resolve_review_comment_ids", "post_success_action", "post_success_author_mode", "continuation_dedupe_key_override"}
	actualFields := make([]string, 0, len(fields))
	for field := range fields {
		actualFields = append(actualFields, field)
	}
	require.ElementsMatch(t, expectedFields, actualFields, "wire schema must explicitly retain every input field, including the two restricted constant fields")
}

func mustCallerCommand(t *testing.T, input SendMessageInput) CallerSendCommand {
	t.Helper()
	command, err := NewCallerSendCommand(CallerSendCodeReviewReviewerDispatch, uuid.New(), input)
	require.NoError(t, err, "prepare complete immutable caller command fixture")
	return command
}

func TestCallerSendCommandImmutability(t *testing.T) {
	t.Parallel()
	input := callerCommandInputFixture()
	command := mustCallerCommand(t, input)
	raw, digest := callerCommandBytes(t, command), command.Digest()
	input.Images[0], input.References[0].Path, input.Commands[0].Arguments = "mutated", "mutated", "mutated"
	*input.UserID, *input.ChangesetID, *input.ContinuationDedupeKeyOverride = uuid.New(), uuid.New(), "mutated"
	require.Equal(t, raw, callerCommandBytes(t, command), "constructor must defensively copy all input slices and optional pointers")
	returned := command.SendInput()
	returned.Images[0], returned.References[0].Path, returned.Commands[0].Arguments = "mutated", "mutated", "mutated"
	*returned.UserID, *returned.ChangesetID, *returned.ContinuationDedupeKeyOverride = uuid.New(), uuid.New(), "mutated"
	encoded := callerCommandBytes(t, command)
	encoded[0] = '!'
	require.Equal(t, raw, callerCommandBytes(t, command), "every getter must return fresh mutable storage rather than exposing retained intent")
	require.Equal(t, digest, command.Digest(), "mutating caller-owned copies must not alter retained digest")
	exposed, err := json.Marshal(command) //nolint:staticcheck // Intentionally verify sealed commands disclose no retained input fields.
	require.NoError(t, err, "opaque command should have no implicit public JSON fields")
	require.Equal(t, `{}`, string(exposed), "public JSON marshaling must not expose the frozen prompt, key, scope, or digest")
}

func TestCallerSendCommandCollectionsAndPointers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                           string
		emptyCollections, emptyPointer bool
	}{
		{name: "nil collections and pointer"},
		{name: "empty collections normalize", emptyCollections: true},
		{name: "empty optional pointer is not nil", emptyPointer: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := mustCallerCommand(t, SendMessageInput{OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), Message: "message"})
			var wire callerSendEnvelope
			require.NoError(t, json.Unmarshal(callerCommandBytes(t, base), &wire), "decode typed fixture for a key-preserving semantic comparison")
			wire.Images, wire.References, wire.Commands, wire.ResolveReviewCommentIDs = nil, nil, nil, nil
			if tt.emptyCollections {
				wire.Images, wire.References, wire.Commands, wire.ResolveReviewCommentIDs = []string{}, models.SessionInputReferences{}, models.SessionInputCommands{}, []uuid.UUID{}
			}
			if tt.emptyPointer {
				empty := ""
				wire.ContinuationDedupeKeyOverride = &empty
			}
			encoded, err := json.Marshal(wire)
			require.NoError(t, err, "encode raw equivalent or distinct optional values")
			expectedDigest := base.Digest()
			if tt.emptyPointer {
				wire.Images, wire.References, wire.Commands, wire.ResolveReviewCommentIDs = []string{}, models.SessionInputReferences{}, models.SessionInputCommands{}, []uuid.UUID{}
				canonical, marshalErr := json.Marshal(wire)
				require.NoError(t, marshalErr, "encode canonical distinct empty-pointer value")
				expectedDigest = callerCommandDigest(canonical)
				require.NotEqual(t, base.Digest(), expectedDigest, "nil and explicitly empty optional pointer must remain different frozen intent")
			}
			decoded, err := DecodeCallerSendCommand(encoded, expectedDigest, callerCommandOwner(base))
			require.NoError(t, err, "decoder should normalize only nil versus empty collections")
			require.Equal(t, expectedDigest, decoded.Digest(), "canonical digest should preserve the optional pointer distinction")
			if tt.emptyPointer {
				require.NotNil(t, decoded.SendInput().ContinuationDedupeKeyOverride, "explicit empty optional pointer must survive restore")
				require.Equal(t, "", *decoded.SendInput().ContinuationDedupeKeyOverride, "empty pointer content must remain exact")
			} else {
				require.Nil(t, decoded.SendInput().ContinuationDedupeKeyOverride, "absent optional pointer must remain absent")
				require.Equal(t, callerCommandBytes(t, base), callerCommandBytes(t, decoded), "empty collection representations must produce the same canonical bytes")
			}
		})
	}
}

func TestCallerSendCommandDifferentIdentity(t *testing.T) {
	t.Parallel()
	input := callerCommandInputFixture()
	ownerID := uuid.New()
	first, err := NewCallerSendCommand(CallerSendCodeReviewReviewerDispatch, ownerID, input)
	require.NoError(t, err, "prepare deterministic command for the existing owner")
	second, err := NewCallerSendCommand(CallerSendCodeReviewReviewerDispatch, ownerID, input)
	require.NoError(t, err, "rebuild must accept the same exact owner and original complete input")
	require.Equal(t, callerCommandBytes(t, first), callerCommandBytes(t, second), "same owner, purpose and full input must rebuild deterministically without hidden entropy")
	require.Equal(t, first.Digest(), second.Digest(), "same-owner reconstruction must not generate a different digest")
	distinct, err := NewCallerSendCommand(CallerSendCodeReviewReviewerDispatch, uuid.New(), input)
	require.NoError(t, err, "different prepared owner may retain a distinct command")
	other, err := NewCallerSendCommand(CallerSendReviewLoopInitialSend, ownerID, input)
	require.NoError(t, err, "same complete text can be a distinct closed-purpose command")
	require.NotEqual(t, first.ClientMessageID(), distinct.ClientMessageID(), "different owner identities must not share a command key")
	require.NotEqual(t, first.Digest(), distinct.Digest(), "owner key changes must alter the complete command digest")
	require.NotEqual(t, first.ClientMessageID(), other.ClientMessageID(), "purpose namespaces must not overlap")
	require.NotEqual(t, first.Digest(), other.Digest(), "same text under a different purpose must remain distinct")
}

func TestCallerSendCommandRetainsExpectedOwnerKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		purpose CallerSendPurpose
		prefix  string
	}{
		{name: "reviewer", purpose: CallerSendCodeReviewReviewerDispatch, prefix: "cr-dispatch:v1:"},
		{name: "loop startup", purpose: CallerSendReviewLoopInitialSend, prefix: "rl-startup:v1:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ownerID := uuid.New()
			input := callerCommandInputFixture()
			input.ClientMessageID = tt.prefix + ownerID.String()
			command, err := NewCallerSendCommand(tt.purpose, ownerID, input)
			require.NoError(t, err, "an exact preassigned owner-derived key must be retained rather than rejected or replaced")
			require.Equal(t, input.ClientMessageID, command.ClientMessageID(), "owner key must survive reconstruction unchanged")
			require.Equal(t, ownerID, command.OwnerID(), "preassigned key cannot replace independently supplied owner identity")
		})
	}
}

func TestCallerSendCommandRejectsWrongOwnerOrKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                                             string
		zeroOwner, differentOwnerKey, differentPurposeKey, whitespaceKey bool
	}{
		{name: "zero owner", zeroOwner: true},
		{name: "different prepared owner", differentOwnerKey: true},
		{name: "different purpose namespace", differentPurposeKey: true},
		{name: "nonempty whitespace key", whitespaceKey: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ownerID := uuid.New()
			input := callerCommandInputFixture()
			if tt.zeroOwner {
				ownerID = uuid.Nil
			}
			if tt.differentOwnerKey {
				input.ClientMessageID = "cr-dispatch:v1:" + uuid.NewString()
			}
			if tt.differentPurposeKey {
				input.ClientMessageID = "rl-startup:v1:" + ownerID.String()
			}
			if tt.whitespaceKey {
				input.ClientMessageID = " "
			}
			command, err := NewCallerSendCommand(CallerSendCodeReviewReviewerDispatch, ownerID, input)
			require.Error(t, err, "zero owner or an independently mismatched supplied key must be refused rather than replaced")
			require.Equal(t, CallerSendCommand{}, command, "owner/key refusal must return no retained command")
		})
	}
}

func TestCallerSendCommandZeroValue(t *testing.T) {
	t.Parallel()
	var command CallerSendCommand
	raw, err := command.Encode()
	require.Error(t, err, "zero command must not masquerade as retained validated intent")
	require.Nil(t, raw, "zero command must expose no manufactured envelope")
	require.Equal(t, "", command.Digest(), "zero getter must not mint or compute a usable identity")
	require.Equal(t, "", command.ClientMessageID(), "zero getter must not manufacture a command key")
	require.Equal(t, uuid.Nil, command.OwnerID(), "zero getter must not manufacture an owner identity")
	require.Equal(t, CallerSendPurpose(""), command.Purpose(), "zero getter must not infer a purpose")
	require.Equal(t, SendMessageInput{}, command.SendInput(), "zero projection must remain visibly unusable and authority-free")
	_, err = DecodeCallerSendCommand(raw, command.Digest(), CallerSendOwnerScope{})
	require.Error(t, err, "empty command cannot be restored through decoder defaults")
}

type callerCommandSemanticMutation struct {
	name   string
	mutate func(*callerSendEnvelope)
}

func callerCommandSemanticMutationFixtures() []callerCommandSemanticMutation {
	return []callerCommandSemanticMutation{
		{name: "purpose", mutate: func(p *callerSendEnvelope) {
			p.ClientMessageID = strings.Replace(p.ClientMessageID, "cr-dispatch:v1:", "rl-startup:v1:", 1)
			p.Purpose = CallerSendReviewLoopInitialSend
		}},
		{name: "organization", mutate: func(p *callerSendEnvelope) { p.OrgID = uuid.New() }},
		{name: "session", mutate: func(p *callerSendEnvelope) { p.SessionID = uuid.New() }},
		{name: "lane", mutate: func(p *callerSendEnvelope) { p.ThreadID = uuid.New() }},
		{name: "client identity", mutate: func(p *callerSendEnvelope) { p.ClientMessageID = "cr-dispatch:v1:" + uuid.NewString() }},
		{name: "user", mutate: func(p *callerSendEnvelope) { id := uuid.New(); p.UserID = &id }},
		{name: "user presence", mutate: func(p *callerSendEnvelope) { p.UserID = nil }},
		{name: "target", mutate: func(p *callerSendEnvelope) { id := uuid.New(); p.ChangesetID = &id }},
		{name: "target presence", mutate: func(p *callerSendEnvelope) { p.ChangesetID = nil }},
		{name: "message", mutate: func(p *callerSendEnvelope) { p.Message += " " }},
		{name: "plan mode", mutate: func(p *callerSendEnvelope) { p.PlanMode = false }},
		{name: "image", mutate: func(p *callerSendEnvelope) { p.Images[0] = "different-image" }},
		{name: "image order", mutate: func(p *callerSendEnvelope) { p.Images[0], p.Images[1] = p.Images[1], p.Images[0] }},
		{name: "reference order", mutate: func(p *callerSendEnvelope) { p.References[0], p.References[1] = p.References[1], p.References[0] }},
		{name: "reference kind", mutate: func(p *callerSendEnvelope) { p.References[0].Kind = models.SessionInputReferenceKindDirectory }},
		{name: "reference token", mutate: func(p *callerSendEnvelope) { p.References[0].Token += "-different" }},
		{name: "reference path", mutate: func(p *callerSendEnvelope) { p.References[0].Path += "-different" }},
		{name: "reference id", mutate: func(p *callerSendEnvelope) { p.References[0].ID += "-different" }},
		{name: "reference display", mutate: func(p *callerSendEnvelope) { p.References[0].Display += "-different" }},
		{name: "command order", mutate: func(p *callerSendEnvelope) { p.Commands[0], p.Commands[1] = p.Commands[1], p.Commands[0] }},
		{name: "command kind", mutate: func(p *callerSendEnvelope) { p.Commands[0].Kind = "" }},
		{name: "command agent", mutate: func(p *callerSendEnvelope) { p.Commands[0].AgentType = models.AgentTypeClaudeCode }},
		{name: "command name", mutate: func(p *callerSendEnvelope) { p.Commands[0].Name += "-different" }},
		{name: "command token", mutate: func(p *callerSendEnvelope) { p.Commands[0].Token += "-different" }},
		{name: "command display", mutate: func(p *callerSendEnvelope) { p.Commands[0].Display += "-different" }},
		{name: "command description", mutate: func(p *callerSendEnvelope) { p.Commands[0].Description += "-different" }},
		{name: "command arguments", mutate: func(p *callerSendEnvelope) { p.Commands[0].Arguments += " " }},
		{name: "command source", mutate: func(p *callerSendEnvelope) { p.Commands[0].Source = models.SessionInputCommandSourceProject }},
		{name: "message source", mutate: func(p *callerSendEnvelope) { p.MessageSource = "" }},
		{name: "post success", mutate: func(p *callerSendEnvelope) { p.PostSuccessAction = "" }},
		{name: "post success author", mutate: func(p *callerSendEnvelope) { p.PostSuccessAuthorMode = "app" }},
		{name: "continuation override", mutate: func(p *callerSendEnvelope) { *p.ContinuationDedupeKeyOverride += " " }},
		{name: "continuation presence", mutate: func(p *callerSendEnvelope) { p.ContinuationDedupeKeyOverride = nil }},
	}
}

func TestCallerSendCommandDigestBindsEverySemanticField(t *testing.T) {
	t.Parallel()
	for _, tt := range callerCommandSemanticMutationFixtures() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := mustCallerCommand(t, callerCommandInputFixture())
			var payload callerSendEnvelope
			require.NoError(t, json.Unmarshal(callerCommandBytes(t, base), &payload), "prepare complete typed retained command")
			tt.mutate(&payload)
			raw, err := json.Marshal(payload)
			require.NoError(t, err, "encode valid changed semantic intent")
			digest := callerCommandDigest(raw)
			require.NotEqual(t, base.Digest(), digest, "each retained field and collection order must affect command identity")
			ownerID, err := uuid.Parse(payload.ClientMessageID[strings.LastIndex(payload.ClientMessageID, ":")+1:])
			require.NoError(t, err, "valid changed fixture must explicitly prepare its corresponding independent owner")
			owner := CallerSendOwnerScope{Purpose: payload.Purpose, OwnerID: ownerID, OrgID: payload.OrgID, SessionID: payload.SessionID, ThreadID: payload.ThreadID, ClientMessageID: payload.ClientMessageID}
			decoded, err := DecodeCallerSendCommand(raw, digest, owner)
			require.NoError(t, err, "a correctly bound changed command must preserve its complete new semantics")
			require.Equal(t, payload.sendInput(), decoded.SendInput(), "restore must retain each mutated semantic field, not merely the text")
			_, err = DecodeCallerSendCommand(raw, base.Digest(), owner)
			require.Error(t, err, "the old digest must not authenticate changed intent even with matching owner scope")
		})
	}
}

func TestCallerSendCommandDecodeRejectsInvalidRetainedSemantics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*callerSendEnvelope)
	}{
		{name: "unrecognized purpose", mutate: func(p *callerSendEnvelope) {
			p.Purpose = "unrecognized"
			p.ClientMessageID = "unrecognized:" + uuid.NewString()
		}},
		{name: "unrecognized version", mutate: func(p *callerSendEnvelope) { p.Version = 2 }},
		{name: "arbitrary key", mutate: func(p *callerSendEnvelope) { p.ClientMessageID = "arbitrary" }},
		{name: "wrong namespace", mutate: func(p *callerSendEnvelope) {
			p.ClientMessageID = "rl-startup:v1:" + uuid.NewString()
		}},
		{name: "zero key UUID", mutate: func(p *callerSendEnvelope) { p.ClientMessageID = "cr-dispatch:v1:" + uuid.Nil.String() }},
		{name: "noncanonical key UUID", mutate: func(p *callerSendEnvelope) {
			p.ClientMessageID = "cr-dispatch:v1:" + strings.ToUpper("12345678-abcd-4321-9876-123456789abc")
		}},
		{name: "undashed key UUID", mutate: func(p *callerSendEnvelope) {
			p.ClientMessageID = "cr-dispatch:v1:" + strings.ReplaceAll(uuid.NewString(), "-", "")
		}},
		{name: "NUL key", mutate: func(p *callerSendEnvelope) { p.ClientMessageID += "\x00" }},
		{name: "comment mutation", mutate: func(p *callerSendEnvelope) { p.ResolveReviewCommentIDs = []uuid.UUID{uuid.New()} }},
		{name: "legacy policy", mutate: func(p *callerSendEnvelope) { p.LegacyChangesetPolicy = LegacyChangesetUnconfigured }},
		{name: "unknown message source", mutate: func(p *callerSendEnvelope) { p.MessageSource = "unrecognized" }},
		{name: "NUL nested command", mutate: func(p *callerSendEnvelope) { p.Commands[0].Description = "malformed\x00description" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := mustCallerCommand(t, callerCommandInputFixture())
			var payload callerSendEnvelope
			require.NoError(t, json.Unmarshal(callerCommandBytes(t, base), &payload), "prepare typed retained fixture")
			tt.mutate(&payload)
			raw, err := json.Marshal(payload)
			require.NoError(t, err, "encode deliberately invalid retained shape")
			owner := CallerSendOwnerScope{Purpose: payload.Purpose, OwnerID: base.OwnerID(), OrgID: payload.OrgID, SessionID: payload.SessionID, ThreadID: payload.ThreadID, ClientMessageID: payload.ClientMessageID}
			command, err := DecodeCallerSendCommand(raw, callerCommandDigest(raw), owner)
			require.Error(t, err, "a matching digest and owner cannot authorize invalid retained semantics")
			require.Equal(t, CallerSendCommand{}, command, "invalid retained intent must not produce a usable command")
		})
	}
}

func TestCallerSendCommandRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*SendMessageInput)
	}{
		{name: "preassigned key", mutate: func(in *SendMessageInput) { in.ClientMessageID = "preassigned" }},
		{name: "zero organization", mutate: func(in *SendMessageInput) { in.OrgID = uuid.Nil }},
		{name: "zero session", mutate: func(in *SendMessageInput) { in.SessionID = uuid.Nil }},
		{name: "zero lane", mutate: func(in *SendMessageInput) { in.ThreadID = uuid.Nil }},
		{name: "zero optional user", mutate: func(in *SendMessageInput) { zero := uuid.Nil; in.UserID = &zero }},
		{name: "zero optional target", mutate: func(in *SendMessageInput) { zero := uuid.Nil; in.ChangesetID = &zero }},
		{name: "review comment mutation", mutate: func(in *SendMessageInput) { in.ResolveReviewCommentIDs = []uuid.UUID{uuid.New()} }},
		{name: "legacy target policy", mutate: func(in *SendMessageInput) { in.LegacyChangesetPolicy = LegacyChangesetRequireMaterialized }},
		{name: "unknown message source", mutate: func(in *SendMessageInput) { in.MessageSource = "unrecognized" }},
		{name: "unknown post success", mutate: func(in *SendMessageInput) { in.PostSuccessAction = "unrecognized" }},
		{name: "unknown author", mutate: func(in *SendMessageInput) { in.PostSuccessAuthorMode = "unrecognized" }},
		{name: "unknown reference", mutate: func(in *SendMessageInput) { in.References[0].Kind = "unrecognized" }},
		{name: "unknown command source", mutate: func(in *SendMessageInput) { in.Commands[0].Source = "unrecognized" }},
		{name: "unknown command agent", mutate: func(in *SendMessageInput) { in.Commands[0].AgentType = "unrecognized" }},
		{name: "invalid command token", mutate: func(in *SendMessageInput) { in.Commands[0].Token = "not-a-command" }},
		{name: "empty image", mutate: func(in *SendMessageInput) { in.Images[0] = " " }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := callerCommandInputFixture()
			tt.mutate(&input)
			command, err := NewCallerSendCommand(CallerSendCodeReviewReviewerDispatch, uuid.New(), input)
			require.Error(t, err, "invalid or unsupported command must be refused without retaining a partial command")
			require.Equal(t, CallerSendCommand{}, command, "failed validation must not return a usable command")
		})
	}
	for _, purpose := range []CallerSendPurpose{"", "arbitrary", "review-loop-initial-send:extra"} {
		t.Run("purpose "+string(purpose), func(t *testing.T) {
			t.Parallel()
			_, err := NewCallerSendCommand(purpose, uuid.New(), callerCommandInputFixture())
			require.Error(t, err, "only closed server-owned purposes may derive a command")
		})
	}
}

func TestCallerSendCommandRejectsMalformedText(t *testing.T) {
	t.Parallel()
	setters := []struct {
		name string
		set  func(*SendMessageInput, string)
	}{
		{name: "message", set: func(in *SendMessageInput, s string) { in.Message = s }},
		{name: "image", set: func(in *SendMessageInput, s string) { in.Images[0] = s }},
		{name: "reference kind", set: func(in *SendMessageInput, s string) { in.References[0].Kind = models.SessionInputReferenceKind(s) }},
		{name: "reference token", set: func(in *SendMessageInput, s string) { in.References[0].Token = s }},
		{name: "reference path", set: func(in *SendMessageInput, s string) { in.References[0].Path = s }},
		{name: "reference id", set: func(in *SendMessageInput, s string) { in.References[0].ID = s }},
		{name: "reference display", set: func(in *SendMessageInput, s string) { in.References[0].Display = s }},
		{name: "command kind", set: func(in *SendMessageInput, s string) { in.Commands[0].Kind = s }},
		{name: "command agent", set: func(in *SendMessageInput, s string) { in.Commands[0].AgentType = models.AgentType(s) }},
		{name: "command name", set: func(in *SendMessageInput, s string) { in.Commands[0].Name = s }},
		{name: "command token", set: func(in *SendMessageInput, s string) { in.Commands[0].Token = "/" + s }},
		{name: "command display", set: func(in *SendMessageInput, s string) { in.Commands[0].Display = s }},
		{name: "command description", set: func(in *SendMessageInput, s string) { in.Commands[0].Description = s }},
		{name: "command arguments", set: func(in *SendMessageInput, s string) { in.Commands[0].Arguments = s }},
		{name: "command source", set: func(in *SendMessageInput, s string) { in.Commands[0].Source = models.SessionInputCommandSource(s) }},
		{name: "message source", set: func(in *SendMessageInput, s string) { in.MessageSource = models.SessionMessageSource(s) }},
		{name: "post success", set: func(in *SendMessageInput, s string) { in.PostSuccessAction = s }},
		{name: "author", set: func(in *SendMessageInput, s string) { in.PostSuccessAuthorMode = s }},
		{name: "continuation", set: func(in *SendMessageInput, s string) { in.ContinuationDedupeKeyOverride = &s }},
	}
	for _, setter := range setters {
		t.Run(setter.name, func(t *testing.T) {
			t.Parallel()
			for _, value := range []struct{ name, text string }{{name: "NUL", text: "a\x00b"}, {name: "invalid UTF8", text: string([]byte{'a', 0xff, 'b'})}} {
				t.Run(value.name, func(t *testing.T) {
					t.Parallel()
					input := callerCommandInputFixture()
					setter.set(&input, value.text)
					_, err := NewCallerSendCommand(CallerSendCodeReviewReviewerDispatch, uuid.New(), input)
					require.Error(t, err, "malformed nested text must be rejected before JSON can replace or sanitize it")
				})
			}
		})
	}
}

func TestCallerSendCommandDecodeRejectsInvalidEnvelope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func([]byte, string, CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope)
	}{
		{name: "wrong digest", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			return raw, strings.Repeat("0", 64), owner
		}},
		{name: "uppercase digest", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			return raw, strings.ToUpper(digest), owner
		}},
		{name: "short digest", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			return raw, digest[:62], owner
		}},
		{name: "nonhex digest", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			return raw, strings.Repeat("g", 64), owner
		}},
		{name: "owner organization", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			owner.OrgID = uuid.New()
			return raw, digest, owner
		}},
		{name: "owner session", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			owner.SessionID = uuid.New()
			return raw, digest, owner
		}},
		{name: "owner lane", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			owner.ThreadID = uuid.New()
			return raw, digest, owner
		}},
		{name: "zero owner identity", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			owner.OwnerID = uuid.Nil
			return raw, digest, owner
		}},
		{name: "wrong owner identity with original key", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			owner.OwnerID = uuid.New()
			return raw, digest, owner
		}},
		{name: "different coherent owner identity and key", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			owner.OwnerID = uuid.New()
			owner.ClientMessageID = "cr-dispatch:v1:" + owner.OwnerID.String()
			return raw, digest, owner
		}},
		{name: "owner purpose", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			owner.Purpose = CallerSendReviewLoopInitialSend
			return raw, digest, owner
		}},
		{name: "different coherent purpose and key", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			owner.Purpose = CallerSendReviewLoopInitialSend
			owner.ClientMessageID = "rl-startup:v1:" + owner.OwnerID.String()
			return raw, digest, owner
		}},
		{name: "owner key", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			owner.ClientMessageID = "cr-dispatch:v1:" + uuid.NewString()
			return raw, digest, owner
		}},
		{name: "unknown field", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			return append([]byte(`{"unexpected":true,`), raw[1:]...), digest, owner
		}},
		{name: "case folded field", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			return bytes.Replace(raw, []byte(`"version":1`), []byte(`"Version":1`), 1), digest, owner
		}},
		{name: "missing restricted field", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			return bytes.Replace(raw, []byte(`"legacy_changeset_policy":"",`), nil, 1), digest, owner
		}},
		{name: "nested unknown field", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			return bytes.Replace(raw, []byte(`"kind":"file"`), []byte(`"unexpected":true,"kind":"file"`), 1), digest, owner
		}},
		{name: "nested case folded field", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			return bytes.Replace(raw, []byte(`"kind":"file"`), []byte(`"Kind":"file"`), 1), digest, owner
		}},
		{name: "duplicate field", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			return append([]byte(`{"version":1,`), raw[1:]...), digest, owner
		}},
		{name: "trailing object", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			return append(raw, []byte(` {}`)...), digest, owner
		}},
		{name: "trailing garbage", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			return append(raw, '!'), digest, owner
		}},
		{name: "unsupported version", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			return bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":2`), 1), digest, owner
		}},
		{name: "corrupted content", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			return bytes.Replace(raw, []byte("original message"), []byte("modified message"), 1), digest, owner
		}},
		{name: "invalid raw UTF8", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			return bytes.Replace(raw, []byte("original message"), []byte{0xff}, 1), digest, owner
		}},
		{name: "null object", change: func(raw []byte, digest string, owner CallerSendOwnerScope) ([]byte, string, CallerSendOwnerScope) {
			return []byte(`null`), digest, owner
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := mustCallerCommand(t, callerCommandInputFixture())
			raw, digest, owner := tt.change(callerCommandBytes(t, base), base.Digest(), callerCommandOwner(base))
			command, err := DecodeCallerSendCommand(raw, digest, owner)
			require.Error(t, err, "invalid envelope, digest, or independent owner binding must fail closed")
			require.Equal(t, CallerSendCommand{}, command, "failed decoding must not return a usable or reminted command")
		})
	}
}

func TestCallerSendCommandDecodeRejectsNullRequiredScalars(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, original string }{
		{name: "boolean", original: `"plan_mode":false`},
		{name: "empty source", original: `"message_source":""`},
		{name: "empty option", original: `"post_success_action":""`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := SendMessageInput{OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), Message: "exact message"}
			command := mustCallerCommand(t, input)
			field := strings.SplitN(tt.original, ":", 2)[0]
			raw := bytes.Replace(callerCommandBytes(t, command), []byte(tt.original), []byte(field+`:null`), 1)
			_, err := DecodeCallerSendCommand(raw, command.Digest(), callerCommandOwner(command))
			require.Error(t, err, "null must not replacement-normalize into a required scalar's zero value")
		})
	}
}

func TestCallerSendCommandUnicodeDecode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, escaped, decoded string
		valid                  bool
	}{
		{name: "unpaired high", escaped: `\ud800`, decoded: "�"},
		{name: "unpaired low", escaped: `\udc00`, decoded: "�"},
		{name: "high followed by text", escaped: `\ud800x`, decoded: "�x"},
		{name: "valid surrogate pair", escaped: `\ud83d\ude00`, decoded: "😀", valid: true},
		{name: "escaped literal backslash", escaped: `\\ud800`, decoded: `\ud800`, valid: true},
		{name: "valid replacement character", escaped: `\ufffd`, decoded: "�", valid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := callerCommandInputFixture()
			input.Message = tt.decoded
			base := mustCallerCommand(t, input)
			original, err := json.Marshal(tt.decoded)
			require.NoError(t, err, "encode decoded semantic reference value")
			raw := bytes.Replace(callerCommandBytes(t, base), append([]byte(`"message":`), original...), []byte(`"message":"`+tt.escaped+`"`), 1)
			decoded, err := DecodeCallerSendCommand(raw, base.Digest(), callerCommandOwner(base))
			if !tt.valid {
				require.Error(t, err, "unpaired UTF16 escape must not be silently replaced into a matching command")
				return
			}
			require.NoError(t, err, "valid JSON Unicode representation must preserve semantic identity")
			require.Equal(t, base.SendInput(), decoded.SendInput(), "valid alternate escapes must preserve complete exact input")
		})
	}
}

func TestCallerSendCommandEncodedLimit(t *testing.T) {
	t.Parallel()
	baseInput := SendMessageInput{OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), Message: "x"}
	base := mustCallerCommand(t, baseInput)
	overhead := len(callerCommandBytes(t, base)) - 1
	for _, tt := range []struct {
		name  string
		delta int
		valid bool
	}{{name: "exact cap", valid: true}, {name: "one byte over", delta: 1}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := baseInput
			input.Message = strings.Repeat("x", CallerSendCommandMaxEncodedBytes-overhead+tt.delta)
			command, err := NewCallerSendCommand(CallerSendCodeReviewReviewerDispatch, uuid.New(), input)
			if !tt.valid {
				require.Error(t, err, "canonical compact envelope over the codec cap must be refused")
				return
			}
			require.NoError(t, err, "canonical envelope exactly at the codec cap should remain valid")
			raw := callerCommandBytes(t, command)
			require.Equal(t, CallerSendCommandMaxEncodedBytes, len(raw), "limit must measure encoded bytes, not rune count or message-only size")
			decoded, err := DecodeCallerSendCommand(raw, command.Digest(), callerCommandOwner(command))
			require.NoError(t, err, "bounded exact-cap retained command should restore without new identity")
			require.Equal(t, command.Digest(), decoded.Digest(), "exact-cap decoding must preserve intent")
			_, err = DecodeCallerSendCommand(append(raw, ' '), command.Digest(), callerCommandOwner(command))
			require.Error(t, err, "decoder must bound raw input too, even when extra presentation whitespace changes no semantics")
		})
	}
}

func TestCallerSendCommandSizePreflight(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*SendMessageInput)
	}{
		{name: "huge raw message", mutate: func(in *SendMessageInput) { in.Message = strings.Repeat("x", CallerSendCommandMaxEncodedBytes+1) }},
		{name: "aggregate images", mutate: func(in *SendMessageInput) {
			value := strings.Repeat("x", CallerSendCommandMaxEncodedBytes/2+1)
			in.Images = []string{value, value}
		}},
		{name: "aggregate reference fields", mutate: func(in *SendMessageInput) {
			value := strings.Repeat("x", CallerSendCommandMaxEncodedBytes/2+1)
			in.References[0].Token, in.References[0].Path = value, value
		}},
		{name: "aggregate command fields", mutate: func(in *SendMessageInput) {
			value := strings.Repeat("x", CallerSendCommandMaxEncodedBytes/2+1)
			in.Commands[0].Description, in.Commands[0].Arguments = value, value
		}},
		{name: "huge continuation", mutate: func(in *SendMessageInput) {
			value := strings.Repeat("x", CallerSendCommandMaxEncodedBytes+1)
			in.ContinuationDedupeKeyOverride = &value
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := callerCommandInputFixture()
			tt.mutate(&input)
			require.False(t, callerSendInputFitsSizeLowerBound(CallerSendCodeReviewReviewerDispatch, input), "obviously oversized raw semantic input must be rejected by the pre-marshal lower-bound check itself")
			command, err := NewCallerSendCommand(CallerSendCodeReviewReviewerDispatch, uuid.New(), input)
			require.Error(t, err, "constructor must refuse oversized input without returning partial retained intent")
			require.Equal(t, CallerSendCommand{}, command, "lower-bound refusal must not return a usable command")
		})
	}
}

func TestCallerSendCollectionSizePreflightOverflowSafe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                   string
		budget, count, minimum int
		fits                   bool
		remaining              int
	}{
		{name: "empty exact brackets", budget: 2, minimum: 2, fits: true},
		{name: "two quoted values exact", budget: 7, count: 2, minimum: 2, fits: true},
		{name: "one byte short", budget: 6, count: 2, minimum: 2},
		{name: "bounded remainder", budget: 9, count: 2, minimum: 2, fits: true, remaining: 2},
		{name: "huge count cannot overflow", budget: CallerSendCommandMaxEncodedBytes, count: int(^uint(0) >> 1), minimum: 2},
		{name: "huge element minimum cannot overflow", budget: CallerSendCommandMaxEncodedBytes, count: 2, minimum: int(^uint(0) >> 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			remaining := tt.budget
			fits := callerSendCollectionFitsSizeLowerBound(&remaining, tt.count, tt.minimum)
			require.Equal(t, tt.fits, fits, "collection punctuation and minimum element bytes must be charged without overflow")
			if fits {
				require.Equal(t, tt.remaining, remaining, "accepted collection lower bound must leave the exact remaining byte budget")
			}
		})
	}
}
