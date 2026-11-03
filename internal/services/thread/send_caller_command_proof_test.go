package thread

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func callerCommandProofFixture(t *testing.T, command CallerSendCommand) *CallerSendCommandProof {
	t.Helper()
	proof, err := command.Proof()
	require.NoError(t, err, "initialized retained command should produce a fresh opaque transport descriptor")
	require.NotNil(t, proof, "valid retained intent must produce an explicit proof value")
	return proof
}

func requireCallerProofMismatch(t *testing.T, input SendMessageInput) {
	t.Helper()
	err := ValidateCallerSendCommandProof(input)
	require.ErrorIs(t, err, ErrCallerCommandMismatch, "supplied mismatch must be a known prewrite refusal, never acceptance or an unknown outcome")
	var mismatch *CallerCommandMismatchError
	require.ErrorAs(t, err, &mismatch, "caller mismatch should retain its explicit typed classification")
	require.Equal(t, "caller send command proof mismatch", err.Error(), "mismatch errors must not disclose retained input, key, scope, or digest")
}

func TestCallerSendCommandProofAbsentPreservesBehavior(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input func() SendMessageInput
	}{
		{name: "zero legacy input", input: func() SendMessageInput { return SendMessageInput{} }},
		{name: "ordinary semantic input", input: callerCommandInputFixture},
		{name: "unrelated malformed legacy input", input: func() SendMessageInput { return SendMessageInput{Message: "\x00", ClientMessageID: "arbitrary"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := tt.input()
			require.NoError(t, ValidateCallerSendCommandProof(input), "absent transport must not add new admission or legacy validation policy")
			require.Nil(t, cloneSendMessageInput(input).CallerCommand, "absent optional transport must remain absent after cloning")
		})
	}
}

func TestCallerSendCommandProofPreservesSemanticGolden(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		purpose CallerSendPurpose
	}{
		{name: "reviewer dispatch", purpose: CallerSendCodeReviewReviewerDispatch},
		{name: "loop initial send", purpose: CallerSendReviewLoopInitialSend},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ownerID := uuid.New()
			command, err := NewCallerSendCommand(tt.purpose, ownerID, callerCommandInputFixture())
			require.NoError(t, err, "prepare complete retained semantic command")
			proof := callerCommandProofFixture(t, command)
			require.Equal(t, 1, proof.Version(), "proof must carry only the supported transport version")
			require.Equal(t, tt.purpose, proof.Purpose(), "proof purpose should identify the retained command family")
			require.Equal(t, ownerID, proof.OwnerID(), "proof owner must come from independent command preparation")
			require.Equal(t, command.Digest(), proof.Digest(), "proof must name the complete original semantic digest")
			input := command.SendInput()
			input.CallerCommand = proof
			require.NoError(t, ValidateCallerSendCommandProof(input), "matching proof should verify exact effective input without granting admission")
			refrozen, err := NewCallerSendCommand(tt.purpose, ownerID, input)
			require.NoError(t, err, "canonical command construction should ignore nonsemantic proof transport")
			require.Equal(t, callerCommandBytes(t, command), callerCommandBytes(t, refrozen), "attached transport must leave all canonical semantic bytes unchanged")
			require.Equal(t, command.Digest(), refrozen.Digest(), "proof must never hash itself recursively")
			require.Nil(t, refrozen.SendInput().CallerCommand, "semantic retained projection must not accidentally retain recursive transport")
			public, err := json.Marshal(input)
			require.NoError(t, err, "encode the existing input projection")
			require.NotContains(t, string(public), "CallerCommand", "private proof transport must be omitted from public input JSON")
			require.NotContains(t, string(public), proof.Digest(), "public input JSON must not disclose the transport digest")
		})
	}
}

func TestCallerSendCommandProofCloneIsolation(t *testing.T) {
	t.Parallel()
	command := mustCallerCommand(t, callerCommandInputFixture())
	input := command.SendInput()
	input.CallerCommand = callerCommandProofFixture(t, command)
	copy := cloneSendMessageInput(input)
	require.NotSame(t, input.CallerCommand, copy.CallerCommand, "service input clone must not share mutable proof pointer storage")
	require.Equal(t, input.CallerCommand, copy.CallerCommand, "cloned proof must initially retain identical private scalar fields")
	input.CallerCommand.version = 99
	input.CallerCommand.ownerID = uuid.New()
	require.NoError(t, ValidateCallerSendCommandProof(copy), "mutating original proof storage must not change the service's cloned command")
	requireCallerProofMismatch(t, input)
	copy.CallerCommand.digest = "tampered"
	requireCallerProofMismatch(t, copy)
	fresh := callerCommandProofFixture(t, command)
	require.NotSame(t, fresh, input.CallerCommand, "each retained-command proof getter must allocate independent pointer storage")
	require.NotSame(t, fresh, copy.CallerCommand, "mutated clones must not alias the retained command's next proof")
	require.Equal(t, 1, fresh.Version(), "mutating a returned proof must not alter the retained command")
	require.Equal(t, command.OwnerID(), fresh.OwnerID(), "retained owner identity must survive external proof-copy mutation")
	require.Equal(t, command.Digest(), fresh.Digest(), "retained semantic digest must survive proof-copy mutation")
}

func TestCallerSendCommandProofMetadataMismatches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*CallerSendCommandProof)
	}{
		{name: "zero version", mutate: func(p *CallerSendCommandProof) { p.version = 0 }},
		{name: "future version", mutate: func(p *CallerSendCommandProof) { p.version = 2 }},
		{name: "empty purpose", mutate: func(p *CallerSendCommandProof) { p.purpose = "" }},
		{name: "unknown purpose", mutate: func(p *CallerSendCommandProof) { p.purpose = "arbitrary" }},
		{name: "different recognized purpose", mutate: func(p *CallerSendCommandProof) { p.purpose = CallerSendReviewLoopInitialSend }},
		{name: "zero owner", mutate: func(p *CallerSendCommandProof) { p.ownerID = uuid.Nil }},
		{name: "different owner", mutate: func(p *CallerSendCommandProof) { p.ownerID = uuid.New() }},
		{name: "empty digest", mutate: func(p *CallerSendCommandProof) { p.digest = "" }},
		{name: "uppercase digest", mutate: func(p *CallerSendCommandProof) { p.digest = strings.ToUpper(p.digest) }},
		{name: "malformed digest", mutate: func(p *CallerSendCommandProof) { p.digest = strings.Repeat("g", 64) }},
		{name: "different well formed digest", mutate: func(p *CallerSendCommandProof) { p.digest = strings.Repeat("0", 64) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			command := mustCallerCommand(t, callerCommandInputFixture())
			input := command.SendInput()
			input.CallerCommand = callerCommandProofFixture(t, command)
			tt.mutate(input.CallerCommand)
			requireCallerProofMismatch(t, input)
		})
	}
}

func TestCallerSendCommandProofRejectsEverySemanticMismatch(t *testing.T) {
	t.Parallel()
	for _, tt := range callerCommandSemanticMutationFixtures() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			command := mustCallerCommand(t, callerCommandInputFixture())
			var payload callerSendEnvelope
			require.NoError(t, json.Unmarshal(callerCommandBytes(t, command), &payload), "prepare an independent changed effective input")
			tt.mutate(&payload)
			input := payload.sendInput()
			input.CallerCommand = callerCommandProofFixture(t, command)
			requireCallerProofMismatch(t, input)
		})
	}
}

func TestCallerSendCommandProofRejectsMissingKeyAndRestrictedInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*SendMessageInput)
	}{
		{name: "missing original key", mutate: func(in *SendMessageInput) { in.ClientMessageID = "" }},
		{name: "review comment resolution", mutate: func(in *SendMessageInput) { in.ResolveReviewCommentIDs = []uuid.UUID{uuid.New()} }},
		{name: "legacy adapter target policy", mutate: func(in *SendMessageInput) { in.LegacyChangesetPolicy = LegacyChangesetRequireMaterialized }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			command := mustCallerCommand(t, callerCommandInputFixture())
			input := command.SendInput()
			input.CallerCommand = callerCommandProofFixture(t, command)
			tt.mutate(&input)
			requireCallerProofMismatch(t, input)
		})
	}
}

func TestCallerSendCommandProofCannotBeLaunderedBySemanticConstruction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*CallerSendCommandProof)
	}{
		{name: "wrong version", mutate: func(p *CallerSendCommandProof) { p.version = 2 }},
		{name: "wrong digest", mutate: func(p *CallerSendCommandProof) { p.digest = "invalid" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			command := mustCallerCommand(t, callerCommandInputFixture())
			input := command.SendInput()
			input.CallerCommand = callerCommandProofFixture(t, command)
			tt.mutate(input.CallerCommand)
			refrozen, err := NewCallerSendCommand(command.Purpose(), command.OwnerID(), input)
			require.NoError(t, err, "transport is not part of the command's semantic codec")
			require.Equal(t, callerCommandBytes(t, command), callerCommandBytes(t, refrozen), "even invalid transport must not change semantic encoding")
			require.Equal(t, command.Digest(), refrozen.Digest(), "transport validation cannot be replaced by newly computed semantic identity")
			requireCallerProofMismatch(t, input)
		})
	}
}

func TestCallerSendCommandProofZeroAndOpacity(t *testing.T) {
	t.Parallel()
	var zero CallerSendCommand
	proof, err := zero.Proof()
	require.ErrorIs(t, err, ErrCallerCommandMismatch, "uninitialized retained command must not issue transport proof")
	require.Nil(t, proof, "zero command must not manufacture an owner, purpose, version, or digest")
	command := mustCallerCommand(t, callerCommandInputFixture())
	input := command.SendInput()
	input.CallerCommand = &CallerSendCommandProof{}
	requireCallerProofMismatch(t, input)
	forged := &CallerSendCommandProof{}
	require.NoError(t, json.Unmarshal([]byte(`{"version":1,"purpose":"code-review-reviewer-dispatch","ownerID":"00000000-0000-4000-8000-000000000004","digest":"forged"}`), forged), "ordinary JSON cannot initialize private proof fields") //nolint:staticcheck // Intentionally verify standard JSON cannot populate sealed proof fields.
	require.Equal(t, &CallerSendCommandProof{}, forged, "proof may not be forged through exported fields or standard JSON input")
	input.CallerCommand = forged
	requireCallerProofMismatch(t, input)
	valid := callerCommandProofFixture(t, command)
	encoded, err := json.Marshal(valid) //nolint:staticcheck // Intentionally verify sealed transport proofs disclose no private fields.
	require.NoError(t, err, "opaque proof should have no public JSON fields")
	require.Equal(t, `{}`, string(encoded), "default JSON must not disclose proof scope, owner, or digest")
	proofType := reflect.TypeFor[CallerSendCommandProof]()
	expected := map[string]reflect.Type{"version": reflect.TypeFor[int](), "purpose": reflect.TypeFor[CallerSendPurpose](), "ownerID": reflect.TypeFor[uuid.UUID](), "digest": reflect.TypeFor[string]()}
	actual := map[string]reflect.Type{}
	for i := 0; i < proofType.NumField(); i++ {
		field := proofType.Field(i)
		require.NotEmpty(t, field.PkgPath, "every proof field must remain unexported and immutable outside this package")
		actual[field.Name] = field.Type
	}
	require.Equal(t, expected, actual, "proof transport should remain four private scalar fields with no mutable references")
}
