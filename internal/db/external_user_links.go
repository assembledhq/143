package db

import (
	"context"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const externalUserLinkSelectColumns = `id, org_id, provider, provider_workspace_id, provider_user_id,
	user_id, source, status, confidence, external_email, external_handle,
	external_display_name, linked_by_user_id, created_at, revoked_at`

type ExternalUserLinkStore struct {
	db DBTX
}

func NewExternalUserLinkStore(db DBTX) *ExternalUserLinkStore {
	return &ExternalUserLinkStore{db: db}
}

func (s *ExternalUserLinkStore) GetActiveByExternal(ctx context.Context, orgID uuid.UUID, provider models.ExternalIdentityProvider, workspaceID, providerUserID string) (models.ExternalUserLink, error) {
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		SELECT %s
		FROM external_user_links
		WHERE org_id = @org_id
		  AND provider = @provider
		  AND provider_workspace_id = @provider_workspace_id
		  AND provider_user_id = @provider_user_id
		  AND status = 'active'`, externalUserLinkSelectColumns),
		pgx.NamedArgs{
			"org_id":                orgID,
			"provider":              provider,
			"provider_workspace_id": workspaceID,
			"provider_user_id":      providerUserID,
		})
	if err != nil {
		return models.ExternalUserLink{}, fmt.Errorf("query active external user link: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.ExternalUserLink])
}

func (s *ExternalUserLinkStore) GetByID(ctx context.Context, orgID, linkID uuid.UUID) (models.ExternalUserLink, error) {
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		SELECT %s
		FROM external_user_links
		WHERE org_id = @org_id
		  AND id = @id`, externalUserLinkSelectColumns),
		pgx.NamedArgs{"org_id": orgID, "id": linkID})
	if err != nil {
		return models.ExternalUserLink{}, fmt.Errorf("query external user link: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.ExternalUserLink])
}

func (s *ExternalUserLinkStore) ListByOrg(ctx context.Context, orgID uuid.UUID) ([]models.ExternalUserLink, error) {
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		SELECT %s
		FROM external_user_links
		WHERE org_id = @org_id
		ORDER BY status ASC, created_at DESC`, externalUserLinkSelectColumns),
		pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query external user links: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.ExternalUserLink])
}

func (s *ExternalUserLinkStore) ListActiveByUser(ctx context.Context, orgID, userID uuid.UUID) ([]models.ExternalUserLink, error) {
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		SELECT %s
		FROM external_user_links
		WHERE org_id = @org_id
		  AND user_id = @user_id
		  AND status = 'active'
		ORDER BY provider ASC, created_at DESC`, externalUserLinkSelectColumns),
		pgx.NamedArgs{"org_id": orgID, "user_id": userID})
	if err != nil {
		return nil, fmt.Errorf("query external user links by user: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.ExternalUserLink])
}

func (s *ExternalUserLinkStore) UpsertActive(ctx context.Context, link models.ExternalUserLink) (models.ExternalUserLink, error) {
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		INSERT INTO external_user_links (
			org_id, provider, provider_workspace_id, provider_user_id, user_id,
			source, status, confidence, external_email, external_handle,
			external_display_name, linked_by_user_id
		)
		VALUES (
			@org_id, @provider, @provider_workspace_id, @provider_user_id, @user_id,
			@source, 'active', @confidence, @external_email, @external_handle,
			@external_display_name, @linked_by_user_id
		)
		ON CONFLICT (org_id, provider, provider_workspace_id, provider_user_id)
		WHERE status = 'active'
		DO UPDATE SET
			user_id = CASE
				WHEN external_user_links.source IN ('self_linked', 'admin_linked') THEN external_user_links.user_id
				ELSE EXCLUDED.user_id
			END,
			source = CASE
				WHEN external_user_links.source IN ('self_linked', 'admin_linked') THEN external_user_links.source
				ELSE EXCLUDED.source
			END,
			confidence = CASE
				WHEN external_user_links.source IN ('self_linked', 'admin_linked') THEN GREATEST(external_user_links.confidence, @trusted_confidence_min)
				ELSE EXCLUDED.confidence
			END,
			external_email = COALESCE(EXCLUDED.external_email, external_user_links.external_email),
			external_handle = COALESCE(EXCLUDED.external_handle, external_user_links.external_handle),
			external_display_name = COALESCE(EXCLUDED.external_display_name, external_user_links.external_display_name),
			linked_by_user_id = COALESCE(EXCLUDED.linked_by_user_id, external_user_links.linked_by_user_id)
		RETURNING %s`, externalUserLinkSelectColumns),
		pgx.NamedArgs{
			"org_id":                 link.OrgID,
			"provider":               link.Provider,
			"provider_workspace_id":  link.ProviderWorkspaceID,
			"provider_user_id":       link.ProviderUserID,
			"user_id":                link.UserID,
			"source":                 link.Source,
			"confidence":             link.Confidence,
			"external_email":         link.ExternalEmail,
			"external_handle":        link.ExternalHandle,
			"external_display_name":  link.ExternalDisplayName,
			"linked_by_user_id":      link.LinkedByUserID,
			"trusted_confidence_min": 90,
		})
	if err != nil {
		return models.ExternalUserLink{}, fmt.Errorf("upsert active external user link: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.ExternalUserLink])
}

func (s *ExternalUserLinkStore) UpsertAdminActive(ctx context.Context, link models.ExternalUserLink) (models.ExternalUserLink, error) {
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		INSERT INTO external_user_links (
			org_id, provider, provider_workspace_id, provider_user_id, user_id,
			source, status, confidence, external_email, external_handle,
			external_display_name, linked_by_user_id
		)
		VALUES (
			@org_id, @provider, @provider_workspace_id, @provider_user_id, @user_id,
			'admin_linked', 'active', @confidence, @external_email, @external_handle,
			@external_display_name, @linked_by_user_id
		)
		ON CONFLICT (org_id, provider, provider_workspace_id, provider_user_id)
		WHERE status = 'active'
		DO UPDATE SET
			user_id = EXCLUDED.user_id,
			source = 'admin_linked',
			confidence = EXCLUDED.confidence,
			external_email = COALESCE(EXCLUDED.external_email, external_user_links.external_email),
			external_handle = COALESCE(EXCLUDED.external_handle, external_user_links.external_handle),
			external_display_name = COALESCE(EXCLUDED.external_display_name, external_user_links.external_display_name),
			linked_by_user_id = COALESCE(EXCLUDED.linked_by_user_id, external_user_links.linked_by_user_id)
		RETURNING %s`, externalUserLinkSelectColumns),
		pgx.NamedArgs{
			"org_id":                link.OrgID,
			"provider":              link.Provider,
			"provider_workspace_id": link.ProviderWorkspaceID,
			"provider_user_id":      link.ProviderUserID,
			"user_id":               link.UserID,
			"confidence":            link.Confidence,
			"external_email":        link.ExternalEmail,
			"external_handle":       link.ExternalHandle,
			"external_display_name": link.ExternalDisplayName,
			"linked_by_user_id":     link.LinkedByUserID,
		})
	if err != nil {
		return models.ExternalUserLink{}, fmt.Errorf("upsert admin external user link: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.ExternalUserLink])
}

func (s *ExternalUserLinkStore) ApproveSuggestion(ctx context.Context, orgID, suggestionID uuid.UUID, linkedByUserID *uuid.UUID) (models.ExternalUserLink, error) {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return models.ExternalUserLink{}, fmt.Errorf("external user link store does not support transactions")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return models.ExternalUserLink{}, fmt.Errorf("begin external user link suggestion approval: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	suggestionStore := NewExternalUserLinkSuggestionStore(tx)
	suggestion, err := suggestionStore.GetOpenByID(ctx, orgID, suggestionID)
	if err != nil {
		return models.ExternalUserLink{}, err
	}
	if _, err := NewOrganizationMembershipStore(tx).Get(ctx, suggestion.SuggestedUserID, orgID); err != nil {
		return models.ExternalUserLink{}, err
	}
	linkStore := NewExternalUserLinkStore(tx)
	link, err := linkStore.UpsertAdminActive(ctx, models.ExternalUserLink{
		OrgID:               orgID,
		Provider:            suggestion.Provider,
		ProviderWorkspaceID: suggestion.ProviderWorkspaceID,
		ProviderUserID:      suggestion.ProviderUserID,
		UserID:              suggestion.SuggestedUserID,
		Confidence:          90,
		ExternalEmail:       suggestion.ExternalEmail,
		ExternalHandle:      suggestion.ExternalHandle,
		ExternalDisplayName: suggestion.ExternalDisplayName,
		LinkedByUserID:      linkedByUserID,
	})
	if err != nil {
		return models.ExternalUserLink{}, err
	}
	if err := suggestionStore.Dismiss(ctx, orgID, suggestionID); err != nil {
		return models.ExternalUserLink{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return models.ExternalUserLink{}, fmt.Errorf("commit external user link suggestion approval: %w", err)
	}
	return link, nil
}

func (s *ExternalUserLinkStore) Revoke(ctx context.Context, orgID, linkID uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE external_user_links
		SET status = 'revoked',
		    revoked_at = now()
		WHERE org_id = @org_id
		  AND id = @id
		  AND status = 'active'`,
		pgx.NamedArgs{"org_id": orgID, "id": linkID})
	if err != nil {
		return fmt.Errorf("revoke external user link: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *ExternalUserLinkStore) RevokeActiveByExternal(ctx context.Context, orgID uuid.UUID, provider models.ExternalIdentityProvider, workspaceID, providerUserID string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE external_user_links
		SET status = 'revoked',
		    revoked_at = now()
		WHERE org_id = @org_id
		  AND provider = @provider
		  AND provider_workspace_id = @provider_workspace_id
		  AND provider_user_id = @provider_user_id
		  AND status = 'active'`,
		pgx.NamedArgs{
			"org_id":                orgID,
			"provider":              provider,
			"provider_workspace_id": workspaceID,
			"provider_user_id":      providerUserID,
		})
	if err != nil {
		return fmt.Errorf("revoke active external user link by provider identity: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *ExternalUserLinkStore) RevokeSelfLinkedByUser(ctx context.Context, orgID, userID uuid.UUID, provider models.ExternalIdentityProvider, workspaceID string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE external_user_links
		SET status = 'revoked',
		    revoked_at = now()
		WHERE org_id = @org_id
		  AND provider = @provider
		  AND provider_workspace_id = @provider_workspace_id
		  AND user_id = @user_id
		  AND source = 'self_linked'
		  AND status = 'active'`,
		pgx.NamedArgs{
			"org_id":                orgID,
			"provider":              provider,
			"provider_workspace_id": workspaceID,
			"user_id":               userID,
		})
	if err != nil {
		return fmt.Errorf("revoke self-linked external user link: %w", err)
	}
	return nil
}

func (s *ExternalUserLinkStore) ClaimSelfLink(ctx context.Context, orgID uuid.UUID, tokenHash []byte, claimingUserID uuid.UUID) (models.ExternalUserLink, error) {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return models.ExternalUserLink{}, fmt.Errorf("external user link store does not support transactions")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return models.ExternalUserLink{}, fmt.Errorf("begin external user link claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := NewOrganizationMembershipStore(tx).Get(ctx, claimingUserID, orgID); err != nil {
		return models.ExternalUserLink{}, err
	}

	var claim models.ExternalUserLinkClaim
	err = tx.QueryRow(ctx, `
		SELECT id, org_id, provider, provider_workspace_id, provider_user_id,
		       source_context, expires_at, claimed_by_user_id, claimed_at, created_at
		FROM external_user_link_claims
		WHERE org_id = @org_id
		  AND token_hash = @token_hash
		  AND claimed_at IS NULL
		  AND expires_at > now()
		FOR UPDATE`,
		pgx.NamedArgs{"org_id": orgID, "token_hash": tokenHash},
	).Scan(
		&claim.ID,
		&claim.OrgID,
		&claim.Provider,
		&claim.ProviderWorkspaceID,
		&claim.ProviderUserID,
		&claim.SourceContext,
		&claim.ExpiresAt,
		&claim.ClaimedByUserID,
		&claim.ClaimedAt,
		&claim.CreatedAt,
	)
	if err != nil {
		return models.ExternalUserLink{}, err
	}

	link, err := NewExternalUserLinkStore(tx).UpsertSelfActive(ctx, models.ExternalUserLink{
		OrgID:               orgID,
		Provider:            claim.Provider,
		ProviderWorkspaceID: claim.ProviderWorkspaceID,
		ProviderUserID:      claim.ProviderUserID,
		UserID:              claimingUserID,
		Confidence:          100,
		LinkedByUserID:      &claimingUserID,
	})
	if err != nil {
		return models.ExternalUserLink{}, err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE external_user_link_claims
		SET claimed_by_user_id = @claimed_by_user_id,
		    claimed_at = now()
		WHERE org_id = @org_id
		  AND id = @id
		  AND claimed_at IS NULL`,
		pgx.NamedArgs{"org_id": orgID, "id": claim.ID, "claimed_by_user_id": claimingUserID})
	if err != nil {
		return models.ExternalUserLink{}, fmt.Errorf("mark external user link claim used: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return models.ExternalUserLink{}, pgx.ErrNoRows
	}

	if err := tx.Commit(ctx); err != nil {
		return models.ExternalUserLink{}, fmt.Errorf("commit external user link claim: %w", err)
	}
	return link, nil
}

func (s *ExternalUserLinkStore) UpsertSelfActive(ctx context.Context, link models.ExternalUserLink) (models.ExternalUserLink, error) {
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		INSERT INTO external_user_links (
			org_id, provider, provider_workspace_id, provider_user_id, user_id,
			source, status, confidence, external_email, external_handle,
			external_display_name, linked_by_user_id
		)
		VALUES (
			@org_id, @provider, @provider_workspace_id, @provider_user_id, @user_id,
			'self_linked', 'active', @confidence, @external_email, @external_handle,
			@external_display_name, @linked_by_user_id
		)
		ON CONFLICT (org_id, provider, provider_workspace_id, provider_user_id)
		WHERE status = 'active'
		DO UPDATE SET
			user_id = EXCLUDED.user_id,
			source = 'self_linked',
			confidence = EXCLUDED.confidence,
			external_email = COALESCE(EXCLUDED.external_email, external_user_links.external_email),
			external_handle = COALESCE(EXCLUDED.external_handle, external_user_links.external_handle),
			external_display_name = COALESCE(EXCLUDED.external_display_name, external_user_links.external_display_name),
			linked_by_user_id = EXCLUDED.linked_by_user_id
		RETURNING %s`, externalUserLinkSelectColumns),
		pgx.NamedArgs{
			"org_id":                link.OrgID,
			"provider":              link.Provider,
			"provider_workspace_id": link.ProviderWorkspaceID,
			"provider_user_id":      link.ProviderUserID,
			"user_id":               link.UserID,
			"confidence":            link.Confidence,
			"external_email":        link.ExternalEmail,
			"external_handle":       link.ExternalHandle,
			"external_display_name": link.ExternalDisplayName,
			"linked_by_user_id":     link.LinkedByUserID,
		})
	if err != nil {
		return models.ExternalUserLink{}, fmt.Errorf("upsert self external user link: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.ExternalUserLink])
}

type ExternalUserLinkSuggestionStore struct {
	db DBTX
}

func NewExternalUserLinkSuggestionStore(db DBTX) *ExternalUserLinkSuggestionStore {
	return &ExternalUserLinkSuggestionStore{db: db}
}

const externalUserLinkSuggestionSelectColumns = `id, org_id, provider, provider_workspace_id, provider_user_id,
	suggested_user_id, reason, confidence, external_email, external_handle,
	external_display_name, last_seen_at, dismissed_at`

func (s *ExternalUserLinkSuggestionStore) ListOpenByOrg(ctx context.Context, orgID uuid.UUID) ([]models.ExternalUserLinkSuggestion, error) {
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		SELECT %s
		FROM external_user_link_suggestions
		WHERE org_id = @org_id
		  AND dismissed_at IS NULL
		ORDER BY last_seen_at DESC`, externalUserLinkSuggestionSelectColumns),
		pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query external user link suggestions: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.ExternalUserLinkSuggestion])
}

func (s *ExternalUserLinkSuggestionStore) GetOpenByID(ctx context.Context, orgID, suggestionID uuid.UUID) (models.ExternalUserLinkSuggestion, error) {
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		SELECT %s
		FROM external_user_link_suggestions
		WHERE org_id = @org_id
		  AND id = @id
		  AND dismissed_at IS NULL`, externalUserLinkSuggestionSelectColumns),
		pgx.NamedArgs{"org_id": orgID, "id": suggestionID})
	if err != nil {
		return models.ExternalUserLinkSuggestion{}, fmt.Errorf("query external user link suggestion: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.ExternalUserLinkSuggestion])
}

func (s *ExternalUserLinkSuggestionStore) UpsertOpen(ctx context.Context, suggestion models.ExternalUserLinkSuggestion) (models.ExternalUserLinkSuggestion, error) {
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		INSERT INTO external_user_link_suggestions (
			org_id, provider, provider_workspace_id, provider_user_id, suggested_user_id,
			reason, confidence, external_email, external_handle, external_display_name
		)
		VALUES (
			@org_id, @provider, @provider_workspace_id, @provider_user_id, @suggested_user_id,
			@reason, @confidence, @external_email, @external_handle, @external_display_name
		)
		ON CONFLICT (org_id, provider, provider_workspace_id, provider_user_id, suggested_user_id)
		WHERE dismissed_at IS NULL
		DO UPDATE SET
			reason = EXCLUDED.reason,
			confidence = GREATEST(external_user_link_suggestions.confidence, EXCLUDED.confidence),
			external_email = COALESCE(EXCLUDED.external_email, external_user_link_suggestions.external_email),
			external_handle = COALESCE(EXCLUDED.external_handle, external_user_link_suggestions.external_handle),
			external_display_name = COALESCE(EXCLUDED.external_display_name, external_user_link_suggestions.external_display_name),
			last_seen_at = now()
		RETURNING %s`, externalUserLinkSuggestionSelectColumns),
		pgx.NamedArgs{
			"org_id":                suggestion.OrgID,
			"provider":              suggestion.Provider,
			"provider_workspace_id": suggestion.ProviderWorkspaceID,
			"provider_user_id":      suggestion.ProviderUserID,
			"suggested_user_id":     suggestion.SuggestedUserID,
			"reason":                suggestion.Reason,
			"confidence":            suggestion.Confidence,
			"external_email":        suggestion.ExternalEmail,
			"external_handle":       suggestion.ExternalHandle,
			"external_display_name": suggestion.ExternalDisplayName,
		})
	if err != nil {
		return models.ExternalUserLinkSuggestion{}, fmt.Errorf("upsert external user link suggestion: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.ExternalUserLinkSuggestion])
}

func (s *ExternalUserLinkSuggestionStore) Dismiss(ctx context.Context, orgID, suggestionID uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE external_user_link_suggestions
		SET dismissed_at = now()
		WHERE org_id = @org_id
		  AND id = @id
		  AND dismissed_at IS NULL`,
		pgx.NamedArgs{"org_id": orgID, "id": suggestionID})
	if err != nil {
		return fmt.Errorf("dismiss external user link suggestion: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
