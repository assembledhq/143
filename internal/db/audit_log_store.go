package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type AuditLogStore struct {
	db DBTX
}

func NewAuditLogStore(db DBTX) *AuditLogStore {
	return &AuditLogStore{db: db}
}

// Create inserts a new audit log entry. This is the only write operation —
// the table is append-only (enforced by DB trigger).
func (s *AuditLogStore) Create(ctx context.Context, entry *models.AuditLog) error {
	if err := entry.ActorType.Validate(); err != nil {
		return err
	}
	if err := entry.Action.Validate(); err != nil {
		return err
	}
	if err := entry.ResourceType.Validate(); err != nil {
		return err
	}

	query := `
		INSERT INTO audit_logs (
			org_id, actor_type, actor_id, user_id,
			action, resource_type, resource_id,
			details, request_id, ip_address, user_agent,
			session_id, project_id
		) VALUES (
			@org_id, @actor_type, @actor_id, @user_id,
			@action, @resource_type, @resource_id,
			@details, @request_id, @ip_address, @user_agent,
			@session_id, @project_id
		)
		RETURNING id, created_at`

	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":        entry.OrgID,
		"actor_type":    entry.ActorType,
		"actor_id":      entry.ActorID,
		"user_id":       entry.UserID,
		"action":        entry.Action,
		"resource_type": entry.ResourceType,
		"resource_id":   entry.ResourceID,
		"details":       entry.Details,
		"request_id":    entry.RequestID,
		"ip_address":    entry.IPAddress,
		"user_agent":    entry.UserAgent,
		"session_id":    entry.SessionID,
		"project_id":    entry.ProjectID,
	})
	return row.Scan(&entry.ID, &entry.CreatedAt)
}

// CreateBatch inserts multiple audit log entries in a single round-trip via
// a multi-row VALUES INSERT. Use this when emitting more than one audit at
// once (e.g. resolving N review comments inline with a message send) — at
// LAN latency the cost of N separate INSERTs is dominated by per-query RTT.
//
// All entries must belong to orgID; cross-tenant batches are rejected so a
// caller can't accidentally smear audit rows across orgs through a single
// send. For a single entry the call falls through to Create so we don't pay
// the cost of building a multi-row statement for the common case.
func (s *AuditLogStore) CreateBatch(ctx context.Context, orgID uuid.UUID, entries []*models.AuditLog) error {
	if len(entries) == 0 {
		return nil
	}
	if len(entries) == 1 {
		if entries[0].OrgID != orgID {
			return fmt.Errorf("audit batch entry org_id %s does not match expected org_id %s", entries[0].OrgID, orgID)
		}
		return s.Create(ctx, entries[0])
	}
	for i, e := range entries {
		if e.OrgID != orgID {
			return fmt.Errorf("audit batch entry %d org_id %s does not match expected org_id %s", i, e.OrgID, orgID)
		}
		if err := e.ActorType.Validate(); err != nil {
			return err
		}
		if err := e.Action.Validate(); err != nil {
			return err
		}
		if err := e.ResourceType.Validate(); err != nil {
			return err
		}
	}

	// 13 placeholders per row, in the column order below.
	const cols = 13
	placeholders := make([]string, len(entries))
	args := make([]any, 0, len(entries)*cols)
	for i, e := range entries {
		ph := make([]string, cols)
		for j := 0; j < cols; j++ {
			ph[j] = fmt.Sprintf("$%d", i*cols+j+1)
		}
		placeholders[i] = "(" + strings.Join(ph, ", ") + ")"
		args = append(args,
			e.OrgID, e.ActorType, e.ActorID, e.UserID,
			e.Action, e.ResourceType, e.ResourceID,
			e.Details, e.RequestID, e.IPAddress, e.UserAgent,
			e.SessionID, e.ProjectID,
		)
	}

	query := `INSERT INTO audit_logs (
			org_id, actor_type, actor_id, user_id,
			action, resource_type, resource_id,
			details, request_id, ip_address, user_agent,
			session_id, project_id
		) VALUES ` + strings.Join(placeholders, ", ")

	if _, err := s.db.Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("insert audit logs batch: %w", err)
	}
	return nil
}

// AuditLogFilters controls listing/search behavior.
type AuditLogFilters struct {
	ActorType    models.AuditActorType
	UserID       *uuid.UUID
	Action       models.AuditAction
	ActionPrefix string
	ResourceType models.AuditResourceType
	ResourceID   string
	SessionID    *uuid.UUID
	ProjectID    *uuid.UUID
	Since        *time.Time
	Until        *time.Time
	Limit        int
	CursorTime   *time.Time
	CursorID     *int64
}

// List returns audit log entries for an organization, filtered and paginated.
func (s *AuditLogStore) List(ctx context.Context, orgID uuid.UUID, filters AuditLogFilters) ([]models.AuditLog, error) {
	w := newWhereClause()
	w.add("org_id = @org_id", "org_id", orgID)

	if filters.ActorType != "" {
		w.add("actor_type = @actor_type", "actor_type", filters.ActorType)
	}
	if filters.UserID != nil {
		w.add("user_id = @user_id", "user_id", *filters.UserID)
	}
	if filters.Action != "" {
		w.add("action = @action", "action", filters.Action)
	}
	if filters.ActionPrefix != "" {
		w.add("action LIKE @action_prefix ESCAPE '\\'", "action_prefix", escapeLike(filters.ActionPrefix)+"%")
	}
	if filters.ResourceType != "" {
		w.add("resource_type = @resource_type", "resource_type", filters.ResourceType)
	}
	if filters.ResourceID != "" {
		w.add("resource_id = @resource_id", "resource_id", filters.ResourceID)
	}
	if filters.SessionID != nil {
		w.add("session_id = @session_id", "session_id", *filters.SessionID)
	}
	if filters.ProjectID != nil {
		w.add("project_id = @project_id", "project_id", *filters.ProjectID)
	}
	if filters.Since != nil {
		w.add("created_at >= @since", "since", *filters.Since)
	}
	if filters.Until != nil {
		w.add("created_at <= @until", "until", *filters.Until)
	}
	if filters.CursorTime != nil && filters.CursorID != nil {
		w.add("(created_at, id) < (@cursor_time, @cursor_id)", "cursor_time", *filters.CursorTime)
		w.addArg("cursor_id", *filters.CursorID)
	}

	// Routine preview secret-bundle resolution events are emitted by the system
	// on every hourly preview recycle. They are pure housekeeping noise in the
	// org activity log, so hide them from the default listing while keeping the
	// matching ".failed" events (and every other action) visible. An explicit
	// action filter still surfaces them when a caller asks for them directly.
	if filters.Action != models.AuditActionPreviewSecretBundleResolved {
		w.add("NOT (resource_type = @hide_resolved_resource AND action = @hide_resolved_action)",
			"hide_resolved_resource", models.AuditResourcePreviewSecretBundle)
		w.addArg("hide_resolved_action", models.AuditActionPreviewSecretBundleResolved)
	}

	where, args := w.build()

	query := `
		SELECT id, org_id, actor_type, actor_id, user_id,
		       action, resource_type, resource_id,
		       details, request_id, ip_address, user_agent,
		       session_id, project_id, created_at
		FROM audit_logs` + where + ` ORDER BY created_at DESC, id DESC`

	limit := filters.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query += fmt.Sprintf(` LIMIT %d`, limit)

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query audit logs: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.AuditLog])
}

// GetByID returns a single audit log entry by ID, scoped to an organization.
func (s *AuditLogStore) GetByID(ctx context.Context, orgID uuid.UUID, id int64) (*models.AuditLog, error) {
	query := `
		SELECT id, org_id, actor_type, actor_id, user_id,
		       action, resource_type, resource_id,
		       details, request_id, ip_address, user_agent,
		       session_id, project_id, created_at
		FROM audit_logs
		WHERE org_id = @org_id AND id = @id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "id": id})
	if err != nil {
		return nil, fmt.Errorf("query audit log by id: %w", err)
	}
	entry, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.AuditLog])
	if err != nil {
		return nil, fmt.Errorf("get audit log %d: %w", id, err)
	}
	return &entry, nil
}

// DeleteExpired calls the SECURITY DEFINER function to delete audit logs older
// than the specified retention period. Returns the number of rows deleted.
func (s *AuditLogStore) DeleteExpired(ctx context.Context, orgID uuid.UUID, retentionDays int) (int64, error) {
	var deleted int64
	err := s.db.QueryRow(ctx, `SELECT delete_expired_audit_logs($1, $2)`, orgID, retentionDays).Scan(&deleted)
	if err != nil {
		return 0, fmt.Errorf("delete expired audit logs: %w", err)
	}
	return deleted, nil
}
