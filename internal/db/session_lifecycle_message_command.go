package db

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

const (
	messageAdmissionClientIngress = "thread_client_message"
	messageAdmissionServerIngress = "thread_server_command"
)

// MessagePostSuccessAction describes the existing continuation follow-up, not
// permission to publish. The worker still validates its own durable authority.
type MessagePostSuccessAction string

const (
	MessagePostSuccessNone          MessagePostSuccessAction = ""
	MessagePostSuccessPushPRChanges MessagePostSuccessAction = "push_pr_changes"
)

func (a MessagePostSuccessAction) Validate() error {
	switch a {
	case MessagePostSuccessNone, MessagePostSuccessPushPRChanges:
		return nil
	default:
		return fmt.Errorf("invalid message post-success action: %q", a)
	}
}

// MessagePostSuccessAuthorMode preserves the existing GitHub identity choices.
// Empty delegates to existing policy; user/app are explicit choices, not tokens.
type MessagePostSuccessAuthorMode string

const (
	MessagePostSuccessAuthorDefault MessagePostSuccessAuthorMode = ""
	MessagePostSuccessAuthorUser    MessagePostSuccessAuthorMode = "user"
	MessagePostSuccessAuthorApp     MessagePostSuccessAuthorMode = "app"
)

func (m MessagePostSuccessAuthorMode) Validate() error {
	switch m {
	case MessagePostSuccessAuthorDefault, MessagePostSuccessAuthorUser, MessagePostSuccessAuthorApp:
		return nil
	default:
		return fmt.Errorf("invalid message post-success author mode: %q", m)
	}
}

// MessageAdmissionDispatchOptions freezes concrete queue options before the
// transaction owner attempts admission. Zero values are not replay wildcards.
type MessageAdmissionDispatchOptions struct {
	RunAt       time.Time
	MaxAttempts int
}

func (o MessageAdmissionDispatchOptions) Validate() error {
	if o.RunAt.IsZero() || o.MaxAttempts <= 0 || o.MaxAttempts > math.MaxInt32 {
		return errors.New("message admission requires explicit run time and positive integer max attempts")
	}
	return nil
}

// MessageAdmissionInput is copied by NewMessageAdmission. Ingress must already
// be fixed before entering any transaction/retry loop. Shape validation cannot
// establish actor membership, ordinary-source authorization or durable authority.
type MessageAdmissionInput struct {
	OrgID                         uuid.UUID
	Fence                         AdmissionFence
	Ingress                       LifecycleIngress
	ActorID                       *uuid.UUID
	Source                        models.SessionMessageSource
	Message                       string
	Images                        []string
	References                    models.SessionInputReferences
	Commands                      models.SessionInputCommands
	ChangesetID                   *uuid.UUID
	LeaseHolderID                 *uuid.UUID
	ResolveReviewCommentIDs       []uuid.UUID
	PlanMode                      bool
	PostSuccessAction             MessagePostSuccessAction
	PostSuccessAuthorMode         MessagePostSuccessAuthorMode
	ContinuationDedupeKeyOverride *string
	Dispatch                      MessageAdmissionDispatchOptions
}

// MessageAdmission is an immutable ordinary-message command, never an admission
// permit. Expected epochs and the server's changeset holder remain explicit for
// durable validation, but do not change the caller-owned semantic payload digest.
type MessageAdmission struct {
	input           MessageAdmissionInput
	semanticPayload json.RawMessage
}

func NewMessageAdmission(input MessageAdmissionInput) (MessageAdmission, error) {
	payload, err := validateMessageAdmission(input)
	if err != nil {
		return MessageAdmission{}, err
	}
	return MessageAdmission{input: cloneMessageAdmissionInput(input), semanticPayload: payload}, nil
}

func (v MessageAdmission) Validate() error {
	payload, err := validateMessageAdmission(v.input)
	if err != nil {
		return err
	}
	if !bytes.Equal(payload, v.semanticPayload) {
		return errors.New("message admission retained payload does not match its command")
	}
	return nil
}

// PrepareMessageAdmissionIngress runs ONCE before transaction attempts. A
// caller-supplied key is scoped to the exact tenant/session/lane. With no key,
// this preparation mints one command UUID; constructors and getters never mint.
// Repeated identical content without a client key is intentionally a new command.
func PrepareMessageAdmissionIngress(input MessageAdmissionInput, clientMessageID string) (LifecycleIngress, error) {
	payload, err := messageAdmissionSemanticPayload(input)
	if err != nil {
		return LifecycleIngress{}, err
	}
	kind, key := messageAdmissionClientIngress, clientMessageID
	if key == "" {
		id, err := uuid.NewRandom()
		if err != nil {
			return LifecycleIngress{}, fmt.Errorf("prepare message command identity: %w", err)
		}
		kind, key = messageAdmissionServerIngress, id.String()
	} else if !validMessageAdmissionText(key) || strings.TrimSpace(key) == "" {
		return LifecycleIngress{}, errors.New("message admission client key must be nonempty valid text")
	}
	digest := LifecycleDigest(sha256.Sum256(payload))
	return NewLifecycleIngress(kind, messageAdmissionScopePrefix(input)+key, digest)
}

func validateMessageAdmission(input MessageAdmissionInput) (json.RawMessage, error) {
	payload, err := messageAdmissionSemanticPayload(input)
	if err != nil {
		return nil, err
	}
	if err := input.Ingress.Validate(); err != nil {
		return nil, fmt.Errorf("message admission ingress: %w", err)
	}
	if input.Ingress.kind != messageAdmissionClientIngress && input.Ingress.kind != messageAdmissionServerIngress {
		return nil, errors.New("message admission requires a recognized thread ingress kind")
	}
	prefix := messageAdmissionScopePrefix(input)
	if !strings.HasPrefix(input.Ingress.key, prefix) {
		return nil, errors.New("message admission ingress must name its exact tenant/session/lane")
	}
	key := strings.TrimPrefix(input.Ingress.key, prefix)
	if !validMessageAdmissionText(key) || strings.TrimSpace(key) == "" {
		return nil, errors.New("message admission ingress requires an exact nonempty command key")
	}
	if input.Ingress.kind == messageAdmissionServerIngress {
		id, err := uuid.Parse(key)
		if err != nil || id == uuid.Nil || id.String() != key {
			return nil, errors.New("message admission server command key must be a canonical nonzero UUID")
		}
	}
	if input.Ingress.digest != LifecycleDigest(sha256.Sum256(payload)) {
		return nil, errors.New("message admission ingress digest does not match its semantic payload")
	}
	return payload, nil
}

func messageAdmissionSemanticPayload(input MessageAdmissionInput) (json.RawMessage, error) {
	if err := validateMessageAdmissionFields(input); err != nil {
		return nil, err
	}
	laneID, _ := input.Fence.ThreadID()
	// Fixed typed JSON gives one semantic encoding. Empty collections encode as
	// [] regardless of nil/empty input. Order and exact text are otherwise kept.
	// Epochs, turn/token/message/job IDs, lease holder and ALL server times are
	// absent; a freshly selected server value must not conflict with client replay.
	payload := struct {
		Version                       int                           `json:"version"`
		OrgID                         uuid.UUID                     `json:"org_id"`
		SessionID                     uuid.UUID                     `json:"session_id"`
		ThreadID                      uuid.UUID                     `json:"thread_id"`
		ActorID                       *uuid.UUID                    `json:"actor_id"`
		Source                        models.SessionMessageSource   `json:"source"`
		Message                       string                        `json:"message"`
		Images                        []string                      `json:"images"`
		References                    models.SessionInputReferences `json:"references"`
		Commands                      models.SessionInputCommands   `json:"commands"`
		ChangesetID                   *uuid.UUID                    `json:"changeset_id"`
		ResolveReviewCommentIDs       []uuid.UUID                   `json:"resolve_review_comment_ids"`
		PlanMode                      bool                          `json:"plan_mode"`
		PostSuccessAction             MessagePostSuccessAction      `json:"post_success_action"`
		PostSuccessAuthorMode         MessagePostSuccessAuthorMode  `json:"post_success_author_mode"`
		ContinuationDedupeKeyOverride *string                       `json:"continuation_dedupe_key_override"`
		MaxAttempts                   int                           `json:"max_attempts"`
	}{
		Version: 1, OrgID: input.OrgID, SessionID: input.Fence.SessionID(), ThreadID: laneID,
		ActorID: input.ActorID, Source: input.Source, Message: input.Message,
		Images:                  append([]string{}, input.Images...),
		References:              append(models.SessionInputReferences{}, input.References...),
		Commands:                append(models.SessionInputCommands{}, input.Commands...),
		ChangesetID:             input.ChangesetID,
		ResolveReviewCommentIDs: append([]uuid.UUID{}, input.ResolveReviewCommentIDs...),
		PlanMode:                input.PlanMode, PostSuccessAction: input.PostSuccessAction,
		PostSuccessAuthorMode:         input.PostSuccessAuthorMode,
		ContinuationDedupeKeyOverride: input.ContinuationDedupeKeyOverride, MaxAttempts: input.Dispatch.MaxAttempts,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode message admission semantic payload: %w", err)
	}
	return encoded, nil
}

func validateMessageAdmissionFields(input MessageAdmissionInput) error {
	if input.OrgID == uuid.Nil {
		return errors.New("message admission requires an exact nonzero tenant")
	}
	if err := input.Fence.Validate(); err != nil {
		return err
	}
	if _, threaded := input.Fence.ThreadID(); !threaded {
		return errors.New("message admission requires an explicit lane fence")
	}
	if input.ActorID != nil && *input.ActorID == uuid.Nil {
		return errors.New("message admission optional actor must be nonzero when supplied")
	}
	if err := input.Source.Validate(); err != nil {
		return err
	}
	if (input.ChangesetID == nil) != (input.LeaseHolderID == nil) {
		return errors.New("message admission changeset and lease holder must be supplied together")
	}
	if input.ChangesetID != nil && (*input.ChangesetID == uuid.Nil || *input.LeaseHolderID == uuid.Nil) {
		return errors.New("message admission changeset and lease holder must be nonzero")
	}
	if err := input.Dispatch.Validate(); err != nil {
		return err
	}
	if err := input.PostSuccessAction.Validate(); err != nil {
		return err
	}
	if err := input.PostSuccessAuthorMode.Validate(); err != nil {
		return err
	}
	if input.PostSuccessAction == MessagePostSuccessNone && input.PostSuccessAuthorMode != MessagePostSuccessAuthorDefault {
		return errors.New("message admission author mode requires a post-success action")
	}
	if input.ContinuationDedupeKeyOverride != nil &&
		(!validMessageAdmissionText(*input.ContinuationDedupeKeyOverride) || strings.TrimSpace(*input.ContinuationDedupeKeyOverride) == "") {
		return errors.New("message admission dedupe override must be nonempty valid text when supplied")
	}
	if !validMessageAdmissionText(input.Message) {
		return errors.New("message admission content must be valid text")
	}
	if strings.TrimSpace(input.Message) == "" && len(input.Images) == 0 && len(input.References) == 0 && len(input.Commands) == 0 {
		return errors.New("message admission requires message content, images, references or commands")
	}
	for _, image := range input.Images {
		if !validMessageAdmissionText(image) || strings.TrimSpace(image) == "" {
			return errors.New("message admission images require nonempty valid text")
		}
	}
	for _, ref := range input.References {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("message admission reference: %w", err)
		}
		if !validMessageAdmissionText(ref.Token, ref.Path, ref.ID, ref.Display) {
			return errors.New("message admission reference fields must be valid text")
		}
	}
	for _, command := range input.Commands {
		if err := command.Validate(); err != nil {
			return fmt.Errorf("message admission command: %w", err)
		}
		if !validMessageAdmissionText(command.Name, command.Token, command.Display, command.Description, command.Arguments) {
			return errors.New("message admission command fields must be valid text")
		}
	}
	seen := make(map[uuid.UUID]struct{}, len(input.ResolveReviewCommentIDs))
	for _, id := range input.ResolveReviewCommentIDs {
		if id == uuid.Nil {
			return errors.New("message admission review comment IDs must be nonzero")
		}
		if _, duplicate := seen[id]; duplicate {
			return errors.New("message admission review comment IDs must be unique")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validMessageAdmissionText(values ...string) bool {
	for _, value := range values {
		// encoding/json would silently replace invalid UTF-8, losing exact content
		// identity. PostgreSQL text cannot preserve a NUL byte either.
		if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
			return false
		}
	}
	return true
}

func messageAdmissionScopePrefix(input MessageAdmissionInput) string {
	laneID, _ := input.Fence.ThreadID()
	return input.OrgID.String() + ":" + input.Fence.SessionID().String() + ":" + laneID.String() + ":"
}

func cloneMessageAdmissionInput(input MessageAdmissionInput) MessageAdmissionInput {
	input.ActorID = cloneMessageAdmissionPointer(input.ActorID)
	input.ChangesetID = cloneMessageAdmissionPointer(input.ChangesetID)
	input.LeaseHolderID = cloneMessageAdmissionPointer(input.LeaseHolderID)
	input.ContinuationDedupeKeyOverride = cloneMessageAdmissionPointer(input.ContinuationDedupeKeyOverride)
	input.Images = slices.Clone(input.Images)
	input.References = slices.Clone(input.References)
	input.Commands = slices.Clone(input.Commands)
	input.ResolveReviewCommentIDs = slices.Clone(input.ResolveReviewCommentIDs)
	return input
}

func cloneMessageAdmissionPointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (v MessageAdmission) Input() MessageAdmissionInput        { return cloneMessageAdmissionInput(v.input) }
func (v MessageAdmission) OrgID() uuid.UUID                    { return v.input.OrgID }
func (v MessageAdmission) Fence() AdmissionFence               { return v.input.Fence }
func (v MessageAdmission) Ingress() LifecycleIngress           { return v.input.Ingress }
func (v MessageAdmission) ActorID() *uuid.UUID                 { return cloneMessageAdmissionPointer(v.input.ActorID) }
func (v MessageAdmission) Source() models.SessionMessageSource { return v.input.Source }
func (v MessageAdmission) Message() string                     { return v.input.Message }
func (v MessageAdmission) Images() []string                    { return slices.Clone(v.input.Images) }
func (v MessageAdmission) References() models.SessionInputReferences {
	return slices.Clone(v.input.References)
}
func (v MessageAdmission) Commands() models.SessionInputCommands {
	return slices.Clone(v.input.Commands)
}
func (v MessageAdmission) ChangesetID() *uuid.UUID {
	return cloneMessageAdmissionPointer(v.input.ChangesetID)
}
func (v MessageAdmission) LeaseHolderID() *uuid.UUID {
	return cloneMessageAdmissionPointer(v.input.LeaseHolderID)
}
func (v MessageAdmission) ResolveReviewCommentIDs() []uuid.UUID {
	return slices.Clone(v.input.ResolveReviewCommentIDs)
}
func (v MessageAdmission) PlanMode() bool { return v.input.PlanMode }
func (v MessageAdmission) PostSuccessAction() MessagePostSuccessAction {
	return v.input.PostSuccessAction
}
func (v MessageAdmission) PostSuccessAuthorMode() MessagePostSuccessAuthorMode {
	return v.input.PostSuccessAuthorMode
}
func (v MessageAdmission) ContinuationDedupeKeyOverride() *string {
	return cloneMessageAdmissionPointer(v.input.ContinuationDedupeKeyOverride)
}
func (v MessageAdmission) Dispatch() MessageAdmissionDispatchOptions { return v.input.Dispatch }
func (v MessageAdmission) SemanticPayload() json.RawMessage          { return slices.Clone(v.semanticPayload) }
