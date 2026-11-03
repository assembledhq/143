package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type lifecycleEnqueueDisposition string

const (
	lifecycleEnqueueNewJob                lifecycleEnqueueDisposition = "new_job"
	lifecycleEnqueueBoundExistingConsumer lifecycleEnqueueDisposition = "bound_existing_consumer"
	lifecycleEnqueuePendingReconciliation lifecycleEnqueueDisposition = "pending_reconciliation"
)

// Pending reconciliation deliberately carries no candidate/foreign job identity.
// Neither an insert collision nor this result acknowledges or binds any input.
type lifecycleEnqueueResult struct {
	disposition lifecycleEnqueueDisposition
	jobID       uuid.UUID
}

// enqueueLifecycleJobInTx inserts only a NEW immutable input envelope or proves
// the exact already-bound job on replay. It never appends/rebinds inputs, adopts
// a merely equal dedupe key, commits, notifies or retries. Its owner holds the
// canonical domain/job/parent/lane locks and establishes admission eligibility;
// a new-job result must be bound to its inputs in that SAME outer transaction.
// Pending reconciliation must become a due waiting receipt at the admission
// seam, not a successful nil-ID enqueue or a second input-consuming job.
func enqueueLifecycleJobInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, binding LifecycleJobBinding, opts EnqueueOpts, allocation *AttemptFence) (lifecycleEnqueueResult, error) {
	if ctx == nil || nilLifecycleDependency(tx) {
		return lifecycleEnqueueResult{}, errors.New("lifecycle enqueue requires a context and supplied transaction")
	}
	if err := binding.Validate(); err != nil {
		return lifecycleEnqueueResult{}, err
	}
	if orgID == uuid.Nil || orgID != binding.orgID || opts.Queue != "agent" || (opts.JobType != "run_agent" && opts.JobType != "continue_session") {
		return lifecycleEnqueueResult{}, errors.New("lifecycle enqueue requires its exact tenant and a recognized input-consuming agent job")
	}
	// Freeze concrete scheduling/retry options with the command. Database
	// defaults are useful for legacy enqueue, but cannot be wildcards when
	// proving replay of one exact immutable consumer.
	if opts.RunAt == nil || opts.MaxAttempts <= 0 || opts.MaxAttempts > math.MaxInt32 {
		return lifecycleEnqueueResult{}, errors.New("lifecycle enqueue requires explicit run time and positive integer max attempts")
	}
	if len(binding.dispatchIDs) > defaultLifecycleConsumerLimits().structuredRows {
		return lifecycleEnqueueResult{}, ErrLifecycleConsumerDiscoveryIncomplete
	}
	if allocation != nil && (allocation.Validate() != nil || !sameLifecycleJobBinding(binding, allocation.binding)) {
		return lifecycleEnqueueResult{}, errors.New("lifecycle enqueue allocation must name the complete immutable binding")
	}
	base, err := json.Marshal(opts.Payload)
	if err != nil {
		return lifecycleEnqueueResult{}, fmt.Errorf("encode lifecycle enqueue domain payload: %w", err)
	}
	payload, err := encodeLifecycleJobBindingPayload(base, binding)
	if err != nil {
		return lifecycleEnqueueResult{}, err
	}
	if len(payload) > legacyJobRouteMaxBytes {
		return lifecycleEnqueueResult{}, ErrLifecycleConsumerDiscoveryIncomplete
	}
	if err := boundLifecycleJobCanonicalExpansion(payload); err != nil {
		return lifecycleEnqueueResult{}, err
	}
	if err := requireLifecycleScopeTransactionInTx(ctx, tx); err != nil {
		return lifecycleEnqueueResult{}, err
	}
	// Discovery reads PostgreSQL's canonical JSONB text, not compact client
	// bytes. Check that same representation before inserting an unusable job.
	// The client preflight above bounds expansion before the server renders it.
	var canonicalSize int
	if err := tx.QueryRow(ctx, `SELECT octet_length(($1::jsonb)::text)`, payload).Scan(&canonicalSize); err != nil {
		return lifecycleEnqueueResult{}, fmt.Errorf("measure canonical lifecycle job payload: %w", err)
	}
	if canonicalSize > legacyJobRouteMaxBytes {
		return lifecycleEnqueueResult{}, ErrLifecycleConsumerDiscoveryIncomplete
	}
	opts.Payload = json.RawMessage(payload)
	id, err := enqueueOnWithID(ctx, tx, orgID, opts, binding.jobID)
	if err != nil {
		return lifecycleEnqueueResult{}, fmt.Errorf("insert immutable lifecycle job: %w", err)
	}
	if id != uuid.Nil {
		if id != binding.jobID {
			return lifecycleEnqueueResult{}, errors.New("lifecycle enqueue returned an unexpected job identity")
		}
		if err := ctx.Err(); err != nil {
			return lifecycleEnqueueResult{}, err
		}
		return lifecycleEnqueueResult{disposition: lifecycleEnqueueNewJob, jobID: id}, nil
	}
	// READ COMMITTED's fresh statement sees the winner after INSERT's unique
	// conflict wait. This is deliberately tenant+expected-ID scoped: a global
	// queue/key collision in another tenant is not authorization to inspect it.
	var exact bool
	err = tx.QueryRow(ctx, `SELECT queue=$3 AND job_type=$4
	 AND payload::text=($5::jsonb)::text AND priority=$6
	 AND dedupe_key IS NOT DISTINCT FROM $7::text
	 AND target_node_id IS NOT DISTINCT FROM $8::text
	 AND run_at=$9::timestamptz AND max_attempts=$10::integer
	 FROM jobs WHERE org_id=$1 AND id=$2`, orgID, binding.jobID,
		opts.Queue, opts.JobType, payload, opts.Priority, opts.DedupeKey,
		opts.TargetNodeID, opts.RunAt, opts.MaxAttempts).Scan(&exact)
	pending := lifecycleEnqueueResult{disposition: lifecycleEnqueuePendingReconciliation}
	if errors.Is(err, pgx.ErrNoRows) {
		return finishLifecycleEnqueue(ctx, pending)
	}
	if err != nil {
		return lifecycleEnqueueResult{}, fmt.Errorf("inspect exact lifecycle enqueue collision: %w", err)
	}
	if !exact {
		return finishLifecycleEnqueue(ctx, pending)
	}
	_, err = proveOwnLifecycleConsumerInTx(ctx, tx, orgID, binding.fence,
		&lifecycleOwnConsumer{binding: binding, attempt: allocation}, defaultLifecycleConsumerLimits().structuredRows)
	if errors.Is(err, ErrLifecycleConsumerReconciliation) || errors.Is(err, ErrLifecycleConsumerDiscoveryIncomplete) {
		return finishLifecycleEnqueue(ctx, pending)
	}
	if err != nil {
		return lifecycleEnqueueResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return lifecycleEnqueueResult{}, err
	}
	return lifecycleEnqueueResult{disposition: lifecycleEnqueueBoundExistingConsumer, jobID: binding.jobID}, nil
}

func finishLifecycleEnqueue(ctx context.Context, result lifecycleEnqueueResult) (lifecycleEnqueueResult, error) {
	if err := ctx.Err(); err != nil {
		return lifecycleEnqueueResult{}, err
	}
	return result, nil
}

// Bound canonical serialization before PostgreSQL expands numeric exponents.
// This is a conservative resource bound, not normalization: the exact JSONB
// representation and its smaller reader budget are checked in the transaction.
func boundLifecycleJobCanonicalExpansion(payload []byte) error {
	const expansionBudget = 4 * legacyJobRouteMaxBytes
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	total := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("bound lifecycle job JSON expansion: %w", err)
		}
		size := 0
		switch value := token.(type) {
		case json.Delim:
			size = 1
			if value == '}' || value == ']' {
				size += 2 // composite values also need comma plus following space
			}
		case string:
			encoded, err := json.Marshal(value)
			if err != nil {
				return fmt.Errorf("bound lifecycle job string: %w", err)
			}
			size = len(encoded) + 2 // comma or colon plus following space
		case json.Number:
			text := string(value)
			negative := strings.HasPrefix(text, "-")
			text = strings.TrimPrefix(text, "-")
			mantissa, exponent := text, int64(0)
			if i := strings.IndexAny(text, "eE"); i >= 0 {
				mantissa = text[:i]
				exponent, err = strconv.ParseInt(text[i+1:], 10, 64)
				if err != nil || exponent < -expansionBudget || exponent > expansionBudget {
					return ErrLifecycleConsumerDiscoveryIncomplete
				}
			}
			point, digits := len(mantissa), len(mantissa)
			if dot := strings.IndexByte(mantissa, '.'); dot >= 0 {
				point, digits = dot, digits-1
			}
			position := int64(point) + exponent
			width := int64(digits + 1)
			if position <= 0 {
				width = 2 - position + int64(digits)
			} else if position >= int64(digits) {
				width = position
			}
			if negative {
				width++
			}
			if width > expansionBudget {
				return ErrLifecycleConsumerDiscoveryIncomplete
			}
			size = int(width) + 2
		case bool:
			size = 7 // longest boolean plus separator
		case nil:
			size = 6 // null plus separator
		default:
			return errors.New("unsupported lifecycle job JSON token")
		}
		if size > expansionBudget-total {
			return ErrLifecycleConsumerDiscoveryIncomplete
		}
		total += size
	}
}
