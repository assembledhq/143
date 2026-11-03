package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const lifecycleModeObservationQuery = `SELECT s.org_id, s.id, s.lifecycle_version,
	c.org_id, c.session_id, c.version, c.state, c.legacy_input_cutoff_id,
	c.normalization, c.activated_at, c.minimum_writer_version, c.resumability_capability,
	s.agent_type, COALESCE((SELECT jsonb_agg(jsonb_build_object(
		'protocol_version', t.lifecycle_version, 'provider', t.agent_type) ORDER BY t.id)
		FROM session_threads t WHERE t.org_id = s.org_id AND t.session_id = s.id
		AND t.archived_at IS NULL), '[]'::jsonb)
	FROM sessions s LEFT JOIN session_lifecycle_cutovers c
	ON c.org_id = s.org_id AND c.session_id = s.id
	WHERE s.org_id = $1 AND s.id = $2`

type lifecycleMode uint8

const (
	lifecycleModeLegacy lifecycleMode = iota + 1
	lifecycleModeNormalizing
	lifecycleModeActive
)

// lifecycleModeObservation belongs only to the supplied transaction. The parent
// protocol stamp is distinct from the cutover CAS revision; neither this mode
// nor valid provenance grants admission, readiness or activation authority.
type lifecycleModeObservation struct {
	mode            lifecycleMode
	protocolVersion int16
	cutoverVersion  int64
}

// ErrLifecycleGuardProvenanceRequired means retained lifecycle state cannot be
// safely resolved. Callers must not reinterpret it as version-zero coexistence.
var ErrLifecycleGuardProvenanceRequired = errors.New("LIFECYCLE_GUARD_PROVENANCE_REQUIRED")

// LifecycleGuardProvenanceError deliberately contains no persisted values or
// scope identifiers. Its reason describes only the failed protocol invariant.
type LifecycleGuardProvenanceError struct {
	reason string
}

func (e *LifecycleGuardProvenanceError) Error() string {
	return ErrLifecycleGuardProvenanceRequired.Error() + ": " + e.reason
}

func (e *LifecycleGuardProvenanceError) Is(target error) bool {
	return target == ErrLifecycleGuardProvenanceRequired
}

type lifecycleModeSnapshot struct {
	parentAgentType        models.AgentType
	visibleLanes           json.RawMessage
	orgID, sessionID       uuid.UUID
	protocolVersion        int16
	cutoverOrgID           *uuid.UUID
	cutoverSessionID       *uuid.UUID
	cutoverVersion         *int64
	state                  *models.LifecycleCutoverState
	legacyInputCutoffID    *int64
	normalization          json.RawMessage
	activatedAt            *time.Time
	minimumWriterVersion   *int
	resumabilityCapability json.RawMessage
}

// resolveLifecycleModeInTx acquires the existing nonwaiting shared interlock,
// then reads the parent and optional cutover in a separate fresh READ COMMITTED
// statement. A missing exact parent remains ErrNoRows, including foreign scope.
// Missing cutover is legacy only when the retained parent stamp is zero. This
// private resolver installs no settings, effects, retry or transaction boundary;
// its owner must discard results and roll back the whole unit on any error.
func resolveLifecycleModeInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) (lifecycleModeObservation, error) {
	if err := tryLockLifecycleCutoverSharedInTx(ctx, tx, orgID, sessionID); err != nil {
		return lifecycleModeObservation{}, err
	}
	var snapshot lifecycleModeSnapshot
	err := tx.QueryRow(ctx, lifecycleModeObservationQuery, orgID, sessionID).
		Scan(&snapshot.orgID, &snapshot.sessionID, &snapshot.protocolVersion,
			&snapshot.cutoverOrgID, &snapshot.cutoverSessionID, &snapshot.cutoverVersion,
			&snapshot.state, &snapshot.legacyInputCutoffID, &snapshot.normalization,
			&snapshot.activatedAt, &snapshot.minimumWriterVersion, &snapshot.resumabilityCapability,
			&snapshot.parentAgentType, &snapshot.visibleLanes)
	if err != nil {
		return lifecycleModeObservation{}, fmt.Errorf("read lifecycle mode: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return lifecycleModeObservation{}, fmt.Errorf("lifecycle mode context after observation: %w", err)
	}
	if snapshot.orgID != orgID || snapshot.sessionID != sessionID {
		return lifecycleModeObservation{}, fmt.Errorf("lifecycle mode parent scope mismatch: %w", pgx.ErrNoRows)
	}
	observation, err := snapshot.resolve()
	if err != nil {
		return lifecycleModeObservation{}, err
	}
	if err := ctx.Err(); err != nil {
		return lifecycleModeObservation{}, fmt.Errorf("lifecycle mode context after validation: %w", err)
	}
	return observation, nil
}

func (s lifecycleModeSnapshot) resolve() (lifecycleModeObservation, error) {
	fail := func(reason string) (lifecycleModeObservation, error) {
		return lifecycleModeObservation{}, &LifecycleGuardProvenanceError{reason: reason}
	}
	if s.protocolVersion < 0 || s.protocolVersion > 1 {
		return fail("unsupported retained protocol stamp")
	}
	lanes, validLanes := decodeLifecycleVisibleLanes(s.visibleLanes)
	if !validLanes {
		return fail("malformed visible lane provenance")
	}
	for _, lane := range lanes {
		if lane.ProtocolVersion < 0 || (s.protocolVersion == 0 && lane.ProtocolVersion > 0) {
			return fail("inconsistent retained lane protocol stamp")
		}
	}
	if s.cutoverOrgID == nil && s.cutoverSessionID == nil && s.cutoverVersion == nil && s.state == nil {
		if s.protocolVersion != 0 || s.legacyInputCutoffID != nil || len(s.normalization) != 0 || s.activatedAt != nil || s.minimumWriterVersion != nil || len(s.resumabilityCapability) != 0 {
			return fail("missing retained cutover provenance")
		}
		return lifecycleModeObservation{mode: lifecycleModeLegacy}, nil
	}
	if s.cutoverOrgID == nil || s.cutoverSessionID == nil || s.cutoverVersion == nil || s.state == nil ||
		*s.cutoverOrgID != s.orgID || *s.cutoverSessionID != s.sessionID || *s.cutoverVersion < 0 ||
		(s.legacyInputCutoffID != nil && *s.legacyInputCutoffID <= 0) {
		return fail("inconsistent retained cutover identity")
	}
	observation := lifecycleModeObservation{protocolVersion: s.protocolVersion, cutoverVersion: *s.cutoverVersion}
	switch *s.state {
	case models.LifecycleCutoverStateShadow, models.LifecycleCutoverStateNormalizing:
		if s.protocolVersion != 0 || !s.inactiveProvenanceValid(*s.state == models.LifecycleCutoverStateNormalizing) {
			return fail("inconsistent inactive cutover provenance")
		}
		observation.mode = lifecycleModeLegacy
		if *s.state == models.LifecycleCutoverStateNormalizing {
			observation.mode = lifecycleModeNormalizing
		}
	case models.LifecycleCutoverStateActive:
		providers, validCapability := lifecycleCapabilityProviders(s.resumabilityCapability)
		if s.protocolVersion != 1 || s.activatedAt == nil || s.activatedAt.IsZero() ||
			s.legacyInputCutoffID == nil || s.minimumWriterVersion == nil || *s.minimumWriterVersion != 1 ||
			!validLifecycleNormalization(s.normalization, s.legacyInputCutoffID, true) || !validCapability || !s.activeProviderProvenanceValid(lanes, providers) {
			return fail("incomplete or unsupported active cutover provenance")
		}
		observation.mode = lifecycleModeActive
	default:
		return fail("unsupported cutover state")
	}
	return observation, nil
}

func (s lifecycleModeSnapshot) inactiveProvenanceValid(requireNormalization bool) bool {
	return s.activatedAt == nil && (s.minimumWriterVersion == nil || *s.minimumWriterVersion == 0) &&
		len(s.resumabilityCapability) == 0 && ((!requireNormalization && len(s.normalization) == 0) || validLifecycleNormalization(s.normalization, s.legacyInputCutoffID, false))
}

func validLifecycleNormalization(raw json.RawMessage, cutoff *int64, complete bool) bool {
	var envelope struct {
		SchemaVersion  int       `json:"schema_version"`
		ReceiptID      uuid.UUID `json:"receipt_id"`
		FloorReceiptID uuid.UUID `json:"floor_receipt_id"`
		CutoffID       int64     `json:"cutoff_id"`
		Complete       bool      `json:"complete"`
	}
	return decodeLifecycleProvenanceObject(raw, &envelope, "schema_version", "receipt_id", "floor_receipt_id", "cutoff_id", "complete") &&
		envelope.SchemaVersion == 1 && envelope.ReceiptID != uuid.Nil && envelope.FloorReceiptID != uuid.Nil &&
		cutoff != nil && envelope.CutoffID > 0 && envelope.CutoffID == *cutoff && envelope.Complete == complete
}

func lifecycleCapabilityProviders(raw json.RawMessage) (map[models.AgentType]struct{}, bool) {
	var envelope struct {
		SchemaVersion    int               `json:"schema_version"`
		ReleaseReceiptID uuid.UUID         `json:"release_receipt_id"`
		Tuples           []json.RawMessage `json:"tuples"`
	}
	if !decodeLifecycleProvenanceObject(raw, &envelope, "schema_version", "release_receipt_id", "tuples") ||
		envelope.SchemaVersion != 1 || envelope.ReleaseReceiptID == uuid.Nil || len(envelope.Tuples) == 0 {
		return nil, false
	}
	providers := make(map[models.AgentType]struct{}, len(envelope.Tuples))
	for _, rawTuple := range envelope.Tuples {
		var tuple struct {
			Provider             models.AgentType   `json:"provider"`
			CLIBuild             string             `json:"cli_build"`
			ImageBuild           string             `json:"image_build"`
			ResumeIdentity       ResumeIdentityKind `json:"resume_identity"`
			VerificationProtocol string             `json:"verification_protocol"`
			ConformanceReceiptID uuid.UUID          `json:"conformance_receipt_id"`
		}
		if !decodeLifecycleProvenanceObject(rawTuple, &tuple, "provider", "cli_build", "image_build", "resume_identity", "verification_protocol", "conformance_receipt_id") ||
			tuple.ResumeIdentity.Validate() != nil || tuple.ConformanceReceiptID == uuid.Nil ||
			tuple.Provider.Validate() != nil || !exactLifecycleCapabilityIdentity(tuple.CLIBuild) ||
			!exactLifecycleCapabilityIdentity(tuple.ImageBuild) || !exactLifecycleCapabilityIdentity(tuple.VerificationProtocol) {
			return nil, false
		}
		providers[tuple.Provider] = struct{}{}
	}
	return providers, true
}

type lifecycleVisibleLane struct {
	ProtocolVersion int16            `json:"protocol_version"`
	Provider        models.AgentType `json:"provider"`
}

func decodeLifecycleVisibleLanes(raw json.RawMessage) ([]lifecycleVisibleLane, bool) {
	var rows []json.RawMessage
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &rows) != nil {
		return nil, false
	}
	lanes := make([]lifecycleVisibleLane, 0, len(rows))
	for _, row := range rows {
		var lane lifecycleVisibleLane
		if !decodeLifecycleProvenanceObject(row, &lane, "protocol_version", "provider") {
			return nil, false
		}
		lanes = append(lanes, lane)
	}
	return lanes, true
}

// These observations are mode evidence, not a locked lane admission or target
// permit. No lane is inferred from a primary pointer; an empty visible set stays
// representable for later explicit session-wide operations.
func (s lifecycleModeSnapshot) activeProviderProvenanceValid(lanes []lifecycleVisibleLane, providers map[models.AgentType]struct{}) bool {
	if s.parentAgentType.Validate() != nil {
		return false
	}
	if _, ok := providers[s.parentAgentType]; !ok {
		return false
	}
	for _, lane := range lanes {
		if lane.ProtocolVersion != 1 || lane.Provider.Validate() != nil {
			return false
		}
		if _, ok := providers[lane.Provider]; !ok {
			return false
		}
	}
	return true
}

func exactLifecycleCapabilityIdentity(value string) bool {
	return value != "" && !strings.ContainsAny(value, "*?") && strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) == -1
}

// decodeLifecycleProvenanceObject requires the exact supported shape. A normal
// struct decode alone would accept duplicate or case-folded keys and JSON null,
// allowing ambiguous future metadata to silently acquire legacy/active meaning.
func decodeLifecycleProvenanceObject(raw json.RawMessage, destination any, fields ...string) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	seen := make(map[string]bool, len(fields))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		field, ok := token.(string)
		if !ok || seen[field] {
			return false
		}
		seen[field] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') || len(seen) != len(fields) {
		return false
	}
	for _, field := range fields {
		if !seen[field] {
			return false
		}
	}
	var extra any
	return decoder.Decode(&extra) == io.EOF && json.Unmarshal(raw, destination) == nil
}
