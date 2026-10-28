package db

import (
	"crypto/sha256"
	"math"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMessageAdmissionFiniteOptions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		action MessagePostSuccessAction
		author MessagePostSuccessAuthorMode
		valid  bool
	}{
		{name: "default policy", valid: true},
		{name: "push as user", action: MessagePostSuccessPushPRChanges, author: MessagePostSuccessAuthorUser, valid: true},
		{name: "push as app", action: MessagePostSuccessPushPRChanges, author: MessagePostSuccessAuthorApp, valid: true},
		{name: "push with default author", action: MessagePostSuccessPushPRChanges, valid: true},
		{name: "unknown action", action: "publish_anything", author: MessagePostSuccessAuthorUser},
		{name: "unknown author", action: MessagePostSuccessPushPRChanges, author: "administrator"},
		{name: "author without action", author: MessagePostSuccessAuthorUser},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := newMessageAdmissionCommandFixture(t)
			input.PostSuccessAction, input.PostSuccessAuthorMode = tt.action, tt.author
			_, err := PrepareMessageAdmissionIngress(input, "client-key")
			if tt.valid {
				require.NoError(t, err, "recognized finite follow-up options should preserve their existing meaning")
			} else {
				require.Error(t, err, "unsupported or orphaned follow-up options must fail closed")
			}
		})
	}
}

func TestMessageAdmissionInvalidShape(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*MessageAdmissionInput)
	}{
		{name: "zero tenant", change: func(i *MessageAdmissionInput) { i.OrgID = uuid.Nil }},
		{name: "zero fence", change: func(i *MessageAdmissionInput) { i.Fence = AdmissionFence{} }},
		{name: "session wide fence", change: func(i *MessageAdmissionInput) { i.Fence = AdmissionFence{sessionID: i.Fence.sessionID} }},
		{name: "zero lane", change: func(i *MessageAdmissionInput) { i.Fence.threadID = uuid.Nil }},
		{name: "negative session epoch", change: func(i *MessageAdmissionInput) { i.Fence.sessionEpoch = -1 }},
		{name: "negative lane epoch", change: func(i *MessageAdmissionInput) { i.Fence.threadEpoch = -1 }},
		{name: "zero actor supplied", change: func(i *MessageAdmissionInput) { *i.ActorID = uuid.Nil }},
		{name: "unknown source", change: func(i *MessageAdmissionInput) { i.Source = "unreviewed_source" }},
		{name: "missing changeset", change: func(i *MessageAdmissionInput) { i.ChangesetID = nil }},
		{name: "missing holder", change: func(i *MessageAdmissionInput) { i.LeaseHolderID = nil }},
		{name: "zero changeset", change: func(i *MessageAdmissionInput) { *i.ChangesetID = uuid.Nil }},
		{name: "zero holder", change: func(i *MessageAdmissionInput) { *i.LeaseHolderID = uuid.Nil }},
		{name: "missing run time", change: func(i *MessageAdmissionInput) { i.Dispatch.RunAt = time.Time{} }},
		{name: "zero max attempts", change: func(i *MessageAdmissionInput) { i.Dispatch.MaxAttempts = 0 }},
		{name: "negative max attempts", change: func(i *MessageAdmissionInput) { i.Dispatch.MaxAttempts = -1 }},
		{name: "attempts exceed PostgreSQL integer", change: func(i *MessageAdmissionInput) { i.Dispatch.MaxAttempts = math.MaxInt32 + 1 }},
		{name: "empty dedupe override", change: func(i *MessageAdmissionInput) { *i.ContinuationDedupeKeyOverride = "" }},
		{name: "blank dedupe override", change: func(i *MessageAdmissionInput) { *i.ContinuationDedupeKeyOverride = " \t" }},
		{name: "zero comment", change: func(i *MessageAdmissionInput) { i.ResolveReviewCommentIDs[0] = uuid.Nil }},
		{name: "duplicate comment", change: func(i *MessageAdmissionInput) { i.ResolveReviewCommentIDs[1] = i.ResolveReviewCommentIDs[0] }},
		{name: "empty image", change: func(i *MessageAdmissionInput) { i.Images[0] = "" }},
		{name: "invalid reference", change: func(i *MessageAdmissionInput) { i.References[0].Path = "" }},
		{name: "invalid command", change: func(i *MessageAdmissionInput) { i.Commands[0].Token = "not-a-command" }},
		{name: "invalid message encoding", change: func(i *MessageAdmissionInput) { i.Message = string([]byte{0xff}) }},
		{name: "message NUL", change: func(i *MessageAdmissionInput) { i.Message = "a\x00b" }},
		{name: "invalid image encoding", change: func(i *MessageAdmissionInput) { i.Images[0] = string([]byte{0xff}) }},
		{name: "invalid reference encoding", change: func(i *MessageAdmissionInput) { i.References[0].Display = string([]byte{0xff}) }},
		{name: "invalid command encoding", change: func(i *MessageAdmissionInput) { i.Commands[0].Arguments = string([]byte{0xff}) }},
		{name: "empty payload", change: func(i *MessageAdmissionInput) {
			i.Message, i.Images, i.References, i.Commands = " \n", nil, nil, nil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := newMessageAdmissionCommandFixture(t)
			tt.change(&input)
			_, err := PrepareMessageAdmissionIngress(input, "client-key")
			require.Error(t, err, "ingress preparation should reject the malformed command before minting identity")
			actual, err := NewMessageAdmission(input)
			require.Error(t, err, "the constructor must independently reject malformed command shape")
			require.Equal(t, MessageAdmission{}, actual, "invalid construction must return no retained command")
		})
	}
}

func TestMessageAdmissionInvalidIngress(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*MessageAdmissionInput)
	}{
		{name: "missing ingress", change: func(i *MessageAdmissionInput) { i.Ingress = LifecycleIngress{} }},
		{name: "zero digest", change: func(i *MessageAdmissionInput) { i.Ingress.digest = LifecycleDigest{} }},
		{name: "forged digest", change: func(i *MessageAdmissionInput) { i.Ingress.digest[0] ^= 1 }},
		{name: "changed content with old digest", change: func(i *MessageAdmissionInput) { i.Message += " changed" }},
		{name: "unknown kind", change: func(i *MessageAdmissionInput) { i.Ingress.kind = "integration_event" }},
		{name: "wrong tenant scope", change: func(i *MessageAdmissionInput) { i.Ingress.key = uuid.UUID{15: 99}.String() + i.Ingress.key[36:] }},
		{name: "missing scoped suffix", change: func(i *MessageAdmissionInput) { i.Ingress.key = messageAdmissionScopePrefix(*i) }},
		{name: "invalid client key encoding", change: func(i *MessageAdmissionInput) { i.Ingress.key = messageAdmissionScopePrefix(*i) + string([]byte{0xff}) }},
		{name: "server key not UUID", change: func(i *MessageAdmissionInput) { i.Ingress.kind = messageAdmissionServerIngress }},
		{name: "server zero UUID", change: func(i *MessageAdmissionInput) {
			i.Ingress.kind, i.Ingress.key = messageAdmissionServerIngress, messageAdmissionScopePrefix(*i)+uuid.Nil.String()
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := newMessageAdmissionCommandFixture(t)
			tt.change(&input)
			actual, err := NewMessageAdmission(input)
			require.Error(t, err, "a supplied ingress must retain exact scoped identity and semantic digest")
			require.Equal(t, MessageAdmission{}, actual, "invalid ingress must not yield a usable command")
		})
	}
}

func TestMessageAdmissionAllowedOptionalPayloads(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*MessageAdmissionInput)
	}{
		{name: "human source and omitted actor", change: func(i *MessageAdmissionInput) { i.Source, i.ActorID = "", nil }},
		{name: "system source remains only shape", change: func(i *MessageAdmissionInput) { i.Source = models.SessionMessageSourceAgentTool }},
		{name: "no changeset or holder", change: func(i *MessageAdmissionInput) { i.ChangesetID, i.LeaseHolderID = nil, nil }},
		{name: "images only", change: func(i *MessageAdmissionInput) { i.Message, i.References, i.Commands = "", nil, nil }},
		{name: "references only", change: func(i *MessageAdmissionInput) { i.Message, i.Images, i.Commands = "", nil, nil }},
		{name: "commands only", change: func(i *MessageAdmissionInput) { i.Message, i.Images, i.References = "", nil, nil }},
		{name: "no dedupe override", change: func(i *MessageAdmissionInput) { i.ContinuationDedupeKeyOverride = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := newMessageAdmissionCommandFixture(t)
			tt.change(&input)
			input.Ingress = prepareMessageAdmissionCommandFixture(t, input, "client-key")
			actual, err := NewMessageAdmission(input)
			require.NoError(t, err, "recognized optional shapes must remain available for durable authorization")
			require.NoError(t, actual.Validate(), "a retained valid optional command must preserve its complete shape")
			require.Equal(t, input, actual.Input(), "construction must preserve exact values without trimming or policy inference")
		})
	}
}

func TestMessageAdmissionDefensiveCopies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		mutateInput func(*MessageAdmissionInput)
		mutateValue func(MessageAdmission)
	}{
		{name: "actor pointer", mutateInput: func(i *MessageAdmissionInput) { *i.ActorID = uuid.Nil }, mutateValue: func(v MessageAdmission) { *v.ActorID() = uuid.Nil }},
		{name: "changeset pointer", mutateInput: func(i *MessageAdmissionInput) { *i.ChangesetID = uuid.Nil }, mutateValue: func(v MessageAdmission) { *v.ChangesetID() = uuid.Nil }},
		{name: "holder pointer", mutateInput: func(i *MessageAdmissionInput) { *i.LeaseHolderID = uuid.Nil }, mutateValue: func(v MessageAdmission) { *v.LeaseHolderID() = uuid.Nil }},
		{name: "dedupe pointer", mutateInput: func(i *MessageAdmissionInput) { *i.ContinuationDedupeKeyOverride = "mutated" }, mutateValue: func(v MessageAdmission) { *v.ContinuationDedupeKeyOverride() = "mutated" }},
		{name: "images", mutateInput: func(i *MessageAdmissionInput) { i.Images[0] = "mutated" }, mutateValue: func(v MessageAdmission) { v.Images()[0] = "mutated" }},
		{name: "references", mutateInput: func(i *MessageAdmissionInput) { i.References[0].Display = "mutated" }, mutateValue: func(v MessageAdmission) { v.References()[0].Display = "mutated" }},
		{name: "commands", mutateInput: func(i *MessageAdmissionInput) { i.Commands[0].Arguments = "mutated" }, mutateValue: func(v MessageAdmission) { v.Commands()[0].Arguments = "mutated" }},
		{name: "comments", mutateInput: func(i *MessageAdmissionInput) { i.ResolveReviewCommentIDs[0] = uuid.Nil }, mutateValue: func(v MessageAdmission) { v.ResolveReviewCommentIDs()[0] = uuid.Nil }},
		{name: "scalar and value options", mutateInput: func(i *MessageAdmissionInput) {
			i.OrgID, i.Message, i.Source, i.PlanMode = uuid.Nil, "mutated", models.SessionMessageSourceAgentTool, false
			i.Fence, i.Ingress, i.Dispatch = AdmissionFence{}, LifecycleIngress{}, MessageAdmissionDispatchOptions{}
			i.PostSuccessAction, i.PostSuccessAuthorMode = MessagePostSuccessNone, MessagePostSuccessAuthorDefault
		}, mutateValue: func(v MessageAdmission) { input := v.Input(); input.Message = "mutated" }},
		{name: "full input getter", mutateInput: func(i *MessageAdmissionInput) { i.Images = nil }, mutateValue: func(v MessageAdmission) {
			i := v.Input()
			*i.ActorID, *i.ChangesetID, *i.LeaseHolderID = uuid.Nil, uuid.Nil, uuid.Nil
			*i.ContinuationDedupeKeyOverride = "mutated"
			i.Images[0], i.References[0].Display, i.Commands[0].Name, i.ResolveReviewCommentIDs[0] = "mutated", "mutated", "mutated", uuid.Nil
		}},
		{name: "semantic payload bytes", mutateInput: func(i *MessageAdmissionInput) { i.Message = "mutated" }, mutateValue: func(v MessageAdmission) { v.SemanticPayload()[0] = '?' }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := newMessageAdmissionCommandFixture(t)
			actual, err := NewMessageAdmission(input)
			require.NoError(t, err, "the fixture must construct a validated immutable command")
			wantInput, wantPayload := actual.Input(), actual.SemanticPayload()
			tt.mutateInput(&input)
			tt.mutateValue(actual)
			require.Equal(t, wantInput, actual.Input(), "caller input and returned aliases must never change retained command values")
			require.Equal(t, wantPayload, actual.SemanticPayload(), "mutations must never alter retained semantic payload bytes")
			require.NoError(t, actual.Validate(), "retained command identity must remain valid after all caller mutations")
		})
	}
}

func TestMessageAdmissionSemanticDigestExcludedServerValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*MessageAdmissionInput)
	}{
		{name: "session epoch", change: func(i *MessageAdmissionInput) { i.Fence.sessionEpoch++ }},
		{name: "lane epoch", change: func(i *MessageAdmissionInput) { i.Fence.threadEpoch++ }},
		{name: "server holder", change: func(i *MessageAdmissionInput) { *i.LeaseHolderID = uuid.UUID{15: 90} }},
		{name: "server run time", change: func(i *MessageAdmissionInput) { i.Dispatch.RunAt = i.Dispatch.RunAt.Add(time.Hour) }},
		{name: "server retry budget", change: func(i *MessageAdmissionInput) { i.Dispatch.MaxAttempts++ }},
		{name: "server run time and retry budget", change: func(i *MessageAdmissionInput) {
			i.Dispatch.RunAt = i.Dispatch.RunAt.Add(time.Hour)
			i.Dispatch.MaxAttempts++
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := newMessageAdmissionCommandFixture(t)
			original, err := NewMessageAdmission(input)
			require.NoError(t, err, "original caller intent must construct before server-selected values change")
			want := input.Ingress
			tt.change(&input)
			actual := prepareMessageAdmissionCommandFixture(t, input, "client-key")
			require.Equal(t, want, actual, "fresh server epochs, holder, schedule or retry budget must not conflict with the same client intent")
			input.Ingress = want
			retained, err := NewMessageAdmission(input)
			require.NoError(t, err, "the original semantic ingress must remain valid under changed excluded server metadata")
			require.Equal(t, original.SemanticPayload(), retained.SemanticPayload(), "server-selected changes must preserve the exact canonical caller-intent bytes")
			require.Equal(t, input.Dispatch, retained.Dispatch(), "exclusion from client intent must not discard concrete scheduling or retry options")
		})
	}
}

func TestMessageAdmissionSemanticDigestIncludedValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*MessageAdmissionInput)
	}{
		{name: "tenant", change: func(i *MessageAdmissionInput) { i.OrgID = uuid.UUID{15: 90} }},
		{name: "session scope", change: func(i *MessageAdmissionInput) { i.Fence.sessionID = uuid.UUID{15: 90} }},
		{name: "lane scope", change: func(i *MessageAdmissionInput) { i.Fence.threadID = uuid.UUID{15: 90} }},
		{name: "actor", change: func(i *MessageAdmissionInput) { *i.ActorID = uuid.UUID{15: 90} }},
		{name: "omitted actor", change: func(i *MessageAdmissionInput) { i.ActorID = nil }},
		{name: "source", change: func(i *MessageAdmissionInput) { i.Source = models.SessionMessageSourceAgentTool }},
		{name: "message bytes", change: func(i *MessageAdmissionInput) { i.Message += " " }},
		{name: "image identity", change: func(i *MessageAdmissionInput) { i.Images[0] = "other-image" }},
		{name: "image ordering", change: func(i *MessageAdmissionInput) { i.Images[0], i.Images[1] = i.Images[1], i.Images[0] }},
		{name: "reference kind", change: func(i *MessageAdmissionInput) { i.References[0].Kind = models.SessionInputReferenceKindDirectory }},
		{name: "reference token", change: func(i *MessageAdmissionInput) { i.References[0].Token += "2" }},
		{name: "reference path", change: func(i *MessageAdmissionInput) { i.References[0].Path += "2" }},
		{name: "reference id", change: func(i *MessageAdmissionInput) { i.References[0].ID = "reference-identity" }},
		{name: "reference display", change: func(i *MessageAdmissionInput) { i.References[0].Display += "2" }},
		{name: "command kind", change: func(i *MessageAdmissionInput) { i.Commands[0].Kind = "" }},
		{name: "command agent", change: func(i *MessageAdmissionInput) { i.Commands[0].AgentType = models.AgentTypeCodex }},
		{name: "command name", change: func(i *MessageAdmissionInput) { i.Commands[0].Name += "2" }},
		{name: "command token", change: func(i *MessageAdmissionInput) { i.Commands[0].Token += "2" }},
		{name: "command display", change: func(i *MessageAdmissionInput) { i.Commands[0].Display += "2" }},
		{name: "command description", change: func(i *MessageAdmissionInput) { i.Commands[0].Description += "2" }},
		{name: "command arguments", change: func(i *MessageAdmissionInput) { i.Commands[0].Arguments += "2" }},
		{name: "command catalog", change: func(i *MessageAdmissionInput) { i.Commands[0].Source = models.SessionInputCommandSourceProject }},
		{name: "changeset identity", change: func(i *MessageAdmissionInput) { *i.ChangesetID = uuid.UUID{15: 90} }},
		{name: "review comment identity", change: func(i *MessageAdmissionInput) { i.ResolveReviewCommentIDs[0] = uuid.UUID{15: 90} }},
		{name: "review comment ordering", change: func(i *MessageAdmissionInput) {
			i.ResolveReviewCommentIDs[0], i.ResolveReviewCommentIDs[1] = i.ResolveReviewCommentIDs[1], i.ResolveReviewCommentIDs[0]
		}},
		{name: "plan mode", change: func(i *MessageAdmissionInput) { i.PlanMode = false }},
		{name: "post success action", change: func(i *MessageAdmissionInput) {
			i.PostSuccessAction, i.PostSuccessAuthorMode = MessagePostSuccessNone, MessagePostSuccessAuthorDefault
		}},
		{name: "post success author", change: func(i *MessageAdmissionInput) { i.PostSuccessAuthorMode = MessagePostSuccessAuthorApp }},
		{name: "dedupe override", change: func(i *MessageAdmissionInput) { *i.ContinuationDedupeKeyOverride = "other-key" }},
		{name: "omitted dedupe override", change: func(i *MessageAdmissionInput) { i.ContinuationDedupeKeyOverride = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := newMessageAdmissionCommandFixture(t)
			previous := input.Ingress.Digest()
			tt.change(&input)
			_, err := NewMessageAdmission(input)
			require.Error(t, err, "genuine caller-intent changes cannot reuse the original semantic ingress")
			input.Ingress = prepareMessageAdmissionCommandFixture(t, input, "client-key")
			require.NotEqual(t, previous, input.Ingress.Digest(), "every included semantic field must participate in conflicting-intent detection")
			_, err = NewMessageAdmission(input)
			require.NoError(t, err, "the newly hashed changed command must remain structurally valid")
		})
	}
}

func TestMessageAdmissionSemanticNilAndEmptyCollections(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		empty func(*MessageAdmissionInput)
	}{
		{name: "images", empty: func(i *MessageAdmissionInput) { i.Images = []string{} }},
		{name: "references", empty: func(i *MessageAdmissionInput) { i.References = models.SessionInputReferences{} }},
		{name: "commands", empty: func(i *MessageAdmissionInput) { i.Commands = models.SessionInputCommands{} }},
		{name: "comments", empty: func(i *MessageAdmissionInput) { i.ResolveReviewCommentIDs = []uuid.UUID{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := newMessageAdmissionCommandFixture(t)
			input.Images, input.References, input.Commands, input.ResolveReviewCommentIDs = nil, nil, nil, nil
			before := prepareMessageAdmissionCommandFixture(t, input, "client-key")
			tt.empty(&input)
			after := prepareMessageAdmissionCommandFixture(t, input, "client-key")
			require.Equal(t, before, after, "nil versus empty collection must not manufacture a semantic replay conflict")
		})
	}
}

func TestMessageAdmissionIngressIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*MessageAdmissionInput)
	}{
		{name: "other tenant", change: func(i *MessageAdmissionInput) { i.OrgID = uuid.UUID{15: 90} }},
		{name: "other session", change: func(i *MessageAdmissionInput) { i.Fence.sessionID = uuid.UUID{15: 90} }},
		{name: "other lane", change: func(i *MessageAdmissionInput) { i.Fence.threadID = uuid.UUID{15: 90} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := newMessageAdmissionCommandFixture(t)
			original := input.Ingress
			tt.change(&input)
			other := prepareMessageAdmissionCommandFixture(t, input, "client-key")
			require.NotEqual(t, original.Key(), other.Key(), "the same client message key must remain isolated by exact tenant/session/lane")
		})
	}
}

func TestMessageAdmissionIngressReplayAndKeylessPreparation(t *testing.T) {
	t.Parallel()
	input := newMessageAdmissionCommandFixture(t)
	original := input.Ingress
	input.Message += " different"
	conflict := prepareMessageAdmissionCommandFixture(t, input, "client-key")
	require.Equal(t, original.Key(), conflict.Key(), "conflicting content must reuse the same key so durable admission can reject it")
	require.NotEqual(t, original.Digest(), conflict.Digest(), "same-key different semantics must produce distinguishable payload digests")
	input.Ingress = prepareMessageAdmissionCommandFixture(t, input, "")
	other := prepareMessageAdmissionCommandFixture(t, input, "")
	require.Equal(t, messageAdmissionServerIngress, input.Ingress.Kind(), "keyless sends must use the finite server command namespace")
	require.NotEqual(t, input.Ingress.Key(), other.Key(), "separately prepared keyless sends are different commands despite equal content")
	require.Equal(t, input.Ingress.Digest(), other.Digest(), "identity generation must not enter the semantic content digest")
	actual, err := NewMessageAdmission(input)
	require.NoError(t, err, "one pre-attempt keyless command must retain its prepared identity")
	for range 3 {
		require.NoError(t, actual.Validate(), "revalidation during retries must not mint a replacement key")
		require.Equal(t, input.Ingress, actual.Ingress(), "every attempt must observe the same retained server command identity")
	}
}

func TestMessageAdmissionSemanticPayloadEncoding(t *testing.T) {
	t.Parallel()
	input := newMessageAdmissionCommandFixture(t)
	actual, err := NewMessageAdmission(input)
	require.NoError(t, err, "the complete message command must construct before checking its canonical semantic representation")
	want := `{"version":1,"org_id":"00000000-0000-0000-0000-000000000001","session_id":"00000000-0000-0000-0000-000000000002","thread_id":"00000000-0000-0000-0000-000000000003","actor_id":"00000000-0000-0000-0000-000000000004","source":"","message":"  exact message\n","images":["image-one","image-two"],"references":[{"kind":"file","token":"@main.go","path":"main.go","display":"main.go"}],"commands":[{"kind":"command","agent_type":"claude_code","name":"review","token":"/review","display":"/review","description":"Review the change","arguments":"--strict","source":"builtin"}],"changeset_id":"00000000-0000-0000-0000-000000000005","resolve_review_comment_ids":["00000000-0000-0000-0000-000000000007","00000000-0000-0000-0000-000000000008"],"plan_mode":true,"post_success_action":"push_pr_changes","post_success_author_mode":"user","continuation_dedupe_key_override":"followup-key"}`
	require.Equal(t, want, string(actual.SemanticPayload()), "canonical JSON must retain every semantic field and exclude all server fences, IDs, times and retry budgets")
	require.Equal(t, LifecycleDigest(sha256.Sum256([]byte(want))), actual.Ingress().Digest(), "the persisted input digest must hash the exact canonical semantic bytes")
	require.Equal(t, input.OrgID, actual.OrgID(), "the immutable command must expose its exact organization")
	require.Equal(t, input.Fence, actual.Fence(), "the immutable command must retain excluded epochs for durable validation")
	require.Equal(t, input.Source, actual.Source(), "empty human source must be preserved without invented attribution")
	require.Equal(t, input.Message, actual.Message(), "message whitespace and newline bytes must not be normalized")
	require.Equal(t, input.PlanMode, actual.PlanMode(), "plan-mode intent must remain an explicit option")
	require.Equal(t, input.PostSuccessAction, actual.PostSuccessAction(), "the finite follow-up action must survive construction")
	require.Equal(t, input.PostSuccessAuthorMode, actual.PostSuccessAuthorMode(), "the finite author mode must survive construction")
	require.Equal(t, input.Dispatch, actual.Dispatch(), "concrete retry and scheduling options must remain frozen")
	var zero MessageAdmission
	require.Error(t, zero.Validate(), "zero command must never be usable as admission authority or valid input")
}

func newMessageAdmissionCommandFixture(t *testing.T) MessageAdmissionInput {
	t.Helper()
	orgID, sessionID, laneID := uuid.UUID{15: 1}, uuid.UUID{15: 2}, uuid.UUID{15: 3}
	actorID, changesetID, holderID := uuid.UUID{15: 4}, uuid.UUID{15: 5}, uuid.UUID{15: 6}
	laneEpoch := int64(8)
	fence, err := NewAdmissionFence(sessionID, &laneID, 7, &laneEpoch)
	require.NoError(t, err, "fixture must have an explicit valid lane and both expected epochs")
	dedupe := "followup-key"
	input := MessageAdmissionInput{
		OrgID: orgID, Fence: fence, ActorID: &actorID, Message: "  exact message\n",
		Images:      []string{"image-one", "image-two"},
		References:  models.SessionInputReferences{{Kind: models.SessionInputReferenceKindFile, Token: "@main.go", Path: "main.go", Display: "main.go"}},
		Commands:    models.SessionInputCommands{{Kind: "command", AgentType: models.AgentTypeClaudeCode, Name: "review", Token: "/review", Display: "/review", Description: "Review the change", Arguments: "--strict", Source: models.SessionInputCommandSourceBuiltin}},
		ChangesetID: &changesetID, LeaseHolderID: &holderID,
		ResolveReviewCommentIDs: []uuid.UUID{{15: 7}, {15: 8}}, PlanMode: true,
		PostSuccessAction: MessagePostSuccessPushPRChanges, PostSuccessAuthorMode: MessagePostSuccessAuthorUser,
		ContinuationDedupeKeyOverride: &dedupe,
		Dispatch:                      MessageAdmissionDispatchOptions{RunAt: time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC), MaxAttempts: 3},
	}
	input.Ingress = prepareMessageAdmissionCommandFixture(t, input, "client-key")
	return input
}

func prepareMessageAdmissionCommandFixture(t *testing.T, input MessageAdmissionInput, clientKey string) LifecycleIngress {
	t.Helper()
	ingress, err := PrepareMessageAdmissionIngress(input, clientKey)
	require.NoError(t, err, "fixture ingress preparation must preserve valid immutable command semantics")
	return ingress
}
