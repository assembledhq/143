package db

import (
	"bytes"
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type messageAdmissionRepairLocator struct{ id, prID uuid.UUID }
type messageAdmissionOwners struct {
	reviewPR, automationGeneration, automationTarget *uuid.UUID
	claimDispatch, claimToken, claimJob              *uuid.UUID
	repairs                                          []messageAdmissionRepairLocator
	extraDispatchIDs, extraJobIDs                    []uuid.UUID
}

const messageAdmissionOwnerLookupQuery = `SELECT s.code_review_owner_pr_id,s.automation_owner_generation_id,
 (SELECT g.target_id FROM automation_target_sessions g WHERE g.org_id=s.org_id AND g.id=s.automation_owner_generation_id),
 t.admission_claim_dispatch_id,t.admission_claim_token,
 (SELECT d.job_id FROM session_input_dispatches d WHERE d.org_id=t.org_id AND d.session_id=t.session_id AND d.thread_id=t.id AND d.id=t.admission_claim_dispatch_id)
 FROM sessions s JOIN session_threads t ON t.org_id=s.org_id AND t.session_id=s.id
 WHERE s.org_id=$1 AND s.id=$2 AND t.id=$3`

const messageAdmissionRepairLookupQuery = `SELECT id,pull_request_id FROM pull_request_repair_runs
 WHERE (CASE WHEN active THEN org_id END)=$1
 AND (CASE WHEN active THEN session_id END)=$2 ORDER BY id LIMIT $3`

// CaptureOrdinaryMessageFenceInTx captures only a NEW command's explicit fence,
// after exact replay miss. Known higher-ranked domain/receipt/job rows precede
// parent/lane locks even in this observation unit. No current owner, status or
// capacity grants admission here. A changed locator fails closed, and the
// caller retains the fence only after known completion of its entry transaction,
// before any mutating retry loop. It must never recapture after stale admission.
func CaptureOrdinaryMessageFenceInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID, laneID uuid.UUID) (AdmissionFence, error) {
	if err := validateThreadSendScope(ctx, tx, orgID, sessionID, laneID); err != nil {
		return AdmissionFence{}, err
	}
	if err := RequireActiveThreadSendModeInTx(ctx, tx, orgID, sessionID); err != nil {
		return AdmissionFence{}, err
	}
	before, err := discoverMessageAdmissionOwners(ctx, tx, orgID, sessionID, laneID)
	if err != nil {
		return AdmissionFence{}, err
	}
	if err := prelockMessageAdmissionOwners(ctx, tx, orgID, before); err != nil {
		return AdmissionFence{}, err
	}
	fence, err := CaptureThreadMessageFenceInTx(ctx, tx, orgID, sessionID, laneID)
	if err != nil {
		return AdmissionFence{}, err
	}
	after, err := discoverMessageAdmissionOwners(ctx, tx, orgID, sessionID, laneID)
	if err != nil {
		return AdmissionFence{}, err
	}
	if !sameMessageAdmissionOwners(before, after) {
		return AdmissionFence{}, ErrMessageAdmissionConflict
	}
	return fence, nil
}

// Discovery is deliberately raw: an inner join must not turn a dangling owner
// marker into "unowned". The parent-locked caller compares the complete locator
// set before using any absence as permission.
func discoverMessageAdmissionOwners(ctx context.Context, tx pgx.Tx, orgID, sessionID, laneID uuid.UUID) (messageAdmissionOwners, error) {
	var owners messageAdmissionOwners
	err := tx.QueryRow(ctx, messageAdmissionOwnerLookupQuery, orgID, sessionID, laneID).
		Scan(&owners.reviewPR, &owners.automationGeneration, &owners.automationTarget, &owners.claimDispatch, &owners.claimToken, &owners.claimJob)
	if err != nil {
		return messageAdmissionOwners{}, fmt.Errorf("discover ordinary admission owner locators: %w", err)
	}
	rows, err := tx.Query(ctx, messageAdmissionRepairLookupQuery, orgID, sessionID, defaultLifecycleConsumerLimits().structuredRows+1)
	if err != nil {
		return messageAdmissionOwners{}, fmt.Errorf("discover ordinary admission repair reservations: %w", err)
	}
	owners.repairs, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (messageAdmissionRepairLocator, error) {
		var v messageAdmissionRepairLocator
		err := row.Scan(&v.id, &v.prID)
		return v, err
	})
	if err != nil {
		return messageAdmissionOwners{}, fmt.Errorf("read ordinary admission repair reservations: %w", err)
	}
	if len(owners.repairs) > defaultLifecycleConsumerLimits().structuredRows {
		return messageAdmissionOwners{}, ErrLifecycleConsumerDiscoveryIncomplete
	}
	return owners, nil
}

func sameMessageAdmissionOwners(a, b messageAdmissionOwners) bool {
	return sameMessageAdmissionUUID(a.reviewPR, b.reviewPR) && sameMessageAdmissionUUID(a.automationGeneration, b.automationGeneration) &&
		sameMessageAdmissionUUID(a.automationTarget, b.automationTarget) && sameMessageAdmissionUUID(a.claimDispatch, b.claimDispatch) &&
		sameMessageAdmissionUUID(a.claimToken, b.claimToken) && sameMessageAdmissionUUID(a.claimJob, b.claimJob) && slices.Equal(a.repairs, b.repairs)
}

func sameMessageAdmissionUUID(a, b *uuid.UUID) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func prelockMessageAdmissionOwners(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, owners messageAdmissionOwners) error {
	keys := []string{}
	if owners.reviewPR != nil {
		keys = append(keys, "code_review_pr:"+orgID.String()+":"+owners.reviewPR.String())
	}
	for _, repair := range owners.repairs {
		keys = append(keys, "code_review_pr:"+orgID.String()+":"+repair.prID.String())
	}
	slices.Sort(keys)
	keys = slices.Compact(keys)
	if len(keys) > 0 {
		var acquired bool
		// The ordered bounded set contains only discovered domain IDs. A real
		// failed try-lock is reported without inventing a retryable SQLSTATE or
		// pretending that the cutover-specific retry cause occurred.
		if err := tx.QueryRow(ctx, `SELECT bool_and(pg_try_advisory_xact_lock(hashtextextended(key,0))) FROM (SELECT unnest($1::text[]) AS key ORDER BY key) ordered`, keys).Scan(&acquired); err != nil {
			return fmt.Errorf("prelock ordinary admission domain keys: %w", err)
		}
		if !acquired {
			return ErrLifecycleContention
		}
	}
	if owners.automationTarget != nil {
		if err := lockMessageAdmissionIDs(ctx, tx, `SELECT id FROM automation_targets WHERE org_id=$1 AND id=ANY($2::uuid[]) ORDER BY id FOR NO KEY UPDATE NOWAIT`, orgID, []uuid.UUID{*owners.automationTarget}); err != nil {
			return err
		}
	}
	if owners.automationGeneration != nil {
		if err := lockMessageAdmissionIDs(ctx, tx, `SELECT id FROM automation_target_sessions WHERE org_id=$1 AND id=ANY($2::uuid[]) ORDER BY id FOR NO KEY UPDATE NOWAIT`, orgID, []uuid.UUID{*owners.automationGeneration}); err != nil {
			return err
		}
	}
	if len(owners.repairs) > 0 {
		ids := make([]uuid.UUID, len(owners.repairs))
		for i, r := range owners.repairs {
			ids[i] = r.id
		}
		if err := lockMessageAdmissionIDs(ctx, tx, `SELECT id FROM pull_request_repair_runs WHERE org_id=$1 AND id=ANY($2::uuid[]) ORDER BY id FOR NO KEY UPDATE NOWAIT`, orgID, ids); err != nil {
			return err
		}
	}
	// A known retained input/job is acquired before the parent, even when
	// ordinary admission will merely wait rather than mutate or adopt it.
	dispatchIDs := slices.Clone(owners.extraDispatchIDs)
	if owners.claimDispatch != nil {
		dispatchIDs = append(dispatchIDs, *owners.claimDispatch)
	}
	if len(dispatchIDs) > 0 {
		if err := lockMessageAdmissionIDs(ctx, tx, `SELECT id FROM session_input_dispatches WHERE org_id=$1 AND id=ANY($2::uuid[]) ORDER BY id FOR NO KEY UPDATE NOWAIT`, orgID, dispatchIDs); err != nil {
			return err
		}
	}
	jobIDs := slices.Clone(owners.extraJobIDs)
	if owners.claimJob != nil {
		jobIDs = append(jobIDs, *owners.claimJob)
	}
	if len(jobIDs) > 0 {
		if err := lockMessageAdmissionIDs(ctx, tx, `SELECT id FROM jobs WHERE org_id=$1 AND id=ANY($2::uuid[]) ORDER BY id FOR NO KEY UPDATE NOWAIT`, orgID, jobIDs); err != nil {
			return err
		}
	}
	return nil
}

func lockMessageAdmissionIDs(ctx context.Context, tx pgx.Tx, query string, orgID uuid.UUID, ids []uuid.UUID) error {
	ordered := slices.Clone(ids)
	slices.SortFunc(ordered, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	ordered = slices.Compact(ordered)
	rows, err := tx.Query(ctx, query, orgID, ordered)
	if err != nil {
		return fmt.Errorf("prelock known ordinary admission rows: %w", err)
	}
	_, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return fmt.Errorf("read known ordinary admission rows: %w", err)
	}
	// Missing retained owner links are NOT interpreted as ordinary authority;
	// the raw parent marker/pair still forces refusal or durable waiting.
	return nil
}
