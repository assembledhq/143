package thread

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
)

// LegacyChangesetPolicy preserves an adapter's version-zero target preflight.
// It is server compatibility policy, not client intent, physical availability
// authority, or permission to start an active-protocol job.
type LegacyChangesetPolicy string

const (
	LegacyChangesetUnchanged           LegacyChangesetPolicy = ""
	LegacyChangesetRequireMaterialized LegacyChangesetPolicy = "require_materialized"
	LegacyChangesetUnconfigured        LegacyChangesetPolicy = "unconfigured"
)

func (p LegacyChangesetPolicy) Validate() error {
	switch p {
	case LegacyChangesetUnchanged, LegacyChangesetRequireMaterialized, LegacyChangesetUnconfigured:
		return nil
	default:
		return errors.New("invalid legacy changeset policy")
	}
}

var (
	ErrSendChangesetsNotConfigured  = errors.New("thread changeset targeting is not configured")
	ErrSendChangesetNotFound        = errors.New("thread changeset target was not found")
	ErrSendChangesetNotMaterialized = errors.New("thread changeset target is not materialized")
)

// requireLegacyChangesetTarget runs only after the sole router chose legacy and
// before its original business prechecks. Direct callers keep their previous
// behavior by default. Active admission and exact replay must never call it.
func (s *Service) requireLegacyChangesetTarget(ctx context.Context, input SendMessageInput) error {
	if input.ChangesetID == nil || input.LegacyChangesetPolicy == LegacyChangesetUnchanged {
		return nil
	}
	if input.LegacyChangesetPolicy != LegacyChangesetRequireMaterialized || nilSendDependency(s.send.Changesets) {
		return ErrSendChangesetsNotConfigured
	}
	if input.OrgID == uuid.Nil || input.SessionID == uuid.Nil || *input.ChangesetID == uuid.Nil {
		return ErrSendChangesetNotFound
	}
	changeset, err := s.send.Changesets.GetByID(ctx, input.OrgID, input.SessionID, *input.ChangesetID)
	if err != nil || changeset.ID != *input.ChangesetID || changeset.OrgID != input.OrgID || changeset.SessionID != input.SessionID {
		return ErrSendChangesetNotFound
	}
	if changeset.WorktreePath == nil || strings.TrimSpace(*changeset.WorktreePath) == "" {
		return ErrSendChangesetNotMaterialized
	}
	return nil
}
