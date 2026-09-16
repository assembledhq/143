package codereview

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type scheduleSnapshotter interface {
	GetCodeReviewPullRequestSnapshot(context.Context, uuid.UUID, uuid.UUID, int) (ghservice.CodeReviewPullRequestSnapshot, error)
}

type schedulingDependencies struct {
	store     *db.CodeReviewScheduleStore
	snapshots scheduleSnapshotter
	now       func() time.Time
}

func (s *Service) SetScheduling(store *db.CodeReviewScheduleStore, snapshots scheduleSnapshotter) {
	s.scheduling = &schedulingDependencies{store: store, snapshots: snapshots, now: func() time.Time { return time.Now().UTC() }}
}
func (s *Service) SetSchedulingStreams(streams *cache.CodeReviewStreams) {
	if s.scheduling != nil {
		s.scheduling.store.SetStreams(streams, s.logger)
	}
}

func (s *Service) SchedulingEnabled() bool {
	return s.scheduling != nil && s.scheduling.snapshots != nil
}

type scheduledReviewIntent struct {
	Input ReviewChangedInput           `json:"input"`
	Mode  models.CodeReviewRequestMode `json:"mode"`
	Force bool                         `json:"force"`
}

type ScheduleRequestInput struct {
	OrgID         uuid.UUID
	PullRequestID uuid.UUID
	RequestID     uuid.UUID
	RequesterID   *uuid.UUID
	Mode          models.CodeReviewRequestMode
}

type ScheduleRequestResult struct {
	RequestID   uuid.UUID                           `json:"request_id"`
	SessionID   *uuid.UUID                          `json:"session_id"`
	Disposition models.CodeReviewRequestDisposition `json:"disposition"`
	Schedule    models.CodeReviewPRState            `json:"schedule"`
}

var ErrReviewIneligible = errors.New("pull request is not eligible for review")
var errScheduleSnapshotUnavailable = errors.New("review scheduling snapshot unavailable")

func (s *Service) GetSchedule(ctx context.Context, orgID, prID uuid.UUID) (models.CodeReviewPRState, error) {
	if !s.SchedulingEnabled() {
		return models.CodeReviewPRState{}, fmt.Errorf("review scheduling unavailable")
	}
	state, err := s.scheduling.store.Get(ctx, orgID, prID)
	if err != nil || state.PendingInput != nil || state.State != models.CodeReviewScheduleRunning || state.ActiveSessionID == nil {
		return state, err
	}
	metadata, err := s.metadata.GetBySessionID(ctx, orgID, *state.ActiveSessionID)
	if err != nil {
		return state, err
	}
	switch metadata.Status {
	case models.CodeReviewSessionStatusCompleted:
		state.State = models.CodeReviewScheduleIdle
		if metadata.HeadSHA == state.HeadSHA && metadata.BaseSHA == state.BaseSHA {
			state.State = models.CodeReviewScheduleCovered
		}
	case models.CodeReviewSessionStatusFailed, models.CodeReviewSessionStatusCancelled, models.CodeReviewSessionStatusStale:
		state.State = models.CodeReviewScheduleIdle
	}
	return state, nil
}

func (s *Service) RequestScheduledReview(ctx context.Context, req ScheduleRequestInput) (ScheduleRequestResult, error) {
	if err := req.Mode.Validate(); err != nil {
		return ScheduleRequestResult{}, err
	}
	if req.RequestID == uuid.Nil {
		return ScheduleRequestResult{}, fmt.Errorf("request_id is required")
	}
	// A UI request operates on a previously monitored PR; it cannot turn an
	// arbitrary tenant PR UUID into a new review without the existing trigger.
	state, err := s.GetSchedule(ctx, req.OrgID, req.PullRequestID)
	if errors.Is(err, pgx.ErrNoRows) {
		latest, loadErr := s.metadata.GetLatestByPullRequest(ctx, req.OrgID, req.PullRequestID)
		if loadErr != nil {
			return ScheduleRequestResult{}, loadErr
		}
		state.RepositoryID = latest.RepositoryID
	} else if err != nil {
		return ScheduleRequestResult{}, err
	}
	input := ReviewChangedInput{OrgID: req.OrgID, RepositoryID: state.RepositoryID, PullRequestID: req.PullRequestID, ExplicitRequest: true, GitHubDeliveryID: req.RequestID.String(), ChangeReason: "ui.review_now", TriggerSource: models.CodeReviewTriggerSourceSlashCommand}
	result, err := s.scheduleReview(ctx, input, req.Mode, false, req.RequesterID)
	if err != nil {
		return ScheduleRequestResult{}, err
	}
	state, err = s.GetSchedule(ctx, req.OrgID, req.PullRequestID)
	disposition := models.CodeReviewRequestQueued
	if result.Reused {
		disposition = models.CodeReviewRequestJoined
	}
	if result.Reused && !result.Deferred {
		disposition = models.CodeReviewRequestReused
	}
	if result.IgnoredReason == "cancelled" {
		disposition = models.CodeReviewRequestCancelled
	}
	var sessionID *uuid.UUID
	if result.SessionID != uuid.Nil {
		sessionID = &result.SessionID
	}
	return ScheduleRequestResult{RequestID: req.RequestID, SessionID: sessionID, Disposition: disposition, Schedule: state}, err
}

func (s *Service) transactionalService(tx pgx.Tx) *Service {
	scoped := *s
	scoped.scheduling = nil
	metadata := db.NewCodeReviewStore(tx)
	scoped.metadata = metadata
	scoped.policies = metadata
	scoped.sessions = db.NewSessionStore(tx)
	scoped.jobs = db.NewJobStore(tx)
	scoped.pullRequests = db.NewPullRequestStore(tx)
	// Notifications before commit are unsafe; polling recovers session updates.
	scoped.statusCommentJobs = db.NewJobStore(tx)
	return &scoped
}

func (s *Service) scheduleReview(ctx context.Context, input ReviewChangedInput, mode models.CodeReviewRequestMode, force bool, requesterID *uuid.UUID) (ReviewRequestedResult, error) {
	if !s.SchedulingEnabled() {
		return ReviewRequestedResult{}, fmt.Errorf("review scheduling unavailable")
	}
	if input.OrgID == uuid.Nil || input.RepositoryID == uuid.Nil || input.PullRequestID == uuid.Nil {
		return ReviewRequestedResult{}, fmt.Errorf("review target is required")
	}
	if !input.ExplicitRequest {
		_, err := s.scheduling.store.Get(ctx, input.OrgID, input.PullRequestID)
		if errors.Is(err, pgx.ErrNoRows) {
			_, err = s.metadata.GetLatestByPullRequest(ctx, input.OrgID, input.PullRequestID)
			if errors.Is(err, pgx.ErrNoRows) {
				return ReviewRequestedResult{IgnoredReason: "review_not_previously_requested"}, nil
			}
		}
		if err != nil {
			return ReviewRequestedResult{}, err
		}
	}
	result := ReviewRequestedResult{Processed: true, Deferred: true}
	err := s.scheduling.store.WithLockedPR(ctx, input.OrgID, input.RepositoryID, input.PullRequestID, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
		scoped := s.transactionalService(tx)
		latest, latestErr := scoped.metadata.GetLatestByPullRequest(ctx, input.OrgID, input.PullRequestID)
		if latestErr != nil && !errors.Is(latestErr, pgx.ErrNoRows) {
			return latestErr
		}
		if !input.ExplicitRequest && errors.Is(latestErr, pgx.ErrNoRows) && state.PendingInput == nil {
			result = ReviewRequestedResult{IgnoredReason: "review_not_previously_requested"}
			return nil
		}
		// Explicit request identity excludes mutable provider revisions. Redelivery
		// remains idempotent after a push while genuinely different intent conflicts.
		var requestID uuid.UUID
		if input.ExplicitRequest {
			identity := strings.TrimSpace(input.GitHubDeliveryID)
			if identity == "" {
				identity = strings.TrimSpace(input.ChangeKey)
			}
			if identity == "" {
				return fmt.Errorf("explicit review requires delivery identity")
			}
			raw, err := json.Marshal(struct {
				PR      uuid.UUID
				Mode    models.CodeReviewRequestMode
				Context *ReviewRequestContext
				Force   bool
			}{input.PullRequestID, mode, input.RequestContext, force})
			if err != nil {
				return err
			}
			hash := fmt.Sprintf("%x", sha256.Sum256(raw))
			kind := "github"
			if requesterID != nil {
				kind = "ui"
			}
			var duplicate bool
			requestID, duplicate, err = db.RecordCodeReviewRequest(ctx, tx, input.OrgID, input.RepositoryID, input.PullRequestID, kind, identity, mode, hash, state.Generation+1, requesterID)
			if err != nil {
				return err
			}
			if duplicate {
				var status string
				var sessionID *uuid.UUID
				if err := tx.QueryRow(ctx, `SELECT session_id,status FROM code_review_requests WHERE org_id=$1 AND id=$2`, input.OrgID, requestID).Scan(&sessionID, &status); err != nil {
					return err
				}
				result.Reused = true
				result.Deferred = status == "pending" || status == "joined"
				if sessionID != nil {
					result.SessionID = *sessionID
				}
				if status == "cancelled" {
					result.IgnoredReason = "cancelled"
				}
				return nil
			}
		}
		now := s.scheduling.now()
		pr, err := db.NewPullRequestStore(tx).GetByID(ctx, input.OrgID, input.PullRequestID)
		if err != nil {
			return err
		}
		snapshot, err := s.scheduling.snapshots.GetCodeReviewPullRequestSnapshot(ctx, input.OrgID, input.RepositoryID, pr.GitHubPRNumber)
		if err != nil {
			return fmt.Errorf("%w: %w", errScheduleSnapshotUnavailable, err)
		}
		if requesterID != nil && (snapshot.State != "open" || snapshot.IsDraft) {
			return ErrReviewIneligible
		}
		if latestErr == nil {
			if err := adoptLegacyScheduledSession(ctx, tx, state, latest, snapshot.BaseRef, now); err != nil {
				return err
			}
		}
		changed := state.HeadSHA != snapshot.HeadSHA || state.BaseSHA != snapshot.BaseSHA || state.BaseRef != snapshot.BaseRef
		if changed || state.LastMaterialChangeAt == nil {
			state.LastMaterialChangeAt = &now
			state.Generation++
		}
		if state.Generation == 0 {
			state.Generation = 1
		}
		state.HeadSHA = snapshot.HeadSHA
		state.BaseSHA = snapshot.BaseSHA
		state.BaseRef = snapshot.BaseRef
		state.IsDraft = snapshot.IsDraft
		state.SnapshotObservedAt = &now
		input.GitHubRepo = pr.GitHubRepo
		input.GitHubPRNumber = snapshot.Number
		input.GitHubPRURL = snapshot.HTMLURL
		input.PullRequestTitle = snapshot.Title
		input.PullRequestAuthor = snapshot.AuthorLogin
		input.HeadSHA = snapshot.HeadSHA
		input.BaseSHA = snapshot.BaseSHA
		input.FromFork = snapshot.FromFork
		input.ChangeKey, err = MaterialChangeKey(input.HeadSHA)
		if err != nil {
			return err
		}
		var pending scheduledReviewIntent
		if state.PendingInput != nil {
			if err = json.Unmarshal(state.PendingInput, &pending); err != nil {
				return err
			}
		}
		// Automated updates move the target but preserve a pending explicit request.
		if pending.Input.ExplicitRequest && !input.ExplicitRequest {
			input.ExplicitRequest = true
			input.GitHubDeliveryID = pending.Input.GitHubDeliveryID
			input.RequestContext = pending.Input.RequestContext
			input.TriggerSource = pending.Input.TriggerSource
			input.TriggeringDisputeID = pending.Input.TriggeringDisputeID
			input.ReviewRequestDisputeID = pending.Input.ReviewRequestDisputeID
			mode = pending.Mode
			force = pending.Force
		}
		if pending.Mode == models.CodeReviewReviewNow {
			mode = pending.Mode
		}
		force = force || pending.Force || (latestErr == nil && latest.HeadSHA == snapshot.HeadSHA && latest.BaseSHA != snapshot.BaseSHA)
		if input.ExplicitRequest && latestErr == nil && latest.HeadSHA == snapshot.HeadSHA {
			var hasResults bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2)`, input.OrgID, latest.SessionID).Scan(&hasResults); err != nil {
				return err
			}
			earlyStop := false
			var err error
			if !hasResults {
				earlyStop, err = db.NewCodeReviewStore(tx).HasPriorDeterministicEarlyStop(ctx, input.OrgID, input.PullRequestID, uuid.Nil, snapshot.HeadSHA)
			}
			if err != nil {
				return err
			}
			force = force || earlyStop
			previous, err := scoped.sessions.GetByID(ctx, input.OrgID, latest.SessionID)
			if err != nil {
				return err
			}
			before, err := json.Marshal(codeReviewRevisionRequestContext(previous.RevisionContext))
			if err != nil {
				return err
			}
			after, err := json.Marshal(normalizeReviewRequestContext(input.RequestContext))
			if err != nil {
				return err
			}
			force = force || string(before) != string(after)
		}
		if force && !pending.Force && !changed {
			state.Generation++
		}
		if requestID != uuid.Nil {
			state.PendingRequestID = &requestID
		}
		if state.FirstPendingAt == nil {
			state.FirstPendingAt = &now
		}
		if _, err = tx.Exec(ctx, `UPDATE code_review_requests SET target_generation=$3 WHERE org_id=$1 AND pull_request_id=$2 AND status IN ('pending','joined')`, input.OrgID, input.PullRequestID, state.Generation); err != nil {
			return err
		}
		state.PendingInput, err = db.EncodeCodeReviewScheduleInput(scheduledReviewIntent{Input: input, Mode: mode, Force: force})
		if err != nil {
			return err
		}
		if snapshot.State != "open" {
			_, err := scoped.metadata.MarkStaleForPullRequestExceptHead(ctx, input.OrgID, input.PullRequestID, "", nil)
			if err != nil {
				return err
			}
			state.State = models.CodeReviewScheduleClosed
			state.WaitReason = models.CodeReviewWaitNone
			state.PendingInput = nil
			state.PendingRequestID = nil
			state.EligibleAt = nil
			state.RetryAt = nil
			return db.ResolveCodeReviewRequests(ctx, tx, input.OrgID, input.PullRequestID, state.Generation, nil, "cancelled")
		}
		policy, err := scoped.policies.ResolvePolicy(ctx, input.OrgID)
		if err != nil {
			return err
		}
		if requesterID != nil && !policy.Config.Enabled {
			return ErrReviewIneligible
		}
		approved, err := scoped.metadata.HasApprovedByPullRequest(ctx, input.OrgID, input.PullRequestID)
		if err != nil {
			return err
		}
		applyScheduleWait(state, policy.Config, input.ExplicitRequest, approved, mode, now)
		// SHA cancellation is deliberately retained for Stage 1, after the latest
		// replacement intent is durable. Actual thread cancellation follows commit.
		if latestErr == nil && (latest.HeadSHA != snapshot.HeadSHA || latest.BaseSHA != snapshot.BaseSHA || snapshot.IsDraft) {
			head := snapshot.HeadSHA
			if snapshot.IsDraft || latest.BaseSHA != snapshot.BaseSHA {
				head = ""
			}
			_, err := scoped.metadata.MarkStaleForPullRequestExceptHead(ctx, input.OrgID, input.PullRequestID, head, nil)
			if err != nil {
				return err
			}
		}
		if state.PendingInput == nil {
			return nil
		}
		wake := now
		if state.EligibleAt != nil {
			wake = *state.EligibleAt
		}
		if state.RetryAt != nil {
			wake = *state.RetryAt
		}
		return db.UpsertCodeReviewWake(ctx, tx, input.OrgID, input.PullRequestID, wake)
	})
	if err != nil {
		return ReviewRequestedResult{}, err
	}
	if err := s.cancelStaleScheduledThreads(ctx, input.OrgID, input.PullRequestID); err != nil {
		return result, err
	}

	return result, nil
}

func (s *Service) cancelStaleScheduledThreads(ctx context.Context, orgID, prID uuid.UUID) error {
	ids, err := s.scheduling.store.StaleActiveSessions(ctx, orgID, prID)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	if s.threadCanceller == nil {
		return fmt.Errorf("review thread cancellation unavailable")
	}
	_, err = s.threadCanceller.CancelActiveThreads(ctx, orgID, ids)
	return err
}

func applyScheduleWait(state *models.CodeReviewPRState, policy models.CodeReviewPolicyConfig, explicit, approved bool, mode models.CodeReviewRequestMode, now time.Time) {
	state.State = models.CodeReviewScheduleWaiting
	state.WaitReason = models.CodeReviewWaitNone
	state.RetryAt = nil
	settings := policy.SchedulingPolicy.Effective()
	// Approval is permanent for automatic admission. Clear the intent before
	// draft/policy/pause holds can turn it into a perpetual polling loop.
	if !explicit && approved {
		state.State = models.CodeReviewSchedulePaused
		state.WaitReason = models.CodeReviewWaitApproved
		state.PendingInput = nil
		state.PendingRequestID = nil
		state.FirstPendingAt = nil
		state.EligibleAt = nil
		return
	}
	switch {
	case state.IsDraft:
		state.WaitReason = models.CodeReviewWaitDraft
	case !policy.Enabled:
		state.WaitReason = models.CodeReviewWaitPolicy
	case !explicit && (state.AutomaticPaused || !settings.AutomaticReReview):
		state.WaitReason = models.CodeReviewWaitPaused
	}
	if state.WaitReason != models.CodeReviewWaitNone {
		state.State = models.CodeReviewSchedulePaused
		state.EligibleAt = nil
		retry := now.Add(5 * time.Minute)
		state.RetryAt = &retry
		return
	}
	due := now
	if mode != models.CodeReviewReviewNow && state.LastMaterialChangeAt != nil {
		due = settings.EligibleAt(*state.LastMaterialChangeAt, state.LastAgentStartAt)
	}
	state.EligibleAt = &due
	if due.After(now) {
		state.WaitReason = models.CodeReviewWaitQuiet
		if state.LastAgentStartAt != nil && state.LastAgentStartAt.Add(time.Duration(settings.MinimumIntervalSeconds)*time.Second).After(state.LastMaterialChangeAt.Add(time.Duration(settings.QuietPeriodSeconds)*time.Second)) {
			state.WaitReason = models.CodeReviewWaitInterval
		}
	}
}

// ReconcileSchedule refreshes GitHub, then claims the current target while
// holding the PR lock. Session, metadata, queue insertion, and request linkage
// commit together. A lost wake-job lease rolls the transaction back.
func (s *Service) ReconcileSchedule(ctx context.Context, wake models.CodeReviewScheduleWake) error {
	state, err := s.GetSchedule(ctx, wake.OrgID, wake.PullRequestID)
	if err != nil {
		return err
	}
	if state.PendingInput != nil {
		_, err = s.scheduleReview(ctx, ReviewChangedInput{OrgID: wake.OrgID, RepositoryID: state.RepositoryID, PullRequestID: wake.PullRequestID}, models.CodeReviewEnsureCurrent, false, nil)
		if err != nil {
			if errors.Is(err, errScheduleSnapshotUnavailable) {
				return s.deferUnavailableSchedule(ctx, wake, state, err)
			}
			return err
		}
	}
	if err := s.cancelStaleScheduledThreads(ctx, wake.OrgID, wake.PullRequestID); err != nil {
		return err
	}
	return s.scheduling.store.WithLockedPR(ctx, wake.OrgID, state.RepositoryID, wake.PullRequestID, func(tx pgx.Tx, current *models.CodeReviewPRState) error {
		jobs := db.NewJobStore(tx)
		jobID, hasJob := jobctx.JobIDFromContext(ctx)
		token, hasToken := jobctx.LockTokenFromContext(ctx)
		if !hasJob || !hasToken {
			return fmt.Errorf("schedule reconciliation requires a worker lease")
		}
		var owned bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE org_id=$1 AND id=$2 AND lock_token=$3 AND status='running' AND lease_expires_at>now())`, wake.OrgID, jobID, token).Scan(&owned); err != nil {
			return err
		}
		if !owned {
			return fmt.Errorf("schedule wake lease lost")
		}
		now := s.scheduling.now()
		finish := func() error {
			ok, err := jobs.MarkSucceededWithLease(ctx, jobID, token)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("schedule wake lease lost")
			}
			return nil
		}
		wait := func(at time.Time) error {
			ok, err := jobs.RetryWithoutConsumingAttemptWithLease(ctx, jobID, token, "waiting for review eligibility", at)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("schedule wake lease lost")
			}
			return nil
		}
		if current.PendingInput == nil {
			return finish()
		}
		var intent scheduledReviewIntent
		if err := json.Unmarshal(current.PendingInput, &intent); err != nil {
			return err
		}
		scoped := s.transactionalService(tx)
		policy, err := scoped.policies.ResolvePolicy(ctx, wake.OrgID)
		if err != nil {
			return err
		}
		approved, err := scoped.metadata.HasApprovedByPullRequest(ctx, wake.OrgID, wake.PullRequestID)
		if err != nil {
			return err
		}
		// MIN per session is its first real reviewer dispatch; MAX across sessions
		// yields the last review start. Queued sessions and pure policy checks have
		// no started reviewer thread and therefore do not consume cadence.
		var lastStart *time.Time
		err = tx.QueryRow(ctx, `SELECT MAX(first_start) FROM (SELECT MIN(t.started_at) AS first_start FROM session_threads t JOIN code_review_session_metadata m ON m.org_id=t.org_id AND m.session_id=t.session_id WHERE m.org_id=$1 AND m.pull_request_id=$2 AND t.label LIKE 'Code review:%' GROUP BY m.session_id) starts`, wake.OrgID, wake.PullRequestID).Scan(&lastStart)
		if err != nil {
			return err
		}
		if lastStart != nil {
			current.LastAgentStartAt = lastStart
		}
		applyScheduleWait(current, policy.Config, intent.Input.ExplicitRequest, approved, intent.Mode, now)
		if current.PendingInput == nil {
			return finish()
		}
		if current.RetryAt != nil {
			return wait(*current.RetryAt)
		}
		if current.EligibleAt != nil && current.EligibleAt.After(now) {
			return wait(*current.EligibleAt)
		}
		// Serialize across all heads/policies, including stale threads still draining.
		active, err := db.HasActiveCodeReview(ctx, tx, wake.OrgID, wake.PullRequestID, codeReviewJobEnqueueGracePeriod)
		if err != nil {
			return err
		}
		if active {
			current.WaitReason = models.CodeReviewWaitActive
			at := now.Add(15 * time.Second)
			current.RetryAt = &at
			return wait(at)
		}
		in := intent.Input
		requested := ReviewRequestedInput{OrgID: in.OrgID, RepositoryID: in.RepositoryID, PullRequestID: in.PullRequestID, GitHubRepo: in.GitHubRepo, GitHubPRNumber: in.GitHubPRNumber, GitHubPRURL: in.GitHubPRURL, PullRequestTitle: in.PullRequestTitle, PullRequestAuthor: in.PullRequestAuthor, BaseSHA: in.BaseSHA, HeadSHA: in.HeadSHA, FromFork: in.FromFork, RequestedLogin: in.RequestedReviewerLogin, RequestedTeam: in.RequestedTeamSlug, RequestContext: in.RequestContext, DeliveryID: in.GitHubDeliveryID}
		source := in.TriggerSource
		if source == "" {
			source = models.CodeReviewTriggerSourceAutoPolicy
		}
		opts := reviewStartOptions{triggerSource: source, forceReassessment: intent.Force, changeKey: fmt.Sprintf("schedule:%d", current.Generation), changeReason: in.ChangeReason, triggeringDisputeID: in.TriggeringDisputeID}
		submitted, err := scoped.metadata.GetLatestSubmittedByPullRequest(ctx, in.OrgID, in.PullRequestID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			opts.previousOutputKey = submitted.ReviewOutputKey
			opts.previousReviewDecision = submitted.Decision
			opts.previousReviewDecidedAt = submitted.CompletedAt
			opts.previousReviewBody = submitted.FinalReviewBody
			opts.existingGitHubReviewID = submitted.GitHubReviewID
			opts.existingGitHubReviewURL = submitted.GitHubReviewURL
		}
		result, err := scoped.startReview(ctx, requested, opts)
		if err != nil {
			return err
		}
		if result.Deferred {
			at := now.Add(15 * time.Second)
			return wait(at)
		}
		if result.SessionID == uuid.Nil {
			current.State = models.CodeReviewSchedulePaused
			current.WaitReason = models.CodeReviewWaitPolicy
			at := now.Add(5 * time.Minute)
			current.RetryAt = &at
			return wait(at)
		}
		if !result.Reused {
			if err := bindScheduledSession(ctx, tx, in.OrgID, result.SessionID, current.Generation); err != nil {
				return err
			}
		}
		if in.TriggeringDisputeID != nil {
			if err := db.NewCodeReviewDisputeStore(tx).MarkReassessmentStarted(ctx, in.OrgID, *in.TriggeringDisputeID, result.SessionID); err != nil {
				return err
			}
		}
		current.ActiveSessionID = &result.SessionID
		current.PendingInput = nil
		current.PendingRequestID = nil
		current.FirstPendingAt = nil
		current.EligibleAt = nil
		current.RetryAt = nil
		current.State = models.CodeReviewScheduleRunning
		current.WaitReason = models.CodeReviewWaitNone
		if result.Reused {
			current.State = models.CodeReviewScheduleCovered
		}
		if err = db.ResolveCodeReviewRequests(ctx, tx, wake.OrgID, wake.PullRequestID, current.Generation, &result.SessionID, "satisfied"); err != nil {
			return err
		}
		return finish()
	})
}

func (s *Service) PauseSchedule(ctx context.Context, orgID, prID uuid.UUID, paused bool) error {
	state, err := s.GetSchedule(ctx, orgID, prID)
	if err != nil {
		return err
	}
	return s.scheduling.store.WithLockedPR(ctx, orgID, state.RepositoryID, prID, func(tx pgx.Tx, current *models.CodeReviewPRState) error {
		current.AutomaticPaused = paused
		if current.PendingInput != nil {
			return db.UpsertCodeReviewWake(ctx, tx, orgID, prID, s.scheduling.now())
		}
		return nil
	})
}

func (s *Service) ListPendingSchedules(ctx context.Context, orgID uuid.UUID, repoID, after *uuid.UUID, limit int) ([]models.CodeReviewScheduledTarget, error) {
	if !s.SchedulingEnabled() {
		return nil, fmt.Errorf("review scheduling unavailable")
	}
	return s.scheduling.store.ListPending(ctx, orgID, repoID, after, limit)
}

func (s *Service) retryScheduledReview(ctx context.Context, input RetryReviewInput) (RetryReviewResult, error) {
	source, err := s.metadata.GetBySessionID(ctx, input.OrgID, input.SessionID)
	if err != nil {
		return RetryReviewResult{}, err
	}
	var result RetryReviewResult
	err = s.scheduling.store.WithLockedPR(ctx, input.OrgID, source.RepositoryID, source.PullRequestID, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
		scoped := s.transactionalService(tx)
		identity := "retry:" + source.SessionID.String()
		var existingSession *uuid.UUID
		err := tx.QueryRow(ctx, `SELECT session_id FROM code_review_requests WHERE org_id=$1 AND pull_request_id=$2 AND source_kind='retry' AND source_identity=$3`, input.OrgID, source.PullRequestID, identity).Scan(&existingSession)
		if err == nil && existingSession != nil {
			metadata, loadErr := scoped.metadata.GetBySessionID(ctx, input.OrgID, *existingSession)
			if loadErr != nil {
				return loadErr
			}
			result = RetryReviewResult{SessionID: *existingSession, PreviousSessionID: source.SessionID, MetadataID: metadata.ID}
			return nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		active, err := db.HasActiveCodeReview(ctx, tx, input.OrgID, source.PullRequestID, codeReviewJobEnqueueGracePeriod)
		if err != nil {
			return err
		}
		if active {
			return &RetryReviewConflictError{Code: RetryReviewConflictNewerAttempt, Message: "A review is still active. Wait for it to finish before retrying."}
		}
		pr, err := db.NewPullRequestStore(tx).GetByID(ctx, input.OrgID, source.PullRequestID)
		if err != nil {
			return err
		}
		snapshot, err := s.scheduling.snapshots.GetCodeReviewPullRequestSnapshot(ctx, input.OrgID, source.RepositoryID, pr.GitHubPRNumber)
		if err != nil {
			return err
		}
		if snapshot.IsDraft || snapshot.State != "open" {
			return ErrReviewIneligible
		}
		if snapshot.HeadSHA != source.HeadSHA {
			return &RetryReviewConflictError{Code: RetryReviewConflictHeadChanged, Message: "Pull request head changed. Request a review of the latest revision."}
		}
		result, err = scoped.RetryReview(ctx, input)
		if err != nil {
			return err
		}
		observeScheduledStart(state, snapshot, s.scheduling.now())
		if err := bindScheduledSession(ctx, tx, input.OrgID, result.SessionID, state.Generation); err != nil {
			return err
		}
		state.ActiveSessionID = &result.SessionID
		state.State = models.CodeReviewScheduleRunning
		_, _, err = db.RecordCodeReviewRequest(ctx, tx, input.OrgID, source.RepositoryID, source.PullRequestID, "retry", identity, models.CodeReviewReviewNow, identity, state.Generation, nil)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE code_review_requests SET session_id=$4,retry_of_session_id=$5,status='satisfied' WHERE org_id=$1 AND pull_request_id=$2 AND source_kind='retry' AND source_identity=$3`, input.OrgID, source.PullRequestID, identity, result.SessionID, source.SessionID)
		return err
	})
	return result, err
}

// Disputes keep their individual provenance and dedicated starter jobs. They
// share the PR admission lock without coalescing distinct objections away.
func (s *Service) handleSerializedReviewChanged(ctx context.Context, input ReviewChangedInput) (ReviewRequestedResult, error) {
	var result ReviewRequestedResult
	err := s.scheduling.store.WithLockedPR(ctx, input.OrgID, input.RepositoryID, input.PullRequestID, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
		scoped := s.transactionalService(tx)
		snapshot, err := s.scheduling.snapshots.GetCodeReviewPullRequestSnapshot(ctx, input.OrgID, input.RepositoryID, input.GitHubPRNumber)
		if err != nil {
			return err
		}
		if snapshot.State != "open" {
			result.IgnoredReason = "pull_request_closed"
			return nil
		}
		if snapshot.IsDraft {
			result.Deferred = true
			result.IgnoredReason = "draft"
			return nil
		}
		active, err := db.HasActiveCodeReview(ctx, tx, input.OrgID, input.PullRequestID, codeReviewJobEnqueueGracePeriod)
		if err != nil {
			return err
		}
		if active {
			result.Deferred = true
			return nil
		}
		input.HeadSHA = snapshot.HeadSHA
		input.BaseSHA = snapshot.BaseSHA
		result, err = scoped.HandleReviewChanged(ctx, input)
		if err != nil {
			return err
		}
		if result.SessionID != uuid.Nil && !result.Reused {
			observeScheduledStart(state, snapshot, s.scheduling.now())
			if err := bindScheduledSession(ctx, tx, input.OrgID, result.SessionID, state.Generation); err != nil {
				return err
			}
			state.ActiveSessionID = &result.SessionID
			state.State = models.CodeReviewScheduleRunning
		}
		return nil
	})
	return result, err
}

// schedule_generation is independent of change_key: retries and disputes keep
// their own identity keys, while every newly allocated scheduled session is
// bound to the generation that admitted it in the same transaction.
func bindScheduledSession(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID, generation int64) error {
	_, err := tx.Exec(ctx, `UPDATE sessions SET revision_context=jsonb_set(COALESCE(revision_context,'{}'::jsonb),'{schedule_generation}',to_jsonb($3::bigint)) WHERE org_id=$1 AND id=$2`, orgID, sessionID, generation)
	return err
}

func scheduledSessionGeneration(raw json.RawMessage) (int64, bool, error) {
	var value struct {
		Generation json.RawMessage `json:"schedule_generation"`
		ChangeKey  string          `json:"change_key"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false, err
	}
	if len(value.Generation) != 0 {
		var generation int64
		if err := json.Unmarshal(value.Generation, &generation); err != nil {
			return 0, false, err
		}
		if generation < 1 {
			return 0, false, fmt.Errorf("invalid review schedule generation")
		}
		return generation, true, nil
	}
	if strings.HasPrefix(value.ChangeKey, "schedule:") {
		generation, err := strconv.ParseInt(strings.TrimPrefix(value.ChangeKey, "schedule:"), 10, 64)
		if err != nil || generation < 1 {
			return 0, false, fmt.Errorf("invalid legacy review schedule generation %q", value.ChangeKey)
		}
		return generation, true, nil
	}
	return 0, false, nil
}

func observeScheduledStart(state *models.CodeReviewPRState, snapshot ghservice.CodeReviewPullRequestSnapshot, now time.Time) {
	state.Generation++
	if state.LastMaterialChangeAt == nil || state.HeadSHA != snapshot.HeadSHA || state.BaseSHA != snapshot.BaseSHA || state.BaseRef != snapshot.BaseRef {
		state.LastMaterialChangeAt = &now
	}
	state.HeadSHA, state.BaseSHA, state.BaseRef = snapshot.HeadSHA, snapshot.BaseSHA, snapshot.BaseRef
	state.IsDraft = snapshot.IsDraft
	state.SnapshotObservedAt = &now
}

// Adopt only the latest, still-active untagged attempt and never reinterpret a
// pending forced replacement as its provenance. Bind before any generation
// advance so old sessions remain fenced even when hashes have not changed.
func adoptLegacyScheduledSession(ctx context.Context, tx pgx.Tx, state *models.CodeReviewPRState, latest models.CodeReviewSessionMetadata, baseRef string, now time.Time) error {
	if latest.Status != models.CodeReviewSessionStatusQueued && latest.Status != models.CodeReviewSessionStatusRunning {
		return nil
	}
	session, err := db.NewSessionStore(tx).GetByID(ctx, latest.OrgID, latest.SessionID)
	if err != nil {
		return err
	}
	_, bound, err := scheduledSessionGeneration(session.RevisionContext)
	if err != nil || bound {
		return err
	}
	if state.PendingInput != nil {
		var pending scheduledReviewIntent
		if err := json.Unmarshal(state.PendingInput, &pending); err != nil {
			return err
		}
		if pending.Force {
			return nil
		}
	}
	if state.Generation == 0 {
		// Legacy sessions predate the PR state row. Establish their baseline
		// before binding; an equivalent first event must not revoke them.
		state.Generation = 1
		state.HeadSHA, state.BaseSHA, state.BaseRef = latest.HeadSHA, latest.BaseSHA, baseRef
		state.LastMaterialChangeAt = &now
	}
	if state.HeadSHA != latest.HeadSHA || state.BaseSHA != latest.BaseSHA || (state.ActiveSessionID != nil && *state.ActiveSessionID != latest.SessionID) {
		return nil
	}
	return bindScheduledSession(ctx, tx, latest.OrgID, latest.SessionID, state.Generation)
}

// ValidateScheduledExecution fences fan-out and publication by both provider
// revision and persisted admission generation. Revocation is durable because
// workers deliberately finish their jobs successfully when this returns false.
func (s *Service) ValidateScheduledExecution(ctx context.Context, orgID, prID, sessionID uuid.UUID) (bool, error) {
	if !s.SchedulingEnabled() {
		return true, nil
	}
	metadata, err := s.metadata.GetBySessionID(ctx, orgID, sessionID)
	if err != nil {
		return false, err
	}
	valid, refreshTarget := false, false
	err = s.scheduling.store.WithLockedPR(ctx, orgID, metadata.RepositoryID, prID, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
		store := db.NewCodeReviewStore(tx)
		metadata, err := store.GetBySessionID(ctx, orgID, sessionID)
		if err != nil {
			return err
		}
		if metadata.PullRequestID != prID {
			return fmt.Errorf("review session does not belong to pull request")
		}
		if metadata.Status == models.CodeReviewSessionStatusStale || metadata.Status == models.CodeReviewSessionStatusCancelled || metadata.Status == models.CodeReviewSessionStatusFailed {
			return nil
		}
		pr, err := db.NewPullRequestStore(tx).GetByID(ctx, orgID, prID)
		if err != nil {
			return err
		}
		snapshot, err := s.scheduling.snapshots.GetCodeReviewPullRequestSnapshot(ctx, orgID, metadata.RepositoryID, pr.GitHubPRNumber)
		if err != nil {
			return err
		}
		latest, err := store.GetLatestByPullRequest(ctx, orgID, prID)
		if err != nil {
			return err
		}
		if latest.SessionID == sessionID {
			if err := adoptLegacyScheduledSession(ctx, tx, state, latest, snapshot.BaseRef, s.scheduling.now()); err != nil {
				return err
			}
		}
		session, err := db.NewSessionStore(tx).GetByID(ctx, orgID, sessionID)
		if err != nil {
			return err
		}
		generation, bound, err := scheduledSessionGeneration(session.RevisionContext)
		if err != nil {
			return err
		}
		refreshTarget = snapshot.State != "open" || snapshot.IsDraft || snapshot.HeadSHA != metadata.HeadSHA || snapshot.BaseSHA != metadata.BaseSHA
		valid = !refreshTarget && bound && generation == state.Generation
		if !valid && !refreshTarget && (metadata.Status == models.CodeReviewSessionStatusQueued || metadata.Status == models.CodeReviewSessionStatusRunning) {
			_, err = store.MarkStale(ctx, orgID, sessionID, "review scheduling target superseded")
		}
		return err
	})
	if err != nil || valid {
		return valid, err
	}
	if refreshTarget {
		// Persist replacement intent before revoking the old session. If the
		// refresh fails, its worker must retry and can still recover the intent.
		if _, err = s.scheduleReview(ctx, ReviewChangedInput{OrgID: orgID, RepositoryID: metadata.RepositoryID, PullRequestID: prID}, models.CodeReviewEnsureCurrent, false, nil); err != nil {
			return false, err
		}
		err = s.scheduling.store.WithLockedPR(ctx, orgID, metadata.RepositoryID, prID, func(tx pgx.Tx, _ *models.CodeReviewPRState) error {
			store := db.NewCodeReviewStore(tx)
			current, err := store.GetBySessionID(ctx, orgID, sessionID)
			if err != nil {
				return err
			}
			if current.Status != models.CodeReviewSessionStatusQueued && current.Status != models.CodeReviewSessionStatusRunning {
				return nil
			}
			_, err = store.MarkStale(ctx, orgID, sessionID, "review scheduling target superseded")
			return err
		})
		if err != nil {
			return false, err
		}
	}
	return false, s.cancelStaleScheduledThreads(ctx, orgID, prID)
}

// Provider outages retain intent with a visible reason and release the worker
// lease without burning attempts or repeatedly allocating replacement jobs.
func (s *Service) deferUnavailableSchedule(ctx context.Context, wake models.CodeReviewScheduleWake, state models.CodeReviewPRState, cause error) error {
	s.logger.Warn().Err(cause).Str("org_id", wake.OrgID.String()).Str("pull_request_id", wake.PullRequestID.String()).Msg("review scheduling waiting for GitHub")
	now := s.scheduling.now()
	delay := time.Minute
	classification := ghservice.ClassifyRetry(cause, now)
	if classification.RetryAfter != nil && *classification.RetryAfter > delay {
		delay = *classification.RetryAfter
	}
	at := now.Add(delay)
	return s.scheduling.store.WithLockedPR(ctx, wake.OrgID, state.RepositoryID, wake.PullRequestID, func(tx pgx.Tx, current *models.CodeReviewPRState) error {
		id, hasID := jobctx.JobIDFromContext(ctx)
		token, hasToken := jobctx.LockTokenFromContext(ctx)
		if !hasID || !hasToken {
			return fmt.Errorf("schedule context wait requires worker lease")
		}
		jobs := db.NewJobStore(tx)
		var ok bool
		var err error
		if current.PendingInput == nil {
			ok, err = jobs.MarkSucceededWithLease(ctx, id, token)
		} else {
			current.State = models.CodeReviewScheduleWaiting
			current.WaitReason = models.CodeReviewWaitContext
			current.RetryAt = &at
			current.EligibleAt = nil
			ok, err = jobs.RetryWithoutConsumingAttemptWithLease(ctx, id, token, "waiting for GitHub snapshot", at)
		}
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("schedule wake lease lost")
		}
		return nil
	})
}
