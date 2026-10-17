package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

var ErrSessionPRIntentFrozen = errors.New("session pull request intent is frozen")

type SessionPRIntentStore struct{ db DBTX }

func NewSessionPRIntentStore(db DBTX) *SessionPRIntentStore {
	return &SessionPRIntentStore{db: db}
}

const sessionPRIntentColumns = `org_id, session_id, automation_run_id, changeset_id,
	team_reviewers, frozen_at, created_at, updated_at`

// Stage records metadata without creating publication work. The completion
// coordinator must Freeze before requesting publication. Both operations lock
// the same unique row, including when no metadata has yet been staged.
func (s *SessionPRIntentStore) Stage(ctx context.Context, orgID uuid.UUID, intent models.SessionPRIntent) error {
	if orgID == uuid.Nil || intent.OrgID != orgID || intent.SessionID == uuid.Nil || intent.AutomationRunID == uuid.Nil || intent.ChangesetID == uuid.Nil {
		return errors.New("session pull request intent requires matching organization and scope")
	}
	if err := validateCanonicalTeamReviewers(intent.TeamReviewers); err != nil {
		return err
	}
	teams := intent.TeamReviewers
	if teams == nil {
		teams = []string{}
	}
	var sessionID uuid.UUID
	err := s.db.QueryRow(ctx, `INSERT INTO session_pr_intents (
		org_id, session_id, automation_run_id, changeset_id, team_reviewers
	)
	SELECT sal.org_id, sal.session_id, sal.automation_run_id, sc.id, $5::text[]
	FROM session_automation_links sal
	JOIN automation_runs ar ON ar.id = sal.automation_run_id AND ar.org_id = sal.org_id
	JOIN sessions sess ON sess.id = sal.session_id AND sess.org_id = sal.org_id
	JOIN repositories repo ON repo.id = sess.repository_id AND repo.org_id = sess.org_id
	JOIN session_changesets sc ON sc.session_id = sal.session_id AND sc.org_id = sal.org_id AND sc.is_primary
	WHERE sal.org_id = $1 AND sal.session_id = $2 AND sal.automation_run_id = $3 AND sc.id = $4
	  AND NOT EXISTS (
		SELECT 1 FROM unnest($5::text[]) team
		WHERE split_part(team, '/', 1) <> lower(split_part(repo.full_name, '/', 1))
	  )
	  AND NOT EXISTS (
		SELECT 1 FROM session_publications p WHERE p.org_id = $1 AND p.changeset_id = $4
	  )
	ON CONFLICT (org_id, session_id, automation_run_id, changeset_id) DO UPDATE
	SET team_reviewers = EXCLUDED.team_reviewers, updated_at = now()
	WHERE session_pr_intents.frozen_at IS NULL
	RETURNING session_id`, orgID, intent.SessionID, intent.AutomationRunID, intent.ChangesetID, teams).Scan(&sessionID)
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var frozen bool
	err = s.db.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM session_pr_intents
		WHERE org_id = $1 AND session_id = $2 AND automation_run_id = $3 AND changeset_id = $4 AND frozen_at IS NOT NULL
	)`, orgID, intent.SessionID, intent.AutomationRunID, intent.ChangesetID).Scan(&frozen)
	if err != nil {
		return err
	}
	if frozen {
		return ErrSessionPRIntentFrozen
	}
	return pgx.ErrNoRows
}

// Freeze returns an immutable snapshot, inserting an empty one when the agent
// did not provide metadata. Replays retain the original snapshot and timestamp.
func (s *SessionPRIntentStore) Freeze(ctx context.Context, orgID, sessionID, automationRunID, changesetID uuid.UUID) (models.SessionPRIntent, error) {
	if orgID == uuid.Nil || sessionID == uuid.Nil || automationRunID == uuid.Nil || changesetID == uuid.Nil {
		return models.SessionPRIntent{}, errors.New("session pull request intent requires complete scope")
	}
	rows, err := s.db.Query(ctx, `INSERT INTO session_pr_intents (
		org_id, session_id, automation_run_id, changeset_id, frozen_at
	)
	SELECT sal.org_id, sal.session_id, sal.automation_run_id, sc.id, now()
	FROM session_automation_links sal
	JOIN automation_runs ar ON ar.id = sal.automation_run_id AND ar.org_id = sal.org_id
	JOIN session_changesets sc ON sc.session_id = sal.session_id AND sc.org_id = sal.org_id AND sc.is_primary
	WHERE sal.org_id = $1 AND sal.session_id = $2 AND sal.automation_run_id = $3 AND sc.id = $4
	ON CONFLICT (org_id, session_id, automation_run_id, changeset_id) DO UPDATE
	SET frozen_at = COALESCE(session_pr_intents.frozen_at, EXCLUDED.frozen_at),
		updated_at = CASE WHEN session_pr_intents.frozen_at IS NULL THEN now() ELSE session_pr_intents.updated_at END
	RETURNING `+sessionPRIntentColumns, orgID, sessionID, automationRunID, changesetID)
	if err != nil {
		return models.SessionPRIntent{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionPRIntent])
}

func validateCanonicalTeamReviewers(teams []string) error {
	if len(teams) > models.MaxTeamReviewers {
		return fmt.Errorf("at most %d team reviewers may be requested", models.MaxTeamReviewers)
	}
	seen := make(map[string]bool, len(teams))
	for _, team := range teams {
		parts := strings.Split(team, "/")
		if len(parts) != 2 {
			return errors.New("team reviewers must include their organization")
		}
		normalized, err := models.NormalizeTeamReviewers(parts[0]+"/repository", []string{team})
		if err != nil || len(normalized) != 1 || normalized[0] != team || seen[team] {
			return errors.New("team reviewers must be canonical and unique")
		}
		seen[team] = true
	}
	return nil
}
