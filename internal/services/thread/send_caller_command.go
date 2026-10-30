package thread

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

// CallerSendPurpose is a closed namespace for caller-owned initial commands.
// It grants no admission, observation, retry, or runtime-start authority.
type CallerSendPurpose string

const (
	CallerSendCodeReviewReviewerDispatch CallerSendPurpose = "code-review-reviewer-dispatch"
	CallerSendReviewLoopInitialSend      CallerSendPurpose = "review-loop-initial-send"
	// CallerSendCommandMaxEncodedBytes bounds this codec's raw input and compact
	// canonical JSON envelope. PostgreSQL JSONB presentation may be larger; this
	// is not a database storage-limit or transaction-size guarantee.
	CallerSendCommandMaxEncodedBytes = 8 << 20
	callerSendCommandDigestDomain    = "143/caller-send-command/v1\x00"
)

// CallerSendOwnerScope must come from independently retained owner columns,
// not from the envelope being decoded. Matching it is correlation, not access.
type CallerSendOwnerScope struct {
	Purpose         CallerSendPurpose
	OwnerID         uuid.UUID
	OrgID           uuid.UUID
	SessionID       uuid.UUID
	ThreadID        uuid.UUID
	ClientMessageID string
}

// CallerSendCommand retains complete effective input, not proof of acceptance.
// Private fields prevent implicit public JSON exposure. Owner preparation,
// outside this codec, supplies the identity; all codec operations are pure.
type CallerSendCommand struct {
	ownerID uuid.UUID
	payload callerSendEnvelope
	encoded []byte
	digest  string
}

// CallerSendCommandProof is opaque, nonsemantic correlation transport. It
// grants no authority and does not itself establish durable acceptance.
type CallerSendCommandProof struct {
	version int
	purpose CallerSendPurpose
	ownerID uuid.UUID
	digest  string
}

var ErrCallerCommandMismatch = errors.New("caller send command proof mismatch")

// CallerCommandMismatchError denotes a known prewrite validation refusal, not
// an unknown send outcome or permission to replay a command.
type CallerCommandMismatchError struct{}

func (*CallerCommandMismatchError) Error() string        { return ErrCallerCommandMismatch.Error() }
func (*CallerCommandMismatchError) Is(target error) bool { return target == ErrCallerCommandMismatch }

func (p CallerSendCommandProof) Version() int               { return p.version }
func (p CallerSendCommandProof) Purpose() CallerSendPurpose { return p.purpose }
func (p CallerSendCommandProof) OwnerID() uuid.UUID         { return p.ownerID }
func (p CallerSendCommandProof) Digest() string             { return p.digest }

// Proof returns a fresh immutable descriptor only for initialized retained
// commands. It is intentionally excluded from its own semantic digest.
func (c CallerSendCommand) Proof() (*CallerSendCommandProof, error) {
	if len(c.encoded) == 0 || c.payload.Version != 1 || c.digest == "" || !validCallerSendKey(c.payload.Purpose, c.ownerID, c.payload.ClientMessageID) {
		return nil, &CallerCommandMismatchError{}
	}
	return &CallerSendCommandProof{version: 1, purpose: c.payload.Purpose, ownerID: c.ownerID, digest: c.digest}, nil
}

// ValidateCallerSendCommandProof is a pure prewrite correlation check. Absent
// transport preserves existing behavior. Callers must separately establish
// current owner authority and perform any stamp write atomically with admission.
func ValidateCallerSendCommandProof(input SendMessageInput) error {
	if input.CallerCommand == nil {
		return nil
	}
	proof := *input.CallerCommand
	if proof.version != 1 || !validCallerSendKey(proof.purpose, proof.ownerID, input.ClientMessageID) {
		return &CallerCommandMismatchError{}
	}
	// The constructor ignores transport for semantics; compare against the
	// actual supplied descriptor, never a newly generated replacement proof.
	command, err := NewCallerSendCommand(proof.purpose, proof.ownerID, input)
	if err != nil || proof.digest != command.Digest() {
		return &CallerCommandMismatchError{}
	}
	return nil
}

type callerSendEnvelope struct {
	Version                       int                           `json:"version"`
	Purpose                       CallerSendPurpose             `json:"purpose"`
	OrgID                         uuid.UUID                     `json:"org_id"`
	SessionID                     uuid.UUID                     `json:"session_id"`
	ThreadID                      uuid.UUID                     `json:"thread_id"`
	ChangesetID                   *uuid.UUID                    `json:"changeset_id"`
	LegacyChangesetPolicy         LegacyChangesetPolicy         `json:"legacy_changeset_policy"`
	ClientMessageID               string                        `json:"client_message_id"`
	UserID                        *uuid.UUID                    `json:"user_id"`
	Message                       string                        `json:"message"`
	Images                        []string                      `json:"images"`
	References                    models.SessionInputReferences `json:"references"`
	Commands                      models.SessionInputCommands   `json:"commands"`
	MessageSource                 models.SessionMessageSource   `json:"message_source"`
	PlanMode                      bool                          `json:"plan_mode"`
	ResolveReviewCommentIDs       []uuid.UUID                   `json:"resolve_review_comment_ids"`
	PostSuccessAction             string                        `json:"post_success_action"`
	PostSuccessAuthorMode         string                        `json:"post_success_author_mode"`
	ContinuationDedupeKeyOverride *string                       `json:"continuation_dedupe_key_override"`
}

// NewCallerSendCommand deterministically derives the exact owner command key.
// A nonempty supplied key must match; it is never silently replaced.
func NewCallerSendCommand(purpose CallerSendPurpose, ownerID uuid.UUID, input SendMessageInput) (CallerSendCommand, error) {
	key := callerSendOwnerKey(purpose, ownerID)
	if key == "" || (input.ClientMessageID != "" && input.ClientMessageID != key) {
		return CallerSendCommand{}, errors.New("caller send command requires an exact owner and matching command key")
	}
	if !callerSendInputFitsSizeLowerBound(purpose, input) {
		return CallerSendCommand{}, errors.New("caller send command exceeds its encoded envelope limit")
	}
	if err := validateCallerSendInput(input); err != nil {
		return CallerSendCommand{}, err
	}
	input.ClientMessageID = key
	return retainCallerSendCommand(callerSendEnvelopeFromInput(purpose, input), ownerID)
}

// DecodeCallerSendCommand restores only exact, independently bound retained
// intent. It performs no writes, send attempts, retries, or identity minting.
func DecodeCallerSendCommand(raw []byte, digest string, owner CallerSendOwnerScope) (CallerSendCommand, error) {
	if len(raw) == 0 || len(raw) > CallerSendCommandMaxEncodedBytes || !utf8.Valid(raw) || !json.Valid(raw) {
		return CallerSendCommand{}, errors.New("invalid caller send command encoding or size")
	}
	if len(digest) != sha256.Size*2 || strings.ToLower(digest) != digest {
		return CallerSendCommand{}, errors.New("invalid caller send command digest encoding")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return CallerSendCommand{}, errors.New("invalid caller send command digest encoding")
	}
	if owner.OrgID == uuid.Nil || owner.SessionID == uuid.Nil || owner.ThreadID == uuid.Nil || !validCallerSendKey(owner.Purpose, owner.OwnerID, owner.ClientMessageID) {
		return CallerSendCommand{}, errors.New("invalid caller send owner scope")
	}
	if err := validateCallerSendJSONStrings(raw); err != nil {
		return CallerSendCommand{}, err
	}
	if err := validateCallerSendJSONFields(json.NewDecoder(bytes.NewReader(raw)), reflect.TypeFor[callerSendEnvelope]()); err != nil {
		return CallerSendCommand{}, errors.New("caller send command contains duplicate, missing, or invalid JSON fields")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var payload callerSendEnvelope
	if err := decoder.Decode(&payload); err != nil {
		return CallerSendCommand{}, errors.New("invalid caller send command fields")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return CallerSendCommand{}, errors.New("caller send command has trailing data")
	}
	if payload.Purpose != owner.Purpose || payload.OrgID != owner.OrgID || payload.SessionID != owner.SessionID || payload.ThreadID != owner.ThreadID || payload.ClientMessageID != owner.ClientMessageID {
		return CallerSendCommand{}, errors.New("caller send command does not match its independent owner")
	}
	command, err := retainCallerSendCommand(payload, owner.OwnerID)
	if err != nil {
		return CallerSendCommand{}, err
	}
	if command.digest != digest {
		return CallerSendCommand{}, errors.New("caller send command semantic digest mismatch")
	}
	return command, nil
}

func (c CallerSendCommand) Encode() ([]byte, error) {
	if len(c.encoded) == 0 {
		return nil, errors.New("uninitialized caller send command")
	}
	return bytes.Clone(c.encoded), nil
}

func (c CallerSendCommand) Digest() string             { return c.digest }
func (c CallerSendCommand) Purpose() CallerSendPurpose { return c.payload.Purpose }
func (c CallerSendCommand) ClientMessageID() string    { return c.payload.ClientMessageID }
func (c CallerSendCommand) OwnerID() uuid.UUID         { return c.ownerID }

func (c CallerSendCommand) SendInput() SendMessageInput {
	return cloneSendMessageInput(c.payload.sendInput())
}

func retainCallerSendCommand(payload callerSendEnvelope, ownerID uuid.UUID) (CallerSendCommand, error) {
	if payload.Version != 1 || !validCallerSendKey(payload.Purpose, ownerID, payload.ClientMessageID) {
		return CallerSendCommand{}, errors.New("invalid caller send command version, purpose, or key")
	}
	if err := validateCallerSendInput(payload.sendInput()); err != nil {
		return CallerSendCommand{}, err
	}
	// Normalize only empty collections. Optional pointers retain presence even
	// when their value is empty; no semantic text or ordering is normalized.
	payload = callerSendEnvelopeFromInput(payload.Purpose, payload.sendInput())
	encoded, err := json.Marshal(payload)
	if err != nil {
		return CallerSendCommand{}, fmt.Errorf("encode caller send command: %w", err)
	}
	if len(encoded) > CallerSendCommandMaxEncodedBytes {
		return CallerSendCommand{}, errors.New("caller send command exceeds its encoded envelope limit")
	}
	return CallerSendCommand{ownerID: ownerID, payload: payload, encoded: encoded, digest: callerSendCommandDigest(encoded)}, nil
}

func callerSendCommandDigest(encoded []byte) string {
	digest := sha256.Sum256(append([]byte(callerSendCommandDigestDomain), encoded...))
	return hex.EncodeToString(digest[:])
}

func callerSendEnvelopeFromInput(purpose CallerSendPurpose, input SendMessageInput) callerSendEnvelope {
	input = cloneSendMessageInput(input)
	return callerSendEnvelope{
		Version: 1, Purpose: purpose, OrgID: input.OrgID, SessionID: input.SessionID, ThreadID: input.ThreadID,
		ChangesetID: input.ChangesetID, LegacyChangesetPolicy: input.LegacyChangesetPolicy, ClientMessageID: input.ClientMessageID, UserID: input.UserID,
		Message: input.Message, Images: append([]string{}, input.Images...), References: append(models.SessionInputReferences{}, input.References...), Commands: append(models.SessionInputCommands{}, input.Commands...),
		MessageSource: input.MessageSource, PlanMode: input.PlanMode, ResolveReviewCommentIDs: append([]uuid.UUID{}, input.ResolveReviewCommentIDs...),
		PostSuccessAction: input.PostSuccessAction, PostSuccessAuthorMode: input.PostSuccessAuthorMode, ContinuationDedupeKeyOverride: input.ContinuationDedupeKeyOverride,
	}
}

func (p callerSendEnvelope) sendInput() SendMessageInput {
	return SendMessageInput{
		OrgID: p.OrgID, SessionID: p.SessionID, ThreadID: p.ThreadID, ChangesetID: p.ChangesetID, LegacyChangesetPolicy: p.LegacyChangesetPolicy, ClientMessageID: p.ClientMessageID,
		UserID: p.UserID, Message: p.Message, Images: p.Images, References: p.References, Commands: p.Commands, MessageSource: p.MessageSource, PlanMode: p.PlanMode,
		ResolveReviewCommentIDs: p.ResolveReviewCommentIDs, PostSuccessAction: p.PostSuccessAction, PostSuccessAuthorMode: p.PostSuccessAuthorMode, ContinuationDedupeKeyOverride: p.ContinuationDedupeKeyOverride,
	}
}

func callerSendKeyPrefix(purpose CallerSendPurpose) string {
	switch purpose {
	case CallerSendCodeReviewReviewerDispatch:
		return "cr-dispatch:v1:"
	case CallerSendReviewLoopInitialSend:
		return "rl-startup:v1:"
	default:
		return ""
	}
}

func callerSendOwnerKey(purpose CallerSendPurpose, ownerID uuid.UUID) string {
	prefix := callerSendKeyPrefix(purpose)
	if prefix == "" || ownerID == uuid.Nil {
		return ""
	}
	return prefix + ownerID.String()
}

func validCallerSendKey(purpose CallerSendPurpose, ownerID uuid.UUID, key string) bool {
	expected := callerSendOwnerKey(purpose, ownerID)
	return expected != "" && key == expected
}

// Reject only an obvious lower-bound overflow before cloning or marshaling.
// Raw text cannot shrink in valid JSON. Property names, escapes and most syntax
// are deliberately omitted, so a command that fits the real cap cannot fail
// this preflight. The exact canonical-size check still runs after encoding.
func callerSendInputFitsSizeLowerBound(purpose CallerSendPurpose, input SendMessageInput) bool {
	remaining := CallerSendCommandMaxEncodedBytes
	add := func(size int) bool {
		if size > remaining {
			return false
		}
		remaining -= size
		return true
	}
	// Three scope UUID strings, purpose and the derived namespace:UUID key.
	// The key has a known width; this calculation mints no identity.
	if !add(3*38) || !add(len(purpose)) || !add(2) || !add(len(callerSendKeyPrefix(purpose))) || !add(36+2) {
		return false
	}
	for _, collection := range []struct{ count, minimum int }{
		{len(input.Images), 2},                   // quoted string values
		{len(input.References), 6},               // object plus two required quoted values
		{len(input.Commands), 12},                // object plus five required quoted values
		{len(input.ResolveReviewCommentIDs), 38}, // quoted UUID values
	} {
		if !callerSendCollectionFitsSizeLowerBound(&remaining, collection.count, collection.minimum) {
			return false
		}
	}
	for _, id := range []*uuid.UUID{input.UserID, input.ChangesetID} {
		size := 4 // null
		if id != nil {
			size = 38
		}
		if !add(size) {
			return false
		}
	}
	for _, value := range []string{input.Message, string(input.LegacyChangesetPolicy), string(input.MessageSource), input.PostSuccessAction, input.PostSuccessAuthorMode} {
		if !add(len(value)) || !add(2) {
			return false
		}
	}
	if input.ContinuationDedupeKeyOverride != nil {
		if !add(len(*input.ContinuationDedupeKeyOverride)) || !add(2) {
			return false
		}
	} else if !add(4) {
		return false
	}
	for _, image := range input.Images {
		if !add(len(image)) {
			return false
		}
	}
	for _, reference := range input.References {
		for _, value := range []string{string(reference.Kind), reference.Token, reference.Path, reference.ID, reference.Display} {
			if !add(len(value)) {
				return false
			}
		}
	}
	for _, command := range input.Commands {
		for _, value := range []string{command.Kind, string(command.AgentType), command.Name, command.Token, command.Display, command.Description, command.Arguments, string(command.Source)} {
			if !add(len(value)) {
				return false
			}
		}
	}
	return true
}

func callerSendCollectionFitsSizeLowerBound(remaining *int, count, minimum int) bool {
	if count < 0 || minimum <= 0 || *remaining < 2 {
		return false
	}
	*remaining -= 2 // array brackets, including canonical empty collections
	if count == 0 {
		return true
	}
	if count-1 > *remaining {
		return false
	}
	*remaining -= count - 1 // commas
	if count > *remaining/minimum {
		return false
	}
	*remaining -= count * minimum // multiplication is bounded by the division
	return true
}

func validateCallerSendInput(input SendMessageInput) error {
	if input.OrgID == uuid.Nil || input.SessionID == uuid.Nil || input.ThreadID == uuid.Nil || (input.UserID != nil && *input.UserID == uuid.Nil) || (input.ChangesetID != nil && *input.ChangesetID == uuid.Nil) {
		return errors.New("caller send command requires nonzero scope and optional identities")
	}
	if len(input.ResolveReviewCommentIDs) != 0 || input.LegacyChangesetPolicy != LegacyChangesetUnchanged {
		return errors.New("caller send command does not support review resolution or adapter target policies")
	}
	texts := []string{input.Message, string(input.MessageSource), input.PostSuccessAction, input.PostSuccessAuthorMode}
	if input.ContinuationDedupeKeyOverride != nil {
		texts = append(texts, *input.ContinuationDedupeKeyOverride)
	}
	for _, text := range texts {
		if !validCallerSendText(text) {
			return errors.New("caller send command contains malformed text")
		}
	}
	if input.MessageSource.Validate() != nil || db.MessagePostSuccessAction(input.PostSuccessAction).Validate() != nil || db.MessagePostSuccessAuthorMode(input.PostSuccessAuthorMode).Validate() != nil {
		return errors.New("caller send command contains unsupported source or post-success options")
	}
	if strings.TrimSpace(input.Message) == "" && len(input.Images) == 0 && len(input.References) == 0 && len(input.Commands) == 0 {
		return errors.New("caller send command requires message content or attachments")
	}
	for _, image := range input.Images {
		if !validCallerSendText(image) || strings.TrimSpace(image) == "" {
			return errors.New("caller send command contains an invalid image")
		}
	}
	for _, reference := range input.References {
		if !validCallerSendTexts(string(reference.Kind), reference.Token, reference.Path, reference.ID, reference.Display) || reference.Validate() != nil {
			return errors.New("caller send command contains an invalid reference")
		}
	}
	for _, command := range input.Commands {
		if !validCallerSendTexts(command.Kind, string(command.AgentType), command.Name, command.Token, command.Display, command.Description, command.Arguments, string(command.Source)) || command.Validate() != nil {
			return errors.New("caller send command contains an invalid command")
		}
	}
	return nil
}

func validCallerSendTexts(values ...string) bool {
	for _, value := range values {
		if !validCallerSendText(value) {
			return false
		}
	}
	return true
}

func validCallerSendText(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

// encoding/json replaces unpaired UTF16 escapes. Reject them before decoding
// so even a digest of the replacement character cannot legitimize corruption.
func validateCallerSendJSONStrings(raw []byte) error {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '"' {
			continue
		}
		for i++; i < len(raw) && raw[i] != '"'; i++ {
			if raw[i] != '\\' {
				continue
			}
			i++
			if raw[i] != 'u' {
				continue
			}
			value, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
			if err != nil {
				return errors.New("invalid caller send Unicode escape")
			}
			i += 4
			if value >= 0xdc00 && value <= 0xdfff {
				return errors.New("unpaired caller send Unicode escape")
			}
			if value >= 0xd800 && value <= 0xdbff {
				if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
					return errors.New("unpaired caller send Unicode escape")
				}
				low, parseErr := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
				if parseErr != nil || low < 0xdc00 || low > 0xdfff {
					return errors.New("unpaired caller send Unicode escape")
				}
				i += 6
			}
		}
	}
	return nil
}

func validateCallerSendJSONFields(decoder *json.Decoder, expected reflect.Type) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		if expected.Kind() != reflect.Pointer && expected.Kind() != reflect.Slice {
			return errors.New("required JSON value is null")
		}
		return nil
	}
	for expected.Kind() == reflect.Pointer {
		expected = expected.Elem()
	}
	delimiter, container := token.(json.Delim)
	if !container {
		return nil
	}
	if delimiter == '[' {
		if expected.Kind() != reflect.Slice && expected.Kind() != reflect.Array {
			return errors.New("unexpected JSON array")
		}
		for decoder.More() {
			if err := validateCallerSendJSONFields(decoder, expected.Elem()); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	}
	if delimiter != '{' || expected.Kind() != reflect.Struct {
		return errors.New("unexpected JSON object")
	}
	fields := make(map[string]reflect.StructField, expected.NumField())
	for i := 0; i < expected.NumField(); i++ {
		field := expected.Field(i)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		fields[name] = field
	}
	seen := map[string]bool{}
	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := fieldToken.(string)
		field, known := fields[key]
		if !ok || !known || seen[key] {
			return errors.New("unknown or duplicate JSON field")
		}
		seen[key] = true
		if err := validateCallerSendJSONFields(decoder, field.Type); err != nil {
			return err
		}
	}
	for name, field := range fields {
		if !seen[name] && !strings.Contains(field.Tag.Get("json"), ",omitempty") {
			return errors.New("missing required JSON field")
		}
	}
	_, err = decoder.Token()
	return err
}
