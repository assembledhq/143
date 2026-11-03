package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrReviewExportLimit = errors.New("review export acquisition limit exceeded")
var ErrReviewExportBusy = fmt.Errorf("%w: busy lock", ErrReviewExportLimit)
var ErrReviewExportSlots = fmt.Errorf("%w: active slot limit", ErrReviewExportLimit)
var ErrReviewExportRows = fmt.Errorf("%w: row limit", ErrReviewExportLimit)
var ErrReviewExportBytes = fmt.Errorf("%w: payload byte limit", ErrReviewExportLimit)
var ErrReviewExportReleased = errors.New("review export released")
var ErrReviewExportArchive = errors.New("archive attestation does not match acquisition")
var ErrReviewExportReleaseConflict = errors.New("acquisition already released with a different archive attestation")
var ErrReviewExportPosition = errors.New("invalid export cursor position")
var ErrReviewExportExpired = errors.New("review export unavailable or expired")

const reviewExportMaxRecords = 20000
const reviewExportMaxBytes = 64 * 1024 * 1024
const reviewExportTTL = 24 * time.Hour

// Provider review objects can be edited in place for distinct publications.
// Only matching publication keys without conflicting provider IDs establish
// overlap; explicit capture links may also represent otherwise unkeyed metadata.
const reviewExportOverlap = `m.org_id=a.org_id AND m.repository_id=a.repository_id AND m.pull_request_id=a.pull_request_id AND (
 (NULLIF(btrim(m.review_output_key),'') IS NOT NULL AND m.review_output_key=a.publication_key
     AND (COALESCE(m.github_review_id,0)<=0 OR COALESCE(a.github_review_id,0)<=0 OR m.github_review_id=a.github_review_id))
 OR (m.assessment_id=a.id AND COALESCE(m.github_review_id,0)<=0 AND COALESCE(btrim(m.review_output_key),'')='')
)`

// Every source payload and join is read in the same REPEATABLE READ transaction.
// No live findings, current policy lookup, latest-head projection, or PR-status
// filter participates. Alias payloads retain both representations of an overlap.
const reviewExportSources = `WITH review_event_groups AS MATERIALIZED (
 SELECT delivery.payload#>>'{pull_request,number}' AS pr_number,delivery.payload#>>'{review,id}' AS review_id,
 jsonb_agg(jsonb_build_object('delivery_id',delivery.id,'received_at',delivery.received_at,
   'signature_valid',delivery.signature_valid,'review_id',delivery.payload#>'{review,id}',
   'commit_sha',delivery.payload#>'{review,commit_id}','body',delivery.payload#>'{review,body}',
   'submitted_at',delivery.payload#>'{review,submitted_at}') ORDER BY delivery.received_at,delivery.id) AS observations
 FROM webhook_deliveries delivery JOIN repositories repo ON repo.org_id=delivery.org_id
  AND repo.id=@repository_id AND repo.github_id>0 AND delivery.payload#>>'{repository,id}'=repo.github_id::text
 WHERE delivery.org_id=@org_id AND delivery.provider='github' AND delivery.event_type='pull_request_review'
  AND delivery.payload->>'action'='submitted' AND delivery.signature_valid IS DISTINCT FROM false
 GROUP BY delivery.payload#>>'{pull_request,number}',delivery.payload#>>'{review,id}'
), eligible_assessments AS (
 SELECT * FROM code_review_revision_assessments WHERE org_id=@org_id AND repository_id=@repository_id AND created_at <= @boundary AND (
 (@time_basis='created' AND created_at>=@from AND created_at<@until)
 OR (@time_basis='completed' AND completed_at>=@from AND completed_at<@until)
 OR (@time_basis='created_or_completed' AND ((created_at>=@from AND created_at<@until) OR (completed_at>=@from AND completed_at<@until)))
 OR (@time_basis='published' AND created_at<@until))
), sources AS (
 SELECT 'assessment'::text AS kind,a.id,a.created_at,a.completed_at,to_jsonb(a)-'org_id' AS source,
 COALESCE((SELECT jsonb_agg(to_jsonb(m)-'org_id' ORDER BY m.created_at,m.id)
 FROM code_review_session_metadata m WHERE m.created_at<=@boundary AND ` + reviewExportOverlap + `),'[]'::jsonb) AS legacy_aliases
 FROM eligible_assessments a
 UNION ALL
 SELECT 'legacy',m.id,m.created_at,m.completed_at,to_jsonb(m)-'org_id','[]'::jsonb
 FROM code_review_session_metadata m WHERE m.org_id=@org_id AND m.repository_id=@repository_id

), candidates AS MATERIALIZED (
SELECT jsonb_build_object('kind',s.kind,'id',s.id,'source',s.source,'legacy_aliases',s.legacy_aliases,
 'policy',(SELECT to_jsonb(pol)-'org_id'-'active'-'created_by_user_id' FROM code_review_policies pol
   WHERE pol.org_id=@org_id AND pol.id=(s.source->>'policy_id')::uuid
     AND (pol.repository_id IS NULL OR pol.repository_id=@repository_id)),
 'provider_observations',COALESCE(events.observations,'[]'::jsonb) ||
 CASE WHEN formal_events.review_id IS DISTINCT FROM events.review_id THEN COALESCE(formal_events.observations,'[]'::jsonb) ELSE '[]'::jsonb END,
 'pull_request',CASE WHEN pr.id IS NULL THEN NULL ELSE jsonb_build_object('id',pr.id,'number',pr.github_pr_number,'repository',pr.github_repo,
     'status',pr.status,'merged_at',pr.merged_at,'github_state_synced_at',pr.github_state_synced_at) END,
 'agent_results',COALESCE((SELECT jsonb_agg(to_jsonb(ar)-'org_id'-'raw_output' ORDER BY ar.created_at,ar.id)
   FROM code_review_agent_results ar WHERE ar.org_id=@org_id
     AND ar.session_id=(s.source->>'session_id')::uuid
     AND ((s.kind='assessment' AND ar.assessment_id=s.id) OR (s.kind='legacy' AND ar.assessment_id IS NULL))),'[]'::jsonb)) AS payload,
 s.created_at,s.id,s.kind
FROM sources s
LEFT JOIN pull_requests pr ON pr.org_id=@org_id AND pr.id=(s.source->>'pull_request_id')::uuid
LEFT JOIN review_event_groups events ON events.pr_number=pr.github_pr_number::text AND events.review_id=s.source->>'github_review_id'
LEFT JOIN review_event_groups formal_events ON formal_events.pr_number=pr.github_pr_number::text
 AND formal_events.review_id=COALESCE(s.source#>>'{publication_receipt,formal_approval_id}',s.source#>>'{publication_receipt,FormalApprovalID}')
WHERE s.created_at <= @boundary AND (
 (@time_basis='created' AND s.created_at>=@from AND s.created_at<@until)
 OR (@time_basis='completed' AND s.completed_at>=@from AND s.completed_at<@until)
 OR (@time_basis='created_or_completed' AND ((s.created_at>=@from AND s.created_at<@until) OR (s.completed_at>=@from AND s.completed_at<@until)))
 -- Publication filtering happens after conservative provider-event binding.
 -- Unknown timing remains included; completion is never a surrogate.
 OR (@time_basis='published' AND s.created_at<@until)
)
ORDER BY s.created_at,s.id,s.kind LIMIT @max_records
)
-- Refuse an oversized individual payload before the PostgreSQL protocol sends
-- it. NULL is an internal limit sentinel, never an exported or skipped record.
SELECT CASE WHEN octet_length(payload::text)>@max_payload_bytes THEN NULL::jsonb ELSE payload END
FROM candidates ORDER BY created_at,id,kind`

type CodeReviewExportStore struct{ db TxStarter }

func NewCodeReviewExportStore(database TxStarter) *CodeReviewExportStore {
	return &CodeReviewExportStore{db: database}
}

func (s *CodeReviewExportStore) Acquire(ctx context.Context, orgID uuid.UUID, f models.CodeReviewExportFilters) (models.CodeReviewExportAcquisition, error) {
	a, err := s.acquire(ctx, orgID, f)
	// Release does not hold the acquisition advisory lock. Its header update
	// can invalidate a REPEATABLE READ snapshot or a just-reused acquisition.
	// Report these races as retryable contention, never a gone first request.
	var pgErr *pgconn.PgError
	if errors.Is(err, ErrReviewExportReleased) || errors.Is(err, ErrReviewExportExpired) ||
		(errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01")) {
		return models.CodeReviewExportAcquisition{}, ErrReviewExportBusy
	}
	return a, err
}

func (s *CodeReviewExportStore) acquire(ctx context.Context, orgID uuid.UUID, f models.CodeReviewExportFilters) (models.CodeReviewExportAcquisition, error) {
	if orgID == uuid.Nil {
		return models.CodeReviewExportAcquisition{}, pgx.ErrNoRows
	}
	if err := f.Validate(); err != nil {
		return models.CodeReviewExportAcquisition{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return models.CodeReviewExportAcquisition{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`); err != nil {
		return models.CodeReviewExportAcquisition{}, err
	}
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout = '30s'`); err != nil {
		return models.CodeReviewExportAcquisition{}, err
	}
	var authorized, locked bool
	var boundary time.Time
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM repositories WHERE org_id=$1 AND id=$2),
 pg_try_advisory_xact_lock(hashtextextended($1::text,731)),transaction_timestamp()`, orgID, f.RepositoryID).Scan(&authorized, &locked, &boundary)
	if err != nil {
		return models.CodeReviewExportAcquisition{}, err
	}
	if !authorized {
		return models.CodeReviewExportAcquisition{}, pgx.ErrNoRows
	}
	if !locked {
		return models.CodeReviewExportAcquisition{}, ErrReviewExportBusy
	}
	if _, err = tx.Exec(ctx, `WITH expired AS (
 UPDATE code_review_exports SET slot=NULL WHERE org_id=$1 AND expires_at<=now() AND slot IS NOT NULL RETURNING id
) DELETE FROM code_review_export_rows r USING expired e WHERE r.org_id=$1 AND r.export_id=e.id`, orgID); err != nil {
		return models.CodeReviewExportAcquisition{}, err
	}
	filters, err := json.Marshal(f)
	if err != nil {
		return models.CodeReviewExportAcquisition{}, err
	}
	// Retry a lost first-page response without consuming another organization slot.
	var existingID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM code_review_exports
 WHERE org_id=$1 AND repository_id=$2 AND schema_version=$3 AND filters=$4 AND released_at IS NULL AND expires_at>now()
 ORDER BY acquired_at DESC,id LIMIT 1`, orgID, f.RepositoryID, models.CodeReviewExportSchemaVersion, filters).Scan(&existingID)
	if err == nil {
		if err = tx.Commit(ctx); err != nil {
			return models.CodeReviewExportAcquisition{}, err
		}
		return s.acquisition(ctx, orgID, existingID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return models.CodeReviewExportAcquisition{}, err
	}
	var slot int
	if err = tx.QueryRow(ctx, `SELECT COALESCE(min(n),0) FROM generate_series(1,3) n
 WHERE NOT EXISTS(SELECT 1 FROM code_review_exports WHERE org_id=$1 AND slot=n)`, orgID).Scan(&slot); err != nil {
		return models.CodeReviewExportAcquisition{}, err
	}
	if slot == 0 {
		return models.CodeReviewExportAcquisition{}, ErrReviewExportSlots
	}

	rows, err := tx.Query(ctx, reviewExportSources, pgx.NamedArgs{"org_id": orgID, "repository_id": f.RepositoryID, "boundary": boundary, "from": f.From, "until": f.Until, "time_basis": string(f.TimeBasis), "max_records": reviewExportMaxRecords + 1, "max_payload_bytes": reviewExportMaxBytes})
	if err != nil {
		return models.CodeReviewExportAcquisition{}, err
	}
	// Bound bytes while scanning rather than collecting arbitrarily large payloads.
	payloads := make([]json.RawMessage, 0)
	bytes, scanned := 0, 0
	representedLegacy := make(map[uuid.UUID]bool)
	var identities []struct {
		kind models.ReviewExportSourceKind
		id   uuid.UUID
	}
	for rows.Next() {
		var raw json.RawMessage
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return models.CodeReviewExportAcquisition{}, err
		}
		if raw == nil {
			rows.Close()
			return models.CodeReviewExportAcquisition{}, ErrReviewExportBytes
		}
		scanned++
		bytes += len(raw)
		if scanned > reviewExportMaxRecords {
			rows.Close()
			return models.CodeReviewExportAcquisition{}, ErrReviewExportRows
		}
		if bytes > reviewExportMaxBytes {
			rows.Close()
			return models.CodeReviewExportAcquisition{}, ErrReviewExportBytes
		}
		var source models.CodeReviewExportSource
		if err = json.Unmarshal(raw, &source); err != nil {
			rows.Close()
			return models.CodeReviewExportAcquisition{}, err
		}
		source = models.ResolveCodeReviewExportPublicationTime(source, boundary)
		if f.TimeBasis == models.ReviewExportPublished && source.PublicationTime != nil && (source.PublicationTime.Before(f.From) || !source.PublicationTime.Before(f.Until)) {
			continue
		}
		frozen, encodeErr := json.Marshal(source)
		if encodeErr != nil {
			rows.Close()
			return models.CodeReviewExportAcquisition{}, encodeErr
		}
		bytes += max(0, len(frozen)-len(raw))
		if bytes > reviewExportMaxBytes {
			rows.Close()
			return models.CodeReviewExportAcquisition{}, ErrReviewExportBytes
		}
		if source.Kind == models.ReviewExportAssessment {
			var aliases []struct {
				ID uuid.UUID `json:"id"`
			}
			if err = json.Unmarshal(source.LegacyAliases, &aliases); err != nil {
				rows.Close()
				return models.CodeReviewExportAcquisition{}, err
			}
			for _, alias := range aliases {
				representedLegacy[alias.ID] = true
			}
		}
		identities = append(identities, struct {
			kind models.ReviewExportSourceKind
			id   uuid.UUID
		}{source.Kind, source.ID})
		payloads = append(payloads, frozen)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return models.CodeReviewExportAcquisition{}, err
	}
	// Resolve representation overlap only after publication-time admission. An
	// excluded assessment must never hide a legacy source with unresolved timing.
	selected := make([]json.RawMessage, 0, len(payloads))
	for i, payload := range payloads {
		if identities[i].kind == models.ReviewExportLegacy && representedLegacy[identities[i].id] {
			continue
		}
		selected = append(selected, payload)
	}
	encoded, err := json.Marshal(selected)
	if err != nil {
		return models.CodeReviewExportAcquisition{}, err
	}
	id := uuid.New()
	_, err = tx.Exec(ctx, `INSERT INTO code_review_exports(id,org_id,repository_id,schema_version,filters,acquired_at,expires_at,counts,slot)
 VALUES($1,$2,$3,$4,$5,$6,$7,'{}',$8)`, id, orgID, f.RepositoryID, models.CodeReviewExportSchemaVersion, filters, boundary, boundary.Add(reviewExportTTL), slot)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return models.CodeReviewExportAcquisition{}, ErrReviewExportSlots
		}
		return models.CodeReviewExportAcquisition{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO code_review_export_rows(org_id,export_id,position,payload)
 SELECT $1,$2,ordinality,value FROM jsonb_array_elements($3::jsonb) WITH ORDINALITY`, orgID, id, encoded)
	if err != nil {
		return models.CodeReviewExportAcquisition{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE code_review_exports e SET counts=(
 SELECT jsonb_build_object('records',count(*),'assessments',count(*) FILTER (WHERE payload->>'kind'='assessment'),
 'legacy',count(*) FILTER (WHERE payload->>'kind'='legacy'),
 'represented_legacy',(SELECT count(DISTINCT alias->>'id') FROM code_review_export_rows aliases
 CROSS JOIN LATERAL jsonb_array_elements(aliases.payload->'legacy_aliases') alias WHERE aliases.org_id=$1 AND aliases.export_id=$2),
 'missing_head_sha',count(*) FILTER (WHERE COALESCE(payload#>>'{source,head_sha}','')=''),
 'missing_base_sha',count(*) FILTER (WHERE COALESCE(payload#>>'{source,base_sha}','')=''),
 'missing_base_ref',count(*) FILTER (WHERE COALESCE(payload#>>'{source,base_ref}','')=''),
 'missing_policy_snapshot',count(*) FILTER (WHERE payload->'policy'='null'::jsonb),
 'unresolved_publication_time',count(*) FILTER (WHERE payload->'publication_time'='null'::jsonb),
 'incomplete_substantive_evidence',count(*) FILTER (WHERE NOT COALESCE(payload->>'kind'='assessment'
   AND payload#>>'{source,review_scope}'='full' AND payload#>>'{source,coverage_complete}'='true',false)))
 FROM code_review_export_rows r WHERE r.org_id=$1 AND r.export_id=$2)
 WHERE e.org_id=$1 AND e.id=$2`, orgID, id)
	if err != nil {
		return models.CodeReviewExportAcquisition{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return models.CodeReviewExportAcquisition{}, err
	}
	return s.acquisition(ctx, orgID, id)
}

func (s *CodeReviewExportStore) acquisition(ctx context.Context, orgID, id uuid.UUID) (models.CodeReviewExportAcquisition, error) {
	status, err := exportStatus(ctx, s.db, orgID, uuid.Nil, id, "")
	if err != nil {
		return status.Acquisition, err
	}
	return status.Acquisition, exportReadable(status)
}

func (s *CodeReviewExportStore) Page(ctx context.Context, orgID uuid.UUID, c models.CodeReviewExportCursor, limit int) (models.CodeReviewExportAcquisition, []models.CodeReviewExportSource, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return models.CodeReviewExportAcquisition{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout = '30s'`); err != nil {
		return models.CodeReviewExportAcquisition{}, nil, err
	}
	// Hold a shared header lock through the payload read. Release and lazy expiry
	// cleanup cannot turn a page into an apparently successful short/empty page.
	status, err := exportStatus(ctx, tx, orgID, uuid.Nil, c.AcquisitionID, " FOR SHARE OF e")
	a := status.Acquisition
	if err != nil {
		return a, nil, err
	}
	if err = exportReadable(status); err != nil {
		return a, nil, err
	}
	if limit < 1 || limit > 100 || c.Position < 0 || c.Position > a.Counts.Records {
		return a, nil, ErrReviewExportPosition
	}
	rows, err := tx.Query(ctx, `SELECT r.payload FROM code_review_export_rows r
 WHERE r.org_id=$1 AND r.export_id=$2 AND r.position>$3 ORDER BY r.position LIMIT $4`, orgID, c.AcquisitionID, c.Position, limit)
	if err != nil {
		return a, nil, err
	}
	raw, err := pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
	if err != nil {
		return a, nil, err
	}
	out := make([]models.CodeReviewExportSource, 0, len(raw))
	for _, payload := range raw {
		var source models.CodeReviewExportSource
		if err = json.Unmarshal(payload, &source); err != nil {
			return a, nil, err
		}
		out = append(out, source)
	}
	if int64(len(out)) != min(int64(limit), a.Counts.Records-c.Position) {
		return a, nil, ErrReviewExportExpired
	}
	if err = tx.Commit(ctx); err != nil {
		return a, nil, err
	}
	return a, out, nil
}
