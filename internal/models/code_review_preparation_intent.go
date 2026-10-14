package models

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"

	"github.com/google/uuid"
)

const CodeReviewPreparationIntentKey = "terminal_preparation_intent"
const CodeReviewPreparationIntentConsumedKey = "terminal_preparation_intent_consumed"

// CodeReviewPreparationFailureClass contains no command output or arbitrary error text.
type CodeReviewPreparationFailureClass string

const (
	CodeReviewPreparationFailureCleanup   CodeReviewPreparationFailureClass = "cleanup_unconfirmed"
	CodeReviewPreparationFailureIntegrity CodeReviewPreparationFailureClass = "source_integrity"
	CodeReviewPreparationFailureDeadline  CodeReviewPreparationFailureClass = "deadline"
	CodeReviewPreparationFailureSetup     CodeReviewPreparationFailureClass = "setup_failed"
)

func (c CodeReviewPreparationFailureClass) Validate() error {
	switch c {
	case CodeReviewPreparationFailureCleanup, CodeReviewPreparationFailureIntegrity, CodeReviewPreparationFailureDeadline, CodeReviewPreparationFailureSetup:
		return nil
	}
	return fmt.Errorf("invalid review preparation failure class")
}

// CodeReviewPreparationTurnScope distinguishes explicit thread turns from an
// unscoped session turn; recovery never infers a sibling thread identity.
type CodeReviewPreparationTurnScope string

const (
	CodeReviewPreparationTurnSession CodeReviewPreparationTurnScope = "session"
	CodeReviewPreparationTurnThread  CodeReviewPreparationTurnScope = "thread"
)

func (s CodeReviewPreparationTurnScope) Validate() error {
	switch s {
	case CodeReviewPreparationTurnSession, CodeReviewPreparationTurnThread:
		return nil
	}
	return fmt.Errorf("invalid review preparation turn scope")
}

type CodeReviewPreparationStage string

const (
	CodeReviewPreparationStageClone           CodeReviewPreparationStage = "clone"
	CodeReviewPreparationStageHydration       CodeReviewPreparationStage = "hydration"
	CodeReviewPreparationStageCheckout        CodeReviewPreparationStage = "checkout"
	CodeReviewPreparationStageDependencySetup CodeReviewPreparationStage = "dependency_setup"
	CodeReviewPreparationStageReadiness       CodeReviewPreparationStage = "readiness"
)

func (s CodeReviewPreparationStage) Validate() error {
	switch s {
	case CodeReviewPreparationStageClone, CodeReviewPreparationStageHydration, CodeReviewPreparationStageCheckout, CodeReviewPreparationStageDependencySetup, CodeReviewPreparationStageReadiness:
		return nil
	}
	return fmt.Errorf("invalid review preparation intent stage")
}

// CodeReviewPreparationIntent is captured from joined durable identities before
// ContinueSession. It becomes recoverable only when its lease-owned terminal
// job update commits; it does not cover a crash before that checkpoint.
type CodeReviewPreparationIntent struct {
	Version             int                               `json:"version"`
	OrgID               uuid.UUID                         `json:"org_id"`
	JobID               uuid.UUID                         `json:"job_id"`
	MetadataID          uuid.UUID                         `json:"metadata_id"`
	SessionID           uuid.UUID                         `json:"session_id"`
	PullRequestID       uuid.UUID                         `json:"pull_request_id"`
	HeadSHA             string                            `json:"head_sha"`
	WorkspaceGeneration int64                             `json:"workspace_generation"`
	ThreadID            *uuid.UUID                        `json:"thread_id"`
	Turn                int                               `json:"turn"`
	TurnScope           CodeReviewPreparationTurnScope    `json:"turn_scope"`
	Recheck             bool                              `json:"recheck"`
	FailureClass        CodeReviewPreparationFailureClass `json:"failure_class"`
	Stage               CodeReviewPreparationStage        `json:"stage"`
}

var preparationIntentHead = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)

func (i CodeReviewPreparationIntent) Validate() error {
	if i.Version != 1 || i.OrgID == uuid.Nil || i.JobID == uuid.Nil || i.MetadataID == uuid.Nil || i.SessionID == uuid.Nil || i.PullRequestID == uuid.Nil || !preparationIntentHead.MatchString(i.HeadSHA) || i.WorkspaceGeneration < 0 || i.Turn < 1 || i.Recheck {
		return fmt.Errorf("incomplete review preparation intent provenance")
	}
	if i.ThreadID == nil {
		if i.TurnScope != CodeReviewPreparationTurnSession {
			return fmt.Errorf("inferred intent requires session turn scope")
		}
	} else if *i.ThreadID == uuid.Nil || i.TurnScope != CodeReviewPreparationTurnThread {
		return fmt.Errorf("explicit intent requires a thread identity and turn scope")
	}
	if err := i.TurnScope.Validate(); err != nil {
		return err
	}
	if err := i.Stage.Validate(); err != nil {
		return err
	}
	return i.FailureClass.Validate()
}

// DecodeCodeReviewPreparationIntent rejects missing zero-valued fields and
// unknown extensions rather than guessing provenance from last_error.
func DecodeCodeReviewPreparationIntent(raw []byte) (CodeReviewPreparationIntent, error) {
	var i CodeReviewPreparationIntent
	if len(raw) == 0 || len(raw) > 2048 {
		return i, fmt.Errorf("invalid review preparation intent size")
	}
	fields := make(map[string]json.RawMessage)
	fieldDecoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := fieldDecoder.Token()
	if err != nil || start != json.Delim('{') {
		return i, fmt.Errorf("review preparation intent must be an object")
	}
	for fieldDecoder.More() {
		token, err := fieldDecoder.Token()
		if err != nil {
			return i, fmt.Errorf("decode review preparation intent field: %w", err)
		}
		key, ok := token.(string)
		if !ok {
			return i, fmt.Errorf("invalid review preparation intent key")
		}
		if _, exists := fields[key]; exists {
			return i, fmt.Errorf("duplicate review preparation intent field %s", key)
		}
		var value json.RawMessage
		if err := fieldDecoder.Decode(&value); err != nil {
			return i, fmt.Errorf("decode review preparation intent value: %w", err)
		}
		fields[key] = value
	}
	if _, err := fieldDecoder.Token(); err != nil {
		return i, fmt.Errorf("decode review preparation intent end: %w", err)
	}
	if _, err := fieldDecoder.Token(); err != io.EOF {
		return i, fmt.Errorf("trailing review preparation intent input")
	}
	for _, key := range []string{"version", "org_id", "job_id", "metadata_id", "session_id", "pull_request_id", "head_sha", "workspace_generation", "thread_id", "turn", "turn_scope", "recheck", "failure_class", "stage"} {
		if _, ok := fields[key]; !ok {
			return i, fmt.Errorf("missing review preparation intent field %s", key)
		}
		if key != "thread_id" && bytes.Equal(bytes.TrimSpace(fields[key]), []byte("null")) {
			return i, fmt.Errorf("null review preparation intent field %s", key)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&i); err != nil {
		return i, fmt.Errorf("decode review preparation intent: %w", err)
	}
	return i, i.Validate()
}
