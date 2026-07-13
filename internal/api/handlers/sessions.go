package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/api/sse"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/gitref"
	"github.com/assembledhq/143/internal/llm"
	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	"github.com/assembledhq/143/internal/services"
	"github.com/assembledhq/143/internal/services/agentcapabilities"
	ghservice "github.com/assembledhq/143/internal/services/github"
	humaninputsvc "github.com/assembledhq/143/internal/services/humaninput"
	"github.com/assembledhq/143/internal/services/linear"
	previewsvc "github.com/assembledhq/143/internal/services/preview"
	prreadinesssvc "github.com/assembledhq/143/internal/services/prreadiness"
	"github.com/assembledhq/143/internal/services/sessiontimeline"
	"github.com/assembledhq/143/internal/services/storage"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// SessionCanceller can cancel a running agent session. The Orchestrator
// implements this interface; it is injected after construction via SetCanceller.
type SessionCanceller interface {
	CancelSession(sessionID uuid.UUID) bool
}

type sessionWorkerSelector interface {
	ResolveNode(ctx context.Context, nodeID string) (previewsvc.WorkerNode, error)
}

type sessionWorkerCancelClient interface {
	CancelSession(ctx context.Context, worker previewsvc.WorkerNode, req previewsvc.RemoteCancelSessionRequest) (*previewsvc.RemoteCancelSessionResponse, error)
}

type sessionPRTitleSyncer interface {
	SyncSessionTitle(ctx context.Context, session *models.Session) error
}

// sessionMembershipStore is the subset of OrganizationMembershipStore that
// StreamLogs needs to authorize cross-org SSE subscriptions when the client
// can't send X-Active-Org-ID (EventSource has no header API). See StreamLogs
// for context.
type sessionMembershipStore interface {
	Get(ctx context.Context, userID, orgID uuid.UUID) (models.OrganizationMembership, error)
}

type SessionHandler struct {
	runStore           *db.SessionStore
	logStore           *db.SessionLogStore
	questionStore      *db.SessionQuestionStore
	humanInputStore    *db.SessionHumanInputRequestStore
	humanInputService  *humaninputsvc.Service
	capabilityService  *agentcapabilities.Service
	pullRequestStore   *db.PullRequestStore
	changesetStore     *db.SessionChangesetStore
	issueStore         *db.IssueStore
	repoStore          *db.RepositoryStore
	orgStore           *db.OrganizationStore
	userStore          *db.UserStore
	jobStore           *db.JobStore
	txStarter          db.TxStarter
	messageStore       *db.SessionMessageStore
	reviewLoopStore    *db.SessionReviewLoopStore
	readinessStore     *db.PRReadinessStore
	reviewCommentStore *db.SessionReviewCommentStore
	linkStore          *db.SessionIssueLinkStore
	slackSessionLinks  *db.SlackSessionLinkStore
	issueSnapshots     *db.SessionTurnIssueSnapshotStore
	threadStore        *db.SessionThreadStore
	readinessRunner    interface {
		EnqueueRun(ctx context.Context, req prreadinesssvc.EnqueueRunRequest) (*models.PRReadinessRun, error)
	}
	threadInboxStore *db.ThreadInboxStore
	attributionStore *db.SessionAttributionStore
	sandboxHolders   *db.SessionSandboxHolderStore
	viewStore        *db.SessionViewStore
	memberships      sessionMembershipStore
	prCredentials    githubStatusCredentialStore
	prAuthChecker    interface {
		HasValidCredential(ctx context.Context, orgID, userID uuid.UUID) (bool, error)
	}
	snapshotStore    storage.SnapshotStore // optional — enables snapshot cleanup on archive
	llmClient        llm.Client            // optional, used for generating manual session titles
	logger           zerolog.Logger
	audit            *db.AuditEmitter
	canceller        SessionCanceller // optional — enables cancelling running sessions
	workerSelector   sessionWorkerSelector
	workerClient     sessionWorkerCancelClient
	localNodeID      string
	prTitleSyncer    sessionPRTitleSyncer
	prAuthSigningKey []byte
	frontendURL      string
	streams          *cache.SessionStreams
	// linearLinker is wired at boot via SetLinearLinker (called from
	// router.go after the SSE-aware Linear service is constructed). Held
	// behind an atomic.Pointer holder so a request that lands during boot —
	// before the setter returns — observes nil cleanly instead of racing
	// on the interface-value assignment, which the race detector flags as
	// undefined behavior on multi-word interface stores.
	linearLinker atomic.Pointer[linearLinkerHolder]
	// shutdownCh is closed on SIGTERM; see SetShutdownSignal.
	shutdownCh <-chan struct{}
	// pollOverride is non-zero in tests to lock the SSE polling fallback to
	// a predictable interval. Production leaves it at zero so the per-
	// connection interval is sampled from sseFallbackPoll{Min,Max} for
	// every reconnect, which staggers Postgres queries across clients and
	// avoids the synchronized N-client dogpile that follows a Redis outage
	// when many SSE connections fail over at once. See SetPollIntervalForTest.
	//
	// Stored as nanoseconds in atomic.Int64 (not a plain time.Duration field)
	// because parallel tests share the SessionHandler instance via
	// newSessionHandler(t, mock) and read this field from request-serving
	// goroutines while another test goroutine may still be calling the
	// setter — the race detector would (correctly) flag a plain field even
	// though the setter is conventionally called once per test before any
	// requests fire.
	pollOverrideNanos atomic.Int64
}

const (
	// sseFallbackPollMin/Max bracket the per-connection polling interval used
	// when the Redis-backed log stream is unavailable and we must serve
	// updates from Postgres. The 1.0s value used here previously translated
	// into N concurrent SSE clients × 1 query/sec on the runs+logs tables —
	// fine for steady-state, problematic when a Redis outage drops every
	// client onto this path simultaneously and they then reconnect in
	// lockstep when Redis comes back. The min/max bracket plus
	// sseFallbackPollInterval's uniform sample give each connection an
	// independent phase, capping the worst-case query rate at ~0.5 N/sec
	// while keeping the median responsiveness well under the 5s SLA the
	// frontend's reconnect logic expects.
	sseFallbackPollMin = 2000 * time.Millisecond
	sseFallbackPollMax = 3500 * time.Millisecond
	// sseFallbackHeartbeatMin/Max jitter the SSE keepalive interval for the
	// same reason — synchronized heartbeats from many connections produce
	// brief CPU/syscall spikes that don't matter at small N but do at large
	// N. The 12-18s window stays well under typical proxy idle timeouts (30
	// or 60s) and the 2× ratio between min and max is enough to break
	// alignment after a few minutes of running connections.
	sseFallbackHeartbeatMin = 12 * time.Second
	sseFallbackHeartbeatMax = 18 * time.Second
)

// sseFallbackPollInterval returns a per-connection randomized polling interval
// in [sseFallbackPollMin, sseFallbackPollMax]. The override path is reserved
// for tests that need a sub-second interval to keep wall-clock test time
// reasonable; production code never sets it.
func (h *SessionHandler) sseFallbackPollInterval() time.Duration {
	if override := time.Duration(h.pollOverrideNanos.Load()); override > 0 {
		return override
	}
	span := int64(sseFallbackPollMax - sseFallbackPollMin)
	// #nosec G404 -- jitter is a load-shedding mechanism (decorrelate per-
	// connection Postgres polling so an outage doesn't cause an N-client
	// dogpile), not a security primitive. An attacker who could predict
	// the interval gains nothing useful; crypto/rand would just burn
	// entropy and CPU on a hot path.
	return sseFallbackPollMin + time.Duration(rand.Int64N(span+1))
}

// sseFallbackHeartbeatInterval returns a per-connection randomized heartbeat
// interval in [sseFallbackHeartbeatMin, sseFallbackHeartbeatMax]. Same
// override-for-tests semantics as sseFallbackPollInterval.
func (h *SessionHandler) sseFallbackHeartbeatInterval() time.Duration {
	if override := time.Duration(h.pollOverrideNanos.Load()); override > 0 {
		// In test mode, scale the heartbeat down proportionally so tests that
		// exercise the heartbeat branch don't have to wait the production
		// floor. 5x the poll interval is enough to keep the heartbeat from
		// firing on every poll tick.
		return override * 5
	}
	span := int64(sseFallbackHeartbeatMax - sseFallbackHeartbeatMin)
	// #nosec G404 -- non-security jitter; see sseFallbackPollInterval above.
	return sseFallbackHeartbeatMin + time.Duration(rand.Int64N(span+1))
}

// SetPollIntervalForTest pins the SSE polling-fallback interval for tests
// that need deterministic, fast iteration. Calling with d <= 0 restores the
// default randomized behavior. Safe to call concurrently with request-
// serving goroutines via the underlying atomic store.
func (h *SessionHandler) SetPollIntervalForTest(d time.Duration) {
	if d <= 0 {
		h.pollOverrideNanos.Store(0)
		return
	}
	h.pollOverrideNanos.Store(int64(d))
}

// linearLinkerHolder wraps the linearSessionLinker interface so the field
// has a single concrete pointer type for atomic.Pointer to operate on.
type linearLinkerHolder struct{ fn linearSessionLinker }

// linearSessionLinker is the surface SessionHandler invokes on the Linear
// service. The interface lives here (not in the linear package) so
// SetLinearLinker can accept a stub in tests without bringing up the full
// Linear stack. We do still depend on linear.CreateInput / linear.CreateResult
// at the type level — those are pure value structs that the handler builds
// anyway, and re-defining them locally would only add adapter code without
// breaking the dependency.
//
// TeamKeyAllowlist is exposed so the MISSING_MESSAGE bypass can verify that
// a bare-identifier reference actually maps to a known Linear team before
// letting an empty-message session through. Without this check, any
// "FOO-123"-shaped string in the references picker would bypass the
// validation and produce a session with an empty user turn.
type linearSessionLinker interface {
	ResolveAndLinkAtCreate(ctx context.Context, in linear.CreateInput) (linear.CreateResult, error)
	ResolveAndLinkMidSession(ctx context.Context, in linear.MidSessionInput) error
	Enabled(ctx context.Context, orgID uuid.UUID) bool
	TeamKeyAllowlist(ctx context.Context, orgID uuid.UUID) (map[string]bool, error)
}

// looksLikeLinearReference is the legacy regex-only hint, retained for the
// no-linker path and for callers that don't yet have an org context. Cheap
// pre-validation hint: when the user supplied no message text but the
// references picker carries Linear-shaped content, allow CreateManual to
// proceed and let the linker hydrate context from the issue alone
// (design 62 §"Issue-only session start"). Detection runs again inside the
// linker — we are not relaxing security here, only the message-required
// check.
//
// Defers to linear.MightContainLinearRef so this hint stays in lockstep
// with the actual detection regexes.
func looksLikeLinearReference(refs []models.SessionInputReference) bool {
	for _, r := range refs {
		// Reference-picker entries can carry the Linear key in either Display
		// (human-readable label) or ID (the picker's underlying value), so
		// scan both. Without ID we'd miss pickers that put "ACS-1234" in the
		// id field and a generic label like "Linear issue" in display.
		if linear.MightContainLinearRef(r.Display) || linear.MightContainLinearRef(r.ID) {
			return true
		}
	}
	return false
}

func linearReferenceText(refs []models.SessionInputReference) string {
	if len(refs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(refs)*3)
	for _, ref := range refs {
		if ref.Display != "" {
			parts = append(parts, ref.Display)
		}
		if ref.ID != "" && ref.ID != ref.Display {
			parts = append(parts, ref.ID)
		}
		if ref.Token != "" && ref.Token != ref.Display && ref.Token != ref.ID {
			parts = append(parts, ref.Token)
		}
	}
	return strings.Join(parts, "\n")
}

// canBypassMissingMessageForLinear is the same check as looksLikeLinearReference
// tightened with the team-key allowlist, so a malformed "FOO-123" can no
// longer wave the request past MISSING_MESSAGE. URL refs always pass — a
// linear.app URL is its own evidence and detection drops them via
// ErrCrossWorkspace if the workspace doesn't match. Bare identifiers must
// have their KEY prefix in the org's allowlist.
//
// Falls back to the regex-only hint when the linker isn't wired or the
// allowlist lookup errors — this preserves the prior behavior in tests
// that haven't installed the linker, and avoids hard-failing CreateManual
// on a transient DB hiccup.
func (h *SessionHandler) canBypassMissingMessageForLinear(ctx context.Context, orgID uuid.UUID, refs []models.SessionInputReference) bool {
	linker := h.getLinearLinker()
	if linker == nil {
		return looksLikeLinearReference(refs)
	}
	if !linker.Enabled(ctx, orgID) {
		return false
	}
	// URL hits short-circuit first: they don't depend on the allowlist and
	// detection treats them as authoritative provenance.
	for _, r := range refs {
		if linearURLPattern.MatchString(r.Display) || linearURLPattern.MatchString(r.ID) {
			return true
		}
	}
	allow, err := linker.TeamKeyAllowlist(ctx, orgID)
	if err != nil {
		// Allowlist lookup failed; fall back to the laxer regex hint rather
		// than hard-fail the request. Detection inside the linker re-checks
		// before any side effects.
		h.logger.Warn().Err(err).Str("org_id", orgID.String()).
			Msg("MISSING_MESSAGE bypass: failed to load Linear team-key allowlist; falling back to regex-only hint")
		return looksLikeLinearReference(refs)
	}
	for _, r := range refs {
		for _, candidate := range []string{r.Display, r.ID} {
			for _, m := range linearBareIdentifierAllPattern.FindAllStringSubmatch(candidate, -1) {
				if len(m) < 2 {
					continue
				}
				key := m[1]
				idx := strings.IndexByte(key, '-')
				if idx <= 0 {
					continue
				}
				if allow[key[:idx]] {
					return true
				}
			}
		}
	}
	return false
}

// linearURLPattern / linearBareIdentifierAllPattern mirror the patterns
// inside the linear package's detect.go but are re-declared here to keep
// the handler from importing internal regex state. The two regexes must
// stay in lockstep — a regression that drifts the handler-side regex
// would silently widen or narrow the MISSING_MESSAGE bypass.
var (
	linearURLPattern               = regexp.MustCompile(`https?://linear\.app/[^/\s]+/issue/[A-Z][A-Z0-9_]{0,9}-[0-9]+`)
	linearBareIdentifierAllPattern = regexp.MustCompile(`(?:^|[^A-Za-z0-9_/-])([A-Z][A-Z0-9_]{0,9}-[0-9]+)\b`)
)

func (h *SessionHandler) SetIssueLinkStore(store *db.SessionIssueLinkStore) {
	h.linkStore = store
}

// SetReviewCommentStore wires the review-comment store used by SendMessage to
// resolve comments inline with the message create transaction. Optional: if
// unset, requests with resolve_review_comment_ids are rejected with a 400.
func (h *SessionHandler) SetReviewCommentStore(store *db.SessionReviewCommentStore) {
	h.reviewCommentStore = store
}

// SetLinearLinker injects the Linear session-linking service. When unset,
// CreateManual treats Linear refs as opaque text — same behavior as when
// the org has no Linear integration. This is the design 62 §"Path C" no-op.
//
// Safe to call after construction even if the HTTP server is already
// serving requests: the atomic store lets a late-running test or a
// reload-without-restart wire the linker without racing the read path.
func (h *SessionHandler) SetLinearLinker(linker linearSessionLinker) {
	if linker == nil {
		h.linearLinker.Store(nil)
		return
	}
	h.linearLinker.Store(&linearLinkerHolder{fn: linker})
}

// getLinearLinker returns the currently-wired linker (or nil if none).
// Used by the read-path; tests should use this rather than reading
// h.linearLinker directly so the atomic load is visible.
func (h *SessionHandler) getLinearLinker() linearSessionLinker {
	holder := h.linearLinker.Load()
	if holder == nil {
		return nil
	}
	return holder.fn
}

func (h *SessionHandler) SetIssueSnapshotStore(store *db.SessionTurnIssueSnapshotStore) {
	h.issueSnapshots = store
}

func (h *SessionHandler) SetSlackSessionLinkStore(store *db.SlackSessionLinkStore) {
	h.slackSessionLinks = store
}

func (h *SessionHandler) enrichSessionLinks(ctx context.Context, orgID uuid.UUID, session *models.Session) {
	if session == nil || h.linkStore == nil {
		return
	}
	links, err := h.linkStore.ListBySession(ctx, orgID, session.ID)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to load session issue links")
		return
	}
	session.LinkedIssues = links
}

// SetAuditEmitter injects the audit emitter for logging session events.
func (h *SessionHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

func (h *SessionHandler) SetAttributionStore(store *db.SessionAttributionStore) {
	h.attributionStore = store
}

// SetCanceller injects the session canceller for stopping running agent sessions.
func (h *SessionHandler) SetCanceller(c SessionCanceller) {
	h.canceller = c
}

func (h *SessionHandler) SetWorkerRuntime(selector sessionWorkerSelector, client sessionWorkerCancelClient, localNodeID string) {
	h.workerSelector = selector
	h.workerClient = client
	h.localNodeID = localNodeID
}

func (h *SessionHandler) SetPRTitleSyncer(syncer sessionPRTitleSyncer) {
	h.prTitleSyncer = syncer
}

// SetViewStore injects the session view store for tracking unread sessions.
func (h *SessionHandler) SetViewStore(vs *db.SessionViewStore) {
	h.viewStore = vs
}

func (h *SessionHandler) SetStreams(streams *cache.SessionStreams) {
	h.streams = streams
}

func (h *SessionHandler) SetTxStarter(txStarter db.TxStarter) {
	h.txStarter = txStarter
}

// SetShutdownSignal wires a channel that is closed when the server is
// shutting down. SSE stream handlers listen on it so they return promptly
// during graceful shutdown instead of blocking Server.Shutdown until its
// deadline expires.
func (h *SessionHandler) SetShutdownSignal(ch <-chan struct{}) {
	h.shutdownCh = ch
}

// SetSnapshotStore injects the snapshot store so session archive can delete
// the associated sandbox snapshot file. Optional — if unset, archive still
// succeeds but leaves the snapshot to be reclaimed by the TTL reaper.
func (h *SessionHandler) SetSnapshotStore(s storage.SnapshotStore) {
	h.snapshotStore = s
}

// SetMembershipStore wires the membership store used by StreamLogs to
// authorize an explicit ?org_id= query param. Required for the SSE log stream
// to work for multi-org users (EventSource cannot send X-Active-Org-ID).
func (h *SessionHandler) SetMembershipStore(store sessionMembershipStore) {
	h.memberships = store
}

// SetPRCredentialStore injects the personal GitHub credential store used to
// decide whether CreatePR should intercept for authorship authorization.
func (h *SessionHandler) SetPRCredentialStore(store githubStatusCredentialStore) {
	h.prCredentials = store
}

// SetPRAuthCredentialChecker injects the refresh-aware GitHub user-auth checker
// used to determine whether Create PR should intercept for authorization.
func (h *SessionHandler) SetPRAuthCredentialChecker(checker interface {
	HasValidCredential(ctx context.Context, orgID, userID uuid.UUID) (bool, error)
}) {
	h.prAuthChecker = checker
}

// SetPRAuthFlow wires the signing key and frontend URL used by the on-demand
// Create PR GitHub-auth flow.
func (h *SessionHandler) SetPRAuthFlow(signingKey, frontendURL string) {
	h.prAuthSigningKey = []byte(signingKey)
	h.frontendURL = frontendURL
}

func (h *SessionHandler) SetHumanInputRequestStore(store *db.SessionHumanInputRequestStore) {
	h.humanInputStore = store
	if store == nil {
		h.humanInputService = nil
		return
	}
	h.humanInputService = humaninputsvc.NewDBService(
		h.runStore,
		store,
		h.questionStore,
		h.messageStore,
		h.threadStore,
		h.jobStore,
	)
	if h.capabilityService != nil {
		h.humanInputService.SetCapabilityApprover(sessionCapabilityApprover{svc: h.capabilityService})
	}
}

func (h *SessionHandler) SetCapabilityService(svc *agentcapabilities.Service) {
	h.capabilityService = svc
	if h.humanInputService != nil && svc != nil {
		h.humanInputService.SetCapabilityApprover(sessionCapabilityApprover{svc: svc})
	}
}

type sessionCapabilityApprover struct {
	svc *agentcapabilities.Service
}

func (a sessionCapabilityApprover) ApplyApprovedGrant(ctx context.Context, orgID, sessionID, requestID uuid.UUID, capability models.AgentCapabilityID, accessLevel models.AgentCapabilityAccessLevel) ([]models.AgentCapabilitySnapshotItem, error) {
	return a.svc.ApplyApprovedGrant(ctx, agentcapabilities.ApprovedGrantInput{
		OrgID:               orgID,
		SessionID:           sessionID,
		HumanInputRequestID: requestID,
		Capability:          capability,
		AccessLevel:         accessLevel,
	})
}

func (h *SessionHandler) SetThreadInboxStore(store *db.ThreadInboxStore) {
	h.threadInboxStore = store
}

func (h *SessionHandler) SetSessionSandboxHolderStore(store *db.SessionSandboxHolderStore) {
	h.sandboxHolders = store
}

func NewSessionHandler(
	runStore *db.SessionStore,
	logStore *db.SessionLogStore,
	questionStore *db.SessionQuestionStore,
	pullRequestStore *db.PullRequestStore,
	issueStore *db.IssueStore,
	repoStore *db.RepositoryStore,
	orgStore *db.OrganizationStore,
	jobStore *db.JobStore,
	messageStore *db.SessionMessageStore,
	threadStore *db.SessionThreadStore,
	llmClient llm.Client,
	logger zerolog.Logger,
) *SessionHandler {
	return &SessionHandler{
		runStore:         runStore,
		logStore:         logStore,
		questionStore:    questionStore,
		pullRequestStore: pullRequestStore,
		issueStore:       issueStore,
		repoStore:        repoStore,
		orgStore:         orgStore,
		jobStore:         jobStore,
		txStarter:        nil,
		messageStore:     messageStore,
		threadStore:      threadStore,
		llmClient:        llmClient,
		logger:           logger,
	}
}

func (h *SessionHandler) SetUserStore(store *db.UserStore) {
	h.userStore = store
}

func (h *SessionHandler) SetChangesetStore(store *db.SessionChangesetStore) {
	h.changesetStore = store
}

func (h *SessionHandler) primaryChangesetID(ctx context.Context, orgID, sessionID uuid.UUID) (uuid.UUID, error) {
	if h.changesetStore == nil {
		return sessionID, nil
	}
	changeset, err := h.changesetStore.GetPrimary(ctx, orgID, sessionID)
	if err != nil {
		return uuid.Nil, err
	}
	return changeset.ID, nil
}

// errInvalidChangesetID marks a malformed changeset_id query parameter so
// callers can distinguish a client error (400) from a store/DB failure (500).
var errInvalidChangesetID = errors.New("invalid changeset ID")

func (h *SessionHandler) requestedChangeset(ctx context.Context, orgID, sessionID uuid.UUID, raw string) (models.SessionChangeset, error) {
	if h.changesetStore == nil {
		return models.SessionChangeset{ID: sessionID, OrgID: orgID, SessionID: sessionID, IsPrimary: true}, nil
	}
	if strings.TrimSpace(raw) == "" {
		return h.changesetStore.GetPrimary(ctx, orgID, sessionID)
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return models.SessionChangeset{}, fmt.Errorf("%w: %v", errInvalidChangesetID, err)
	}
	return h.changesetStore.GetByID(ctx, orgID, sessionID, id)
}

type publishActionTxError struct {
	phase string
	err   error
}

func (e *publishActionTxError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.phase + ": " + e.err.Error()
}

func (e *publishActionTxError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (h *SessionHandler) enqueuePublishActionInTx(
	ctx context.Context,
	orgID uuid.UUID,
	sessionID uuid.UUID,
	queue string,
	jobType string,
	payload any,
	dedupeKey string,
	markQueued func(context.Context, *db.SessionStore, *db.SessionChangesetStore) (bool, error),
) (bool, error) {
	if h.txStarter == nil {
		return false, &publishActionTxError{phase: "begin", err: errors.New("transaction starter not configured")}
	}
	tx, err := h.txStarter.Begin(ctx)
	if err != nil {
		return false, &publishActionTxError{phase: "begin", err: err}
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txSessions := db.NewSessionStore(tx)
	txSessions.SetLogger(h.logger)
	queued, err := markQueued(ctx, txSessions, db.NewSessionChangesetStore(tx))
	if err != nil {
		return false, &publishActionTxError{phase: "state", err: err}
	}
	if !queued {
		return false, nil
	}

	jobID, err := h.jobStore.EnqueueInTx(ctx, tx, orgID, queue, jobType, payload, 5, &dedupeKey)
	if err != nil {
		return false, &publishActionTxError{phase: "enqueue", err: err}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, &publishActionTxError{phase: "commit", err: err}
	}
	h.jobStore.Notify(context.WithoutCancel(ctx), jobID)
	h.publishSessionStatusAfterCommit(ctx, orgID, sessionID)
	return true, nil
}

func (h *SessionHandler) publishSessionStatusAfterCommit(ctx context.Context, orgID, sessionID uuid.UUID) {
	if h.streams == nil {
		return
	}
	session, err := h.runStore.GetByID(context.WithoutCancel(ctx), orgID, sessionID)
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to reload session after queued publish action")
		return
	}
	if err := h.streams.PublishStatus(context.WithoutCancel(ctx), &session); err != nil {
		h.logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to publish queued session status")
	}
}

// encodeSessionCursor produces an opaque cursor from the last row's created_at and id.
func encodeSessionCursor(createdAt time.Time, id uuid.UUID) string {
	return encodeCursor(createdAt, id.String())
}

// decodeSessionCursor is the inverse of encodeSessionCursor.
func decodeSessionCursor(cursor string) (time.Time, uuid.UUID, error) {
	t, rawID, err := decodeCursor(cursor)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	id, err := uuid.Parse(rawID)
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("invalid cursor id: %w", err)
	}
	return t, id, nil
}

func (h *SessionHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	limit := queryInt(r, "limit", 50)
	filters := db.SessionFilters{
		Limit: limit,
	}

	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		t, id, err := decodeSessionCursor(cursor)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "invalid cursor")
			return
		}
		filters.CursorTime = &t
		filters.CursorID = &id
	}

	if r.URL.Query().Get("only_archived") == "true" {
		filters.OnlyArchived = true
	} else if r.URL.Query().Get("include_archived") == "true" {
		filters.IncludeArchived = true
	}

	if statusParam := r.URL.Query().Get("status"); statusParam != "" {
		for _, s := range strings.Split(statusParam, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			status := models.SessionStatus(s)
			if err := status.Validate(); err != nil {
				writeError(w, r, http.StatusBadRequest, "INVALID_STATUS", "invalid status: "+s)
				return
			}
			filters.Statuses = append(filters.Statuses, status)
		}
	}

	if search := r.URL.Query().Get("search"); search != "" {
		filters.Search = search
	}
	if raw := r.URL.Query().Get("created_after"); raw != "" {
		parsed, ok := parseOptionalRFC3339(w, r, &raw)
		if !ok {
			return
		}
		filters.CreatedAfter = parsed
	}
	if raw := r.URL.Query().Get("created_before"); raw != "" {
		parsed, ok := parseOptionalRFC3339(w, r, &raw)
		if !ok {
			return
		}
		filters.CreatedBefore = parsed
	}

	if repoIDStr := r.URL.Query().Get("repository_id"); repoIDStr != "" {
		repoID, err := uuid.Parse(repoIDStr)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", "invalid repository_id")
			return
		}
		filters.RepositoryID = repoID
	}

	if _, ok := r.URL.Query()["triggered_by_user_ids"]; ok {
		userIDsStr := r.URL.Query().Get("triggered_by_user_ids")
		userIDs, err := parseUUIDList(userIDsStr)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_USER_ID", "invalid triggered_by_user_ids")
			return
		}
		filters.TriggeredByUserIDs = userIDs
	} else if userIDStr := r.URL.Query().Get("triggered_by_user_id"); userIDStr != "" {
		userID, err := uuid.Parse(userIDStr)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_USER_ID", "invalid triggered_by_user_id")
			return
		}
		filters.TriggeredByUserID = userID
	}

	runs, err := h.runStore.ListByOrg(r.Context(), orgID, filters)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list runs", err)
		return
	}
	if runs == nil {
		runs = []models.Session{}
	}

	var nextCursor string
	if len(runs) > 0 && len(runs) == limit {
		last := runs[len(runs)-1]
		nextCursor = encodeSessionCursor(last.LastActivityAt, last.ID)
	}

	// Enrich sessions with last_viewed_at and PR summaries.
	items := make([]models.SessionListItem, len(runs))
	sessionIDs := make([]uuid.UUID, len(runs))
	for i, s := range runs {
		s.Diff = nil
		s.DiffHistory = nil
		h.enrichSessionLinks(r.Context(), orgID, &s)
		items[i] = models.SessionListItem{Session: s}
		sessionIDs[i] = s.ID
	}

	user := middleware.UserFromContext(r.Context())
	if user != nil && h.viewStore != nil && len(sessionIDs) > 0 {
		viewTimes, err := h.viewStore.BatchGetLastViewed(r.Context(), user.ID, sessionIDs)
		if err != nil {
			h.logger.Warn().Err(err).Msg("failed to fetch session view times")
		} else {
			for i, s := range runs {
				if t, ok := viewTimes[s.ID]; ok {
					items[i].LastViewedAt = &t
				}
			}
		}
	}

	if h.pullRequestStore != nil && len(sessionIDs) > 0 {
		prMap, err := h.pullRequestStore.BatchGetPrimaryBySessionIDs(r.Context(), orgID, sessionIDs)
		if err != nil {
			h.logger.Warn().Err(err).Msg("failed to fetch PR summaries")
		} else {
			for i, s := range runs {
				if pr, ok := prMap[s.ID]; ok {
					items[i].PRSummary = &models.PRSummary{
						Status:   pr.Status,
						CIStatus: pr.CIStatus,
						Number:   pr.GitHubPRNumber,
						URL:      pr.GitHubPRURL,
					}
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.SessionListItem]{
		Data: items,
		Meta: models.PaginationMeta{NextCursor: nextCursor},
	})
}

// Counts returns capped tab-badge counts for the sessions list. Bucket values
// that hit the cap indicate "at least cap" and should be rendered as e.g. 99+.
func (h *SessionHandler) Counts(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	filters := db.SessionCountsFilters{}

	if repoIDStr := r.URL.Query().Get("repository_id"); repoIDStr != "" {
		repoID, err := uuid.Parse(repoIDStr)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", "invalid repository_id")
			return
		}
		filters.RepositoryID = repoID
	}

	if _, ok := r.URL.Query()["triggered_by_user_ids"]; ok {
		userIDsStr := r.URL.Query().Get("triggered_by_user_ids")
		userIDs, err := parseUUIDList(userIDsStr)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_USER_ID", "invalid triggered_by_user_ids")
			return
		}
		filters.TriggeredByUserIDs = userIDs
	} else if userIDStr := r.URL.Query().Get("triggered_by_user_id"); userIDStr != "" {
		userID, err := uuid.Parse(userIDStr)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_USER_ID", "invalid triggered_by_user_id")
			return
		}
		filters.TriggeredByUserID = userID
	}

	counts, err := h.runStore.CountsByOrg(r.Context(), orgID, filters)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "COUNTS_FAILED", "failed to compute session counts", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.SessionCounts]{Data: counts})
}

func (h *SessionHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid run ID")
		return
	}

	run, err := h.runStore.GetAPIDetailByID(r.Context(), orgID, runID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "run not found")
		return
	}
	h.enrichSessionLinks(r.Context(), orgID, &run)

	detail := models.SessionDetail{Session: run}
	changesets, err := h.listChangesetSummaries(r.Context(), orgID, runID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CHANGESETS_FAILED", "failed to load pull requests", err)
		return
	}
	detail.Changesets = changesets
	if h.repoStore != nil && run.RepositoryID != nil {
		repo, err := h.repoStore.GetByID(r.Context(), orgID, *run.RepositoryID)
		if err != nil {
			zerolog.Ctx(r.Context()).Warn().Err(err).Str("session_id", runID.String()).Str("repository_id", run.RepositoryID.String()).Msg("failed to load repository for session detail")
		} else {
			detail.RepositoryFullName = &repo.FullName
		}
	}
	if h.threadStore != nil {
		threads, err := h.threadStore.ListBySession(r.Context(), orgID, runID)
		if err != nil {
			zerolog.Ctx(r.Context()).Warn().Err(err).Str("session_id", runID.String()).Msg("failed to load threads for session")
		}
		if threads == nil {
			threads = []models.SessionThread{}
		}
		if err := h.attachThreadInboxDeliverySummaries(r.Context(), orgID, runID, threads); err != nil {
			zerolog.Ctx(r.Context()).Warn().Err(err).Str("session_id", runID.String()).Msg("failed to load thread inbox delivery summaries for session")
		}
		detail.Threads = threads
	} else {
		detail.Threads = []models.SessionThread{}
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.SessionDetail]{Data: detail})
}

func (h *SessionHandler) listChangesetSummaries(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.ChangesetSummary, error) {
	if h.changesetStore == nil {
		return []models.ChangesetSummary{}, nil
	}
	changesets, err := h.changesetStore.ListBySession(ctx, orgID, sessionID)
	if err != nil {
		return nil, err
	}
	if changesets == nil {
		changesets = []models.ChangesetSummary{}
	}
	if h.pullRequestStore == nil || len(changesets) == 0 {
		return changesets, nil
	}
	prs, err := h.pullRequestStore.ListBySessionChangesets(ctx, orgID, sessionID)
	if err != nil {
		return nil, err
	}
	for i := range changesets {
		if pr, ok := prs[changesets[i].ID]; ok {
			changesets[i].PullRequest = &pr
		}
	}
	return changesets, nil
}

func (h *SessionHandler) ListChangesets(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	if _, err := h.runStore.GetAPIDetailByID(r.Context(), orgID, sessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "SESSION_LOOKUP_FAILED", "failed to load session", err)
		return
	}
	changesets, err := h.listChangesetSummaries(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CHANGESETS_FAILED", "failed to load pull requests", err)
		return
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.ChangesetSummary]{Data: changesets})
}

type createChangesetRequest struct {
	Title     string     `json:"title"`
	Summary   string     `json:"summary"`
	StackedOn *uuid.UUID `json:"stacked_on_changeset_id"`
}

func (h *SessionHandler) CreateChangeset(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	var req createChangesetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		writeError(w, r, http.StatusBadRequest, "TITLE_REQUIRED", "title is required")
		return
	}
	changeset, err := h.changesetStore.Create(r.Context(), orgID, sessionID, req.Title, strings.TrimSpace(req.Summary), req.StackedOn)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session or parent pull request not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "CHANGESET_CREATE_FAILED", "failed to create pull request", err)
		return
	}
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.ChangesetSummary]{Data: changeset.SummaryView()})
}

type updateChangesetRequest struct {
	Title   *string `json:"title"`
	Summary *string `json:"summary"`
}

func (h *SessionHandler) UpdateChangeset(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	changesetID, err := uuid.Parse(chi.URLParam(r, "changeset_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_CHANGESET_ID", "invalid changeset ID")
		return
	}
	var req updateChangesetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	if req.Title == nil && req.Summary == nil {
		writeError(w, r, http.StatusBadRequest, "NO_CHANGES", "provide a title or summary")
		return
	}
	if req.Title != nil {
		trimmed := strings.TrimSpace(*req.Title)
		if trimmed == "" {
			writeError(w, r, http.StatusBadRequest, "TITLE_REQUIRED", "title cannot be empty")
			return
		}
		req.Title = &trimmed
	}
	if req.Summary != nil {
		trimmed := strings.TrimSpace(*req.Summary)
		req.Summary = &trimmed
	}
	changeset, err := h.changesetStore.UpdateMetadata(r.Context(), orgID, sessionID, changesetID, req.Title, req.Summary)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "pull request not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "CHANGESET_UPDATE_FAILED", "failed to update pull request", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.ChangesetSummary]{Data: changeset.SummaryView()})
}

func (h *SessionHandler) attachThreadInboxDeliverySummaries(ctx context.Context, orgID, sessionID uuid.UUID, threads []models.SessionThread) error {
	if h.threadInboxStore == nil || len(threads) == 0 {
		return nil
	}
	summaries, err := h.threadInboxStore.ListDeliverySummariesBySession(ctx, orgID, sessionID)
	if err != nil {
		return fmt.Errorf("list thread inbox delivery summaries: %w", err)
	}
	for i := range threads {
		summary, ok := summaries[threads[i].ID]
		if !ok {
			summary = models.ThreadInboxDeliverySummary{ThreadID: threads[i].ID}
			summary.Normalize()
		}
		threads[i].InboxDelivery = &summary
	}
	return nil
}

func (h *SessionHandler) requireSnapshotQuiescent(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, session models.Session, action string) bool {
	if session.PendingSnapshotKey != nil && *session.PendingSnapshotKey != "" {
		writeError(w, r, http.StatusConflict, "SNAPSHOT_PENDING", "a snapshot upload is still finishing; try again in a moment")
		return false
	}
	if session.Status == models.SessionStatusRunning {
		writeError(w, r, http.StatusConflict, "SESSION_RUNNING", "wait for the session to finish before "+action)
		return false
	}
	if h.sandboxHolders == nil {
		return true
	}
	active, err := h.sandboxHolders.CountActiveThreadRuntimesBySession(r.Context(), orgID, session.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "QUIESCENCE_CHECK_FAILED", "failed to check active thread runtimes", err)
		return false
	}
	if active > 0 {
		writeError(w, r, http.StatusConflict, "SNAPSHOT_NOT_QUIESCENT", "wait for active tabs to finish before "+action)
		return false
	}
	return true
}

func (h *SessionHandler) GetDiff(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	payload, err := h.runStore.GetDiffByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session diff not found")
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.SessionDiff]{Data: payload})
}

func (h *SessionHandler) Update(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	var body struct {
		Title *string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	if body.Title == nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "title is required")
		return
	}

	title, ok := services.CleanTitle(*body.Title)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "INVALID_TITLE", "title must be between 1 and 120 characters")
		return
	}

	session, err := h.runStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}
	if session.Title != nil && *session.Title == title {
		writeJSON(w, http.StatusOK, models.SingleResponse[models.Session]{Data: session})
		return
	}
	priorTitleState, titleStateErr := h.runStore.GetTitleState(r.Context(), orgID, sessionID)
	if titleStateErr != nil {
		zerolog.Ctx(r.Context()).Warn().Err(titleStateErr).Str("session_id", sessionID.String()).Msg("failed to read title provenance before manual rename")
	}

	if err := h.runStore.UpdateTitle(r.Context(), orgID, sessionID, title); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update session title", err)
		return
	}
	session.Title = &title
	if titleStateErr == nil && priorTitleState.TitleSource == models.SessionTitleSourceGenerated && priorTitleState.TitleGeneratedAt != nil {
		age := time.Since(*priorTitleState.TitleGeneratedAt)
		if age >= 0 && age <= 24*time.Hour {
			metrics.RecordSessionTitleDecision(r.Context(), string(priorTitleState.TitleSource), "manual_override_within_24h")
			zerolog.Ctx(r.Context()).Debug().
				Str("session_id", sessionID.String()).
				Str("title_action", "manual_override_within_24h").
				Str("title_source", string(priorTitleState.TitleSource)).
				Msg("manually replaced generated session title")
		}
	}

	if h.prTitleSyncer != nil {
		if err := h.prTitleSyncer.SyncSessionTitle(r.Context(), &session); err != nil {
			zerolog.Ctx(r.Context()).Warn().
				Err(err).
				Str("session_id", sessionID.String()).
				Msg("failed to sync PR title after session title update")
		}
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.Session]{Data: session})
}

// RegenerateTitle explicitly replaces any title provenance with a generated
// title based on the original primary-thread request. Unlike background pivot
// detection, this user-initiated action may replace manual or issue titles.
func (h *SessionHandler) RegenerateTitle(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	if h.llmClient == nil {
		writeError(w, r, http.StatusServiceUnavailable, "TITLE_GENERATION_UNAVAILABLE", "title generation is unavailable")
		return
	}
	session, err := h.runStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}

	var primaryThreadID *uuid.UUID
	if h.threadStore != nil {
		threads, listErr := h.threadStore.ListBySession(r.Context(), orgID, sessionID)
		if listErr != nil {
			writeError(w, r, http.StatusInternalServerError, "TITLE_CONTEXT_FAILED", "failed to load title context", listErr)
			return
		}
		if len(threads) > 0 {
			primaryThreadID = &threads[0].ID
		}
	}
	contextMessages, err := h.messageStore.ListTitleContext(r.Context(), orgID, sessionID, primaryThreadID, -1, 1)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "TITLE_CONTEXT_FAILED", "failed to load title context", err)
		return
	}
	if len(contextMessages) == 0 {
		writeError(w, r, http.StatusConflict, "TITLE_CONTEXT_MISSING", "session has no original request to title")
		return
	}
	if err := h.generateSessionTitle(r.Context(), &session, orgID, contextMessages[0].Content, models.SessionTitleSourceGenerated); err != nil {
		writeError(w, r, http.StatusInternalServerError, "TITLE_GENERATION_FAILED", "failed to generate session title", err)
		return
	}
	metrics.RecordSessionTitleDecision(r.Context(), string(models.SessionTitleSourceGenerated), "explicit_regeneration")

	if h.prTitleSyncer != nil {
		if err := h.prTitleSyncer.SyncSessionTitle(r.Context(), &session); err != nil {
			zerolog.Ctx(r.Context()).Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to sync PR title after explicit regeneration")
		}
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Session]{Data: session})
}

// RecordView records that the current user has viewed a session (for unread tracking).
func (h *SessionHandler) RecordView(w http.ResponseWriter, r *http.Request) {
	if h.viewStore == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user not found in context")
		return
	}

	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	if err := h.viewStore.Upsert(r.Context(), user.ID, sessionID, orgID); err != nil {
		h.logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to record session view")
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// TriggerFix creates a new agent run for an issue and enqueues a run_agent job.
func (h *SessionHandler) TriggerFix(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	issueID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid issue ID")
		return
	}

	// Verify the issue exists.
	issue, err := h.issueStore.GetByID(r.Context(), orgID, issueID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "issue not found")
		return
	}

	// Parse optional overrides from the request body.
	var body struct {
		AgentType     string `json:"agent_type"`
		AutonomyLevel string `json:"autonomy_level"`
		TokenMode     string `json:"token_mode"`
		Message       string `json:"message"`
		// Force allows an admin to kick off a session even when the autopilot
		// state is blocked, failed, or skipped. Rejected for non-admins.
		Force bool `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body", err)
		return
	}
	if body.Force && middleware.ActiveRoleFromContext(r.Context()) != string(models.RoleAdmin) {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "admin role required to force-start a session")
		return
	}
	const maxMessageLength = 10000
	if utf8.RuneCountInString(body.Message) > maxMessageLength {
		writeError(w, r, http.StatusBadRequest, "MESSAGE_TOO_LONG", fmt.Sprintf("message must not exceed %d characters", maxMessageLength))
		return
	}

	agentType := models.AgentType(body.AgentType)
	if agentType == "" {
		org, err := h.orgStore.GetByID(r.Context(), orgID)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "DEFAULT_AGENT_LOOKUP_FAILED", "failed to load organization settings", err)
			return
		}
		orgSettings, parseErr := models.ParseOrgSettings(org.Settings)
		if parseErr != nil {
			zerolog.Ctx(r.Context()).Warn().Err(parseErr).Msg("failed to parse org settings, using defaults")
		}
		agentType = orgSettings.DefaultAgentType
		if agentType == "" {
			agentType = models.DefaultDefaultAgentType
		}
	}
	if err := agentType.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_AGENT_TYPE", err.Error())
		return
	}

	autonomyLevel := body.AutonomyLevel
	if autonomyLevel == "" {
		autonomyLevel = string(models.DefaultSessionAutonomy)
	}
	if err := models.SessionAutonomy(autonomyLevel).Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_AUTONOMY_LEVEL", "autonomy_level must be one of: full, semi, supervised")
		return
	}

	tokenMode := body.TokenMode
	if tokenMode == "" {
		tokenMode = "low"
	}
	validTokenModes := map[string]bool{"low": true, "high": true}
	if !validTokenModes[tokenMode] {
		writeError(w, r, http.StatusBadRequest, "INVALID_TOKEN_MODE", "token_mode must be one of: low, high")
		return
	}
	if issue.RepositoryID == nil {
		writeError(w, r, http.StatusBadRequest, "REPOSITORY_REQUIRED", "issue must be attached to a repository before starting a session")
		return
	}

	var triggeredByUserID *uuid.UUID
	if user := middleware.UserFromContext(r.Context()); user != nil {
		triggeredByUserID = &user.ID
	}

	run := &models.Session{
		PrimaryIssueID:    &issueID,
		OrgID:             orgID,
		Origin:            models.SessionOriginIssueTrigger,
		InteractionMode:   models.SessionInteractionModeSingleRun,
		ValidationPolicy:  models.SessionValidationPolicyOnTurnComplete,
		AgentType:         agentType,
		Status:            models.SessionStatusPending,
		AutonomyLevel:     models.SessionAutonomy(autonomyLevel),
		TokenMode:         models.SessionTokenMode(tokenMode),
		TriggeredByUserID: triggeredByUserID,
		RepositoryID:      issue.RepositoryID,
	}
	if err := h.runStore.Create(r.Context(), run); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create agent run", err)
		return
	}

	if initialMessage := strings.TrimSpace(body.Message); h.messageStore != nil && initialMessage != "" {
		msg := &models.SessionMessage{
			SessionID:  run.ID,
			OrgID:      orgID,
			ThreadID:   run.PrimaryThreadID,
			TurnNumber: 0,
			Role:       models.MessageRoleUser,
			Content:    initialMessage,
		}
		if user := middleware.UserFromContext(r.Context()); user != nil {
			msg.UserID = &user.ID
		}
		if err := h.messageStore.Create(r.Context(), msg); err != nil {
			zerolog.Ctx(r.Context()).Warn().Err(err).Str("session_id", run.ID.String()).Msg("failed to persist initial session message; session will proceed without it")
		}
	}

	// Generate a title from the issue for non-manual sessions.
	if h.llmClient != nil {
		titleInput := issue.Title
		if issue.Description != nil && len(*issue.Description) > 0 {
			desc := *issue.Description
			if len(desc) > 500 {
				desc = desc[:500] + "..."
			}
			titleInput += "\n\n" + desc
		}
		if err := h.generateSessionTitle(r.Context(), run, orgID, titleInput, models.SessionTitleSourceIssue); err != nil {
			zerolog.Ctx(r.Context()).Warn().Err(err).Msg("failed to generate title for issue session")
		}
	}

	// Enqueue the run_agent job. Dedupe by session ID so a double-clicked
	// submit (or any other transient retry path) cannot land two run_agent
	// rows for the same session — the second insert would race the first at
	// AcquireTurnHold and surface "sandbox race" to the user.
	dedupeKey := db.RunAgentDedupeKey(run.ID)
	payload := db.RunAgentPayload(run)
	if _, err := h.jobStore.Enqueue(r.Context(), orgID, "agent", "run_agent", payload, 5, &dedupeKey); err != nil {
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue agent run job", err)
		return
	}

	sessionIDStr := run.ID.String()
	h.enrichSessionLinks(r.Context(), orgID, run)
	emitUserAuditWithSession(
		h.audit,
		r,
		models.AuditActionSessionCreated,
		models.AuditResourceSession,
		&sessionIDStr,
		&run.ID,
		nil,
		sessionCreateAuditDetails(h.logger, run, &issue, nil),
	)
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.Session]{Data: *run})
}

const retryCheckpointTranscriptNote = "Retrying from the latest saved progress."

// RetrySession retries a failed session. The default mode resumes from the
// latest durable checkpoint; start_over preserves the old destructive rerun
// path for explicit user selection.
func (h *SessionHandler) RetrySession(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	mode, err := parseRetrySessionMode(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_RETRY_MODE", "invalid retry mode", err)
		return
	}

	switch mode {
	case models.SessionRetryModeCheckpoint:
		h.retrySessionFromCheckpoint(w, r, orgID, sessionID)
	case models.SessionRetryModeStartOver:
		h.retrySessionStartOver(w, r, orgID, sessionID)
	default:
		writeError(w, r, http.StatusBadRequest, "INVALID_RETRY_MODE", "invalid retry mode")
	}
}

func parseRetrySessionMode(r *http.Request) (models.SessionRetryMode, error) {
	var req models.RetrySessionRequest
	if r.Body != nil {
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
	}
	if req.Mode == "" {
		req.Mode = models.SessionRetryModeCheckpoint
	}
	if err := req.Mode.Validate(); err != nil {
		return "", err
	}
	return req.Mode, nil
}

func (h *SessionHandler) retrySessionStartOver(w http.ResponseWriter, r *http.Request, orgID, sessionID uuid.UUID) {
	if err := h.runStore.ResetForRetry(r.Context(), orgID, sessionID); err != nil {
		if errors.Is(err, db.ErrSessionNotFound) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		} else if errors.Is(err, db.ErrSessionNotFailed) {
			writeError(w, r, http.StatusConflict, "NOT_FAILED", "session is not in failed status")
		} else {
			writeError(w, r, http.StatusInternalServerError, "RETRY_FAILED", "failed to reset session for retry", err)
		}
		return
	}

	// Re-enqueue the run_agent job. If this fails, roll back the session status
	// so it doesn't get stuck in pending with no job to pick it up. Dedupe by
	// session ID so a Retry can never land alongside an in-flight run_agent
	// for the same session — the partial unique index on (queue, dedupe_key)
	// only covers pending|running, so legitimate retries after the prior job
	// reaches a terminal state still go through.
	dedupeKey := db.RunAgentDedupeKey(sessionID)
	payload := map[string]string{
		"session_id": sessionID.String(),
		"org_id":     orgID.String(),
	}
	if _, err := h.jobStore.Enqueue(r.Context(), orgID, "agent", "run_agent", payload, 5, &dedupeKey); err != nil {
		if undoErr := h.runStore.UndoResetForRetry(r.Context(), orgID, sessionID, "Retry failed: could not enqueue job", ""); undoErr != nil {
			h.logger.Error().Err(undoErr).Str("session_id", sessionID.String()).Msg("failed to undo retry reset after enqueue failure")
		}
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue agent run job", err)
		return
	}

	// Fetch the updated session to return.
	session, err := h.runStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "FETCH_FAILED", "failed to fetch updated session", err)
		return
	}
	h.enrichSessionLinks(r.Context(), orgID, &session)

	sessionIDStr := sessionID.String()
	retryDetails := sessionAuditSnapshot(&session, nil, map[string]any{
		"job_type":   "run_agent",
		"retry_mode": string(models.SessionRetryModeStartOver),
		"changes": map[string]any{
			"status": auditChange("failed", session.Status),
		},
	})
	emitUserAuditWithSession(h.audit, r, models.AuditActionSessionRetried, models.AuditResourceSession, &sessionIDStr, &sessionID, nil,
		marshalAuditDetails(h.logger, retryDetails))
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Session]{Data: session})
}

func (h *SessionHandler) retrySessionFromCheckpoint(w http.ResponseWriter, r *http.Request, orgID, sessionID uuid.UUID) {
	session, err := h.runStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, db.ErrSessionNotFound) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "FETCH_FAILED", "failed to fetch session", err)
		return
	}
	if session.Status != models.SessionStatusFailed {
		writeError(w, r, http.StatusConflict, "NOT_FAILED", "session is not in failed status")
		return
	}
	if session.SnapshotKey == nil || strings.TrimSpace(*session.SnapshotKey) == "" || session.SandboxState == models.SandboxStateDestroyed {
		writeError(w, r, http.StatusConflict, "CHECKPOINT_UNAVAILABLE", "No saved progress is available.")
		return
	}
	if session.PendingSnapshotKey != nil && strings.TrimSpace(*session.PendingSnapshotKey) != "" {
		writeError(w, r, http.StatusConflict, "CHECKPOINT_PENDING", "checkpoint upload is still pending")
		return
	}

	targetThread, err := h.threadStore.GetRetryTarget(r.Context(), orgID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusConflict, "NO_RETRY_THREAD", "no visible retry thread is available")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "THREAD_LOOKUP_FAILED", "failed to find retry thread", err)
		return
	}

	claimedSession, err := h.runStore.ClaimForResume(r.Context(), orgID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusConflict, "CHECKPOINT_UNAVAILABLE", "No saved progress is available.")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "RETRY_FAILED", "failed to claim session for checkpoint retry", err)
		return
	}

	claimedThread, err := h.claimRetryThread(r.Context(), orgID, sessionID, targetThread)
	if err != nil {
		h.revertCheckpointRetry(r.Context(), orgID, sessionID, uuid.Nil, models.ThreadStatus(""))
		if errors.Is(err, db.ErrThreadRunningLimitReached) || errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusConflict, "THREAD_NOT_RETRYABLE", "retry thread is not available")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "THREAD_CLAIM_FAILED", "failed to claim retry thread", err)
		return
	}

	messageThreadID := claimedThread.ID
	note := models.SessionMessage{
		SessionID:  sessionID,
		OrgID:      orgID,
		ThreadID:   &messageThreadID,
		TurnNumber: claimedThread.CurrentTurn + 1,
		Role:       models.MessageRoleAssistant,
		Content:    retryCheckpointTranscriptNote,
	}
	if err := h.messageStore.Create(r.Context(), &note); err != nil {
		h.revertCheckpointRetry(r.Context(), orgID, sessionID, claimedThread.ID, targetThread.Status)
		writeError(w, r, http.StatusInternalServerError, "MESSAGE_FAILED", "failed to add retry transcript note", err)
		return
	}

	dedupeKey := db.ContinueSessionDedupeKey(claimedThread.ID)
	payload := map[string]string{
		"session_id": sessionID.String(),
		"thread_id":  claimedThread.ID.String(),
		"org_id":     orgID.String(),
	}
	if _, err := h.jobStore.EnqueueWithTarget(r.Context(), orgID, "agent", "continue_session", payload, 5, &dedupeKey, models.SessionWorkerTarget(&claimedSession)); err != nil {
		h.revertCheckpointRetry(r.Context(), orgID, sessionID, claimedThread.ID, targetThread.Status)
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue session continuation job", err)
		return
	}

	h.enrichSessionLinks(r.Context(), orgID, &claimedSession)
	sessionIDStr := sessionID.String()
	retryDetails := sessionAuditSnapshot(&claimedSession, nil, map[string]any{
		"job_type":   "continue_session",
		"retry_mode": string(models.SessionRetryModeCheckpoint),
		"thread_id":  claimedThread.ID.String(),
		"changes": map[string]any{
			"status": auditChange(session.Status, claimedSession.Status),
		},
	})
	emitUserAuditWithSession(h.audit, r, models.AuditActionSessionRetried, models.AuditResourceSession, &sessionIDStr, &sessionID, nil,
		marshalAuditDetails(h.logger, retryDetails))
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Session]{Data: claimedSession})
}

func (h *SessionHandler) claimRetryThread(ctx context.Context, orgID, sessionID uuid.UUID, targetThread models.SessionThread) (models.SessionThread, error) {
	if targetThread.Status == models.ThreadStatusIdle {
		return h.threadStore.ClaimIdleForSession(ctx, orgID, sessionID, targetThread.ID, models.MaxRunningThreadsPerSession)
	}
	if retryThreadStatusResumable(targetThread.Status) {
		return h.threadStore.ClaimForResumeInSession(ctx, orgID, sessionID, targetThread.ID, models.MaxRunningThreadsPerSession)
	}
	return models.SessionThread{}, pgx.ErrNoRows
}

func retryThreadStatusResumable(status models.ThreadStatus) bool {
	for _, resumable := range models.ResumableThreadStatuses {
		if status == resumable {
			return true
		}
	}
	return false
}

func (h *SessionHandler) revertCheckpointRetry(ctx context.Context, orgID, sessionID, threadID uuid.UUID, previousThreadStatus models.ThreadStatus) {
	if err := h.runStore.UpdateStatus(ctx, orgID, sessionID, models.SessionStatusFailed); err != nil {
		h.logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to revert checkpoint retry session status")
	}
	if threadID != uuid.Nil && previousThreadStatus != "" {
		if err := h.threadStore.UpdateStatus(ctx, orgID, threadID, previousThreadStatus); err != nil {
			h.logger.Warn().Err(err).Str("thread_id", threadID.String()).Msg("failed to revert checkpoint retry thread status")
		}
	}
}

// GetLogs returns all logs for a run as a JSON array.
// This is the primary endpoint for viewing historical logs for completed runs
// and also serves as the initial log fetch for active runs.
func (h *SessionHandler) GetLogs(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	h.writeLogsForOrg(w, r, orgID)
}

var (
	errSessionStreamOrgInvalid   = errors.New("invalid session stream org")
	errSessionStreamOrgForbidden = errors.New("forbidden session stream org")
	errSessionStreamUnauthorized = errors.New("unauthorized session stream request")
)

// streamOrgID resolves the org for a SSE log stream request, honouring an
// optional ?org_id= query param when the X-Active-Org-ID header is absent
// (EventSource has no header API). The query value is accepted only when the
// requesting user holds a membership in that org. Falls back to the auth
// middleware's resolved active org when no query param is supplied — that
// preserves the existing fetch-based behaviour for callers that *can* set the
// header. See pull_requests.go's streamOrgIDFromRequest for the prior art.
func (h *SessionHandler) streamOrgID(r *http.Request) (uuid.UUID, error) {
	orgID := middleware.OrgIDFromContext(r.Context())
	requestedRaw := strings.TrimSpace(r.URL.Query().Get("org_id"))
	if requestedRaw == "" {
		return orgID, nil
	}

	requestedOrgID, err := uuid.Parse(requestedRaw)
	if err != nil {
		return uuid.Nil, errSessionStreamOrgInvalid
	}
	if requestedOrgID == orgID {
		return requestedOrgID, nil
	}

	user := middleware.UserFromContext(r.Context())
	if user == nil {
		return uuid.Nil, errSessionStreamUnauthorized
	}
	if h.memberships == nil {
		return uuid.Nil, errors.New("membership store not configured")
	}
	if _, err := h.memberships.Get(r.Context(), user.ID, requestedOrgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, errSessionStreamOrgForbidden
		}
		return uuid.Nil, err
	}
	return requestedOrgID, nil
}

// writeLogsForOrg writes the JSON log list for the given org. Split out so
// StreamLogs can delegate to it with the org resolved by streamOrgID (which
// honours the ?org_id= query fallback when the X-Active-Org-ID header is
// missing).
func (h *SessionHandler) writeLogsForOrg(w http.ResponseWriter, r *http.Request, orgID uuid.UUID) {
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid run ID")
		return
	}

	// Verify run exists and belongs to org.
	_, err = h.runStore.GetByID(r.Context(), orgID, runID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "run not found")
		return
	}

	logs, err := h.logStore.ListByRunID(r.Context(), orgID, runID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list logs", err)
		return
	}
	if logs == nil {
		logs = []models.SessionLog{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.SessionLogResponse]{
		Data: models.NewSessionLogResponses(logs),
	})
}

// GetLogDetail returns the full message for one session log. Historical log
// list and timeline endpoints return previews, so expanded UI rows call this
// endpoint only when full detail is needed.
func (h *SessionHandler) GetLogDetail(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	logID, err := strconv.ParseInt(chi.URLParam(r, "log_id"), 10, 64)
	if err != nil || logID <= 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid log ID")
		return
	}

	if _, err := h.runStore.GetByID(r.Context(), orgID, sessionID); err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}

	log, err := h.logStore.GetByID(r.Context(), orgID, sessionID, logID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "log not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "GET_FAILED", "failed to get log", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.SessionLogDetailResponse]{
		Data: models.NewSessionLogDetailResponse(log),
	})
}

// StreamLogs streams agent run logs as Server-Sent Events.
func (h *SessionHandler) StreamLogs(w http.ResponseWriter, r *http.Request) {
	// EventSource cannot send X-Active-Org-ID, so accept ?org_id= as a
	// fallback. Validates the requesting user has membership in the org —
	// see streamOrgID for details. Without this, multi-org users whose
	// session-hint last_org_id != their actively-viewed org would 404.
	orgID, err := h.streamOrgID(r)
	if err != nil {
		switch {
		case errors.Is(err, errSessionStreamOrgInvalid):
			writeError(w, r, http.StatusBadRequest, "INVALID_ORG", "invalid org_id")
		case errors.Is(err, errSessionStreamOrgForbidden):
			writeError(w, r, http.StatusForbidden, "FORBIDDEN", "access denied")
		case errors.Is(err, errSessionStreamUnauthorized):
			writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "missing user")
		default:
			writeError(w, r, http.StatusInternalServerError, "INTERNAL", "failed to authorize log stream", err)
		}
		return
	}
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid run ID")
		return
	}

	// Verify run exists.
	run, err := h.runStore.GetByID(r.Context(), orgID, runID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "run not found")
		return
	}

	// For terminal runs, return existing logs as JSON instead of SSE
	// since there will be no new logs to stream. Pass orgID explicitly so
	// the resolved org from streamOrgID flows through (GetLogs reads it
	// from context, which may not match for header-less SSE callers).
	if isTerminalStatus(run.Status) {
		h.writeLogsForOrg(w, r, orgID)
		return
	}

	sw := sse.NewWriter(w)
	if sw == nil {
		writeError(w, r, http.StatusInternalServerError, "SSE_NOT_SUPPORTED", "streaming not supported")
		return
	}

	lastEventID := r.Header.Get("Last-Event-ID")
	if lastEventID == "" {
		lastEventID = r.URL.Query().Get("last_event_id")
	}

	if h.streams != nil && h.streams.Available() {
		if h.streamLogsViaRedis(r.Context(), sw, orgID, run, lastEventID) {
			return
		}
		zerolog.Ctx(r.Context()).Warn().Str("session_id", runID.String()).Msg("Redis stream path unavailable, falling back to Postgres polling")
	}

	h.streamLogsViaPolling(r.Context(), sw, orgID, run, lastEventID)
}

func (h *SessionHandler) streamLogsViaPolling(ctx context.Context, sw *sse.Writer, orgID uuid.UUID, run models.Session, lastEventID string) {
	logger := zerolog.Ctx(ctx)
	var (
		logs       []models.SessionLog
		err        error
		lastSeenID int64
	)
	if lastEventID == "" {
		logs, err = h.logStore.ListByRunID(ctx, orgID, run.ID)
	} else {
		lastSeenID, err = cache.ParseLogStreamID(lastEventID)
		if err != nil {
			logs, err = h.logStore.ListByRunID(ctx, orgID, run.ID)
			lastSeenID = 0
		} else {
			logs, err = h.logStore.ListByRunIDSince(ctx, orgID, run.ID, lastSeenID)
		}
	}
	if err != nil {
		logger.Error().Err(err).Str("session_id", run.ID.String()).Msg("failed to load initial logs for SSE polling stream")
		return
	}
	for _, log := range logs {
		if err := writeSessionLogSSEEvent(sw, log); err != nil {
			logger.Error().Err(err).Str("session_id", run.ID.String()).Msg("failed to write log event to SSE stream")
			return
		}
		lastSeenID = log.ID
	}

	lastStatus := run.Status
	if err := sw.WriteEvent(sse.EventStatus, h.sessionStatusPayload(ctx, orgID, run)); err != nil {
		logger.Error().Err(err).Str("session_id", run.ID.String()).Msg("failed to write initial status event to SSE stream")
		return
	}
	sw.Flush()

	// Per-connection randomized intervals — see the comment block above
	// sseFallbackPollMin for why this isn't a fixed 1s anymore.
	pollInterval := h.sseFallbackPollInterval()
	heartbeatInterval := h.sseFallbackHeartbeatInterval()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	shutdownCh := h.shutdownCh

	for {
		select {
		case <-ctx.Done():
			return
		case <-shutdownCh:
			if err := sw.WriteHeartbeat(); err != nil {
				logger.Warn().Err(err).Str("session_id", run.ID.String()).Msg("failed to write shutdown heartbeat to SSE polling stream")
			}
			sw.Flush()
			return
		case <-heartbeat.C:
			if err := sw.WriteHeartbeat(); err != nil {
				logger.Warn().Err(err).Str("session_id", run.ID.String()).Msg("failed to write heartbeat to SSE polling stream")
				return
			}
			sw.Flush()
		case <-ticker.C:
			run, err := h.runStore.GetByID(ctx, orgID, run.ID)
			if err != nil {
				logger.Error().Err(err).Str("session_id", run.ID.String()).Msg("failed to reload session for SSE polling stream")
				return
			}

			newLogs, err := h.logStore.ListByRunIDSince(ctx, orgID, run.ID, lastSeenID)
			if err != nil {
				logger.Error().Err(err).Str("session_id", run.ID.String()).Int64("last_seen_id", lastSeenID).Msg("failed to load incremental logs for SSE polling stream")
				return
			}
			for _, log := range newLogs {
				if err := writeSessionLogSSEEvent(sw, log); err != nil {
					logger.Error().Err(err).Str("session_id", run.ID.String()).Msg("failed to write log event to SSE stream")
					return
				}
				lastSeenID = log.ID
			}

			// Send a status event whenever the session status changes.
			if run.Status != lastStatus {
				lastStatus = run.Status
				statusPayload := h.sessionStatusPayload(ctx, orgID, run)
				if err := sw.WriteEvent(sse.EventStatus, statusPayload); err != nil {
					logger.Error().Err(err).Str("session_id", run.ID.String()).Msg("failed to write status event to SSE stream")
					return
				}
			}

			sw.Flush()

			if isTerminalStatus(run.Status) {
				if err := sw.WriteEvent(sse.EventDone, h.sessionStatusPayload(ctx, orgID, run)); err != nil {
					logger.Error().Err(err).Str("session_id", run.ID.String()).Msg("failed to write done event to SSE stream")
					return
				}
				sw.Flush()
				return
			}
		}
	}
}

func (h *SessionHandler) streamLogsViaRedis(ctx context.Context, sw *sse.Writer, orgID uuid.UUID, run models.Session, lastEventID string) bool {
	logger := zerolog.Ctx(ctx)
	logSub, err := h.streams.SubscribeLogs(run.ID)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", run.ID.String()).Msg("failed to subscribe to Redis session log stream")
		return false
	}
	defer logSub.Close()

	statusSub, err := h.streams.SubscribeStatus(run.ID)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", run.ID.String()).Msg("failed to subscribe to Redis session status stream")
		return false
	}
	defer statusSub.Close()

	eventSub, err := h.streams.SubscribeEvents(run.ID)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", run.ID.String()).Msg("failed to subscribe to Redis session event stream")
		return false
	}
	defer eventSub.Close()

	logs, err := h.catchUpLogs(ctx, orgID, run.ID, lastEventID)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", run.ID.String()).Str("last_event_id", lastEventID).Msg("failed to catch up logs from Redis-backed stream")
		return false
	}
	lastDeliveredStreamID := lastEventID
	for _, log := range logs {
		streamID := cache.SessionLogStreamID(log.ID)
		if err := writeSessionLogSSEEventWithID(sw, streamID, log); err != nil {
			logger.Error().Err(err).Str("session_id", run.ID.String()).Str("stream_id", streamID).Msg("failed to write replayed log event to SSE stream")
			return false
		}
		lastDeliveredStreamID = streamID
	}
	if err := sw.WriteEvent(sse.EventStatus, h.sessionStatusPayload(ctx, orgID, run)); err != nil {
		logger.Error().Err(err).Str("session_id", run.ID.String()).Msg("failed to write initial status event to Redis-backed SSE stream")
		return false
	}
	sw.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return true
		case <-h.shutdownCh:
			if err := sw.WriteHeartbeat(); err != nil {
				logger.Warn().Err(err).Str("session_id", run.ID.String()).Msg("failed to write shutdown heartbeat to Redis-backed SSE stream")
			}
			sw.Flush()
			return true
		case <-heartbeat.C:
			if err := sw.WriteHeartbeat(); err != nil {
				logger.Warn().Err(err).Str("session_id", run.ID.String()).Msg("failed to write heartbeat to Redis-backed SSE stream")
				return true
			}
			sw.Flush()
		case logEntry, ok := <-logSub.C:
			if !ok {
				closeReason := logSub.CloseReason()
				logger.Warn().Str("session_id", run.ID.String()).Str("reason", closeReason).Msg("Redis log subscription closed; client should reconnect")
				if err := sw.WriteEvent(sse.EventType("error"), map[string]string{"error": "retry", "reason": closeReason}); err != nil {
					logger.Warn().Err(err).Str("session_id", run.ID.String()).Msg("failed to write retry event after Redis log subscription closed")
				}
				sw.Flush()
				return true
			}
			if seen, skip := shouldSkipRedisLog(ctx, logEntry.StreamID, lastDeliveredStreamID, run.ID); skip {
				if seen != "" {
					lastDeliveredStreamID = seen
				}
				continue
			}
			if err := writeSessionLogSSEEventWithID(sw, logEntry.StreamID, logEntry.Log); err != nil {
				logger.Error().Err(err).Str("session_id", run.ID.String()).Str("stream_id", logEntry.StreamID).Msg("failed to write Redis log event to SSE stream")
				return true
			}
			lastDeliveredStreamID = logEntry.StreamID
			sw.Flush()
		case updated, ok := <-statusSub.C:
			if !ok {
				closeReason := statusSub.CloseReason()
				logger.Warn().Str("session_id", run.ID.String()).Str("reason", closeReason).Msg("Redis status subscription closed; client should reconnect")
				if err := sw.WriteEvent(sse.EventType("error"), map[string]string{"error": "retry", "reason": closeReason}); err != nil {
					logger.Warn().Err(err).Str("session_id", run.ID.String()).Msg("failed to write retry event after Redis status subscription closed")
				}
				sw.Flush()
				return true
			}
			statusPayload := h.sessionStatusPayload(ctx, orgID, updated)
			if err := sw.WriteEvent(sse.EventStatus, statusPayload); err != nil {
				logger.Error().Err(err).Str("session_id", run.ID.String()).Msg("failed to write Redis status event to SSE stream")
				return true
			}
			sw.Flush()
			if isTerminalStatus(updated.Status) {
				if err := sw.WriteEvent(sse.EventDone, statusPayload); err != nil {
					logger.Error().Err(err).Str("session_id", run.ID.String()).Msg("failed to write Redis done event to SSE stream")
					return true
				}
				sw.Flush()
				return true
			}
		case event, ok := <-eventSub.C:
			if !ok {
				closeReason := eventSub.CloseReason()
				logger.Warn().Str("session_id", run.ID.String()).Str("reason", closeReason).Msg("Redis event subscription closed; client should reconnect")
				if err := sw.WriteEvent(sse.EventType("error"), map[string]string{"error": "retry", "reason": closeReason}); err != nil {
					logger.Warn().Err(err).Str("session_id", run.ID.String()).Msg("failed to write retry event after Redis event subscription closed")
				}
				sw.Flush()
				return true
			}
			if err := writeSessionStreamSSEEvent(sw, event); err != nil {
				logger.Error().Err(err).Str("session_id", run.ID.String()).Str("event_type", string(event.Type)).Msg("failed to write Redis session event to SSE stream")
				return true
			}
			sw.Flush()
		}
	}
}

func (h *SessionHandler) sessionStatusPayload(ctx context.Context, orgID uuid.UUID, session models.Session) models.SessionDetail {
	detail := models.SessionDetail{
		Session: session,
		Threads: []models.SessionThread{},
	}
	if h.threadStore == nil {
		return detail
	}
	threads, err := h.threadStore.ListBySession(ctx, orgID, session.ID)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to load threads for session SSE status")
		return detail
	}
	if threads == nil {
		return detail
	}
	detail.Threads = threads
	return detail
}

func (h *SessionHandler) catchUpLogs(ctx context.Context, orgID, runID uuid.UUID, lastEventID string) ([]models.SessionLog, error) {
	if lastEventID == "" {
		return h.logStore.ListByRunID(ctx, orgID, runID)
	}
	if h.streams != nil {
		if buffered, ok := h.streams.ReplayBufferedLogs(runID, lastEventID); ok {
			out := make([]models.SessionLog, 0, len(buffered))
			for _, item := range buffered {
				out = append(out, item.Log)
			}
			return out, nil
		}
		ranged, err := h.streams.RangeLogsSince(ctx, runID, lastEventID, 10000)
		if err == nil {
			out := make([]models.SessionLog, 0, len(ranged))
			for _, item := range ranged {
				out = append(out, item.Log)
			}
			return out, nil
		}
		zerolog.Ctx(ctx).Warn().Err(err).Str("session_id", runID.String()).Str("last_event_id", lastEventID).Msg("failed to read Redis stream range, falling back to Postgres log catch-up")
	}
	lastSeenID, err := cache.ParseLogStreamID(lastEventID)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("session_id", runID.String()).Str("last_event_id", lastEventID).Msg("invalid Last-Event-ID, replaying full session log history")
		return h.logStore.ListByRunID(ctx, orgID, runID)
	}
	return h.logStore.ListByRunIDSince(ctx, orgID, runID, lastSeenID)
}

func shouldSkipRedisLog(ctx context.Context, streamID string, lastDeliveredStreamID string, sessionID uuid.UUID) (string, bool) {
	if lastDeliveredStreamID == "" {
		return "", false
	}

	currentID, err := cache.ParseLogStreamID(streamID)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("session_id", sessionID.String()).Str("stream_id", streamID).Msg("failed to parse Redis log stream ID")
		return "", false
	}

	lastSeenID, err := cache.ParseLogStreamID(lastDeliveredStreamID)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("session_id", sessionID.String()).Str("last_stream_id", lastDeliveredStreamID).Msg("failed to parse last delivered Redis log stream ID")
		return "", false
	}

	if currentID <= lastSeenID {
		return lastDeliveredStreamID, true
	}
	return "", false
}

func writeSessionLogSSEEvent(sw *sse.Writer, log models.SessionLog) error {
	return writeSessionLogSSEEventWithID(sw, cache.SessionLogStreamID(log.ID), log)
}

func writeSessionLogSSEEventWithID(sw *sse.Writer, streamID string, log models.SessionLog) error {
	payload := models.NewSessionLogResponse(log)
	if err := sw.WriteDataID(streamID, payload); err != nil {
		return err
	}
	if eventType, ok := humanInputSSEEventType(log); ok {
		if err := sw.WriteEventID(eventType, streamID, payload); err != nil {
			return err
		}
	}
	return nil
}

func writeSessionStreamSSEEvent(sw *sse.Writer, event models.SessionStreamEvent) error {
	switch event.Type {
	case models.SessionStreamEventThreadInboxQueued:
		return sw.WriteEvent(sse.EventThreadInboxQueued, event.Data)
	case models.SessionStreamEventThreadInboxCleared:
		return sw.WriteEvent(sse.EventThreadInboxCleared, event.Data)
	case models.SessionStreamEventThreadRuntimeUpdated:
		return sw.WriteEvent(sse.EventThreadRuntimeUpdated, event.Data)
	case models.SessionStreamEventWorkspaceGenerationChanged:
		return sw.WriteEvent(sse.EventSessionWorkspaceGenerationChanged, event.Data)
	default:
		return fmt.Errorf("unsupported session stream event type: %s", event.Type)
	}
}

func humanInputSSEEventType(log models.SessionLog) (sse.EventType, bool) {
	if log.Level != "human_input" {
		return "", false
	}
	if len(log.Metadata) == 0 {
		return sse.EventHumanInputCreated, true
	}
	var metadata struct {
		Event string `json:"event"`
	}
	if err := json.Unmarshal(log.Metadata, &metadata); err != nil || metadata.Event == "" {
		return sse.EventHumanInputCreated, true
	}
	switch sse.EventType(metadata.Event) {
	case sse.EventHumanInputCreated:
		return sse.EventHumanInputCreated, true
	case sse.EventHumanInputUpdated:
		return sse.EventHumanInputUpdated, true
	default:
		return "", false
	}
}

// GetPullRequest returns the PR associated with an agent run, or null if none exists.
// "No PR yet" is a normal empty state for an active session, not a missing resource,
// so we return 200 with a null body rather than 404.
func (h *SessionHandler) GetPullRequest(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid run ID")
		return
	}

	var pr models.PullRequest
	changesetIDParam := strings.TrimSpace(r.URL.Query().Get("changeset_id"))
	if changesetIDParam == "" {
		pr, err = h.pullRequestStore.GetPrimaryBySessionID(r.Context(), orgID, runID)
	} else {
		changesetID, parseErr := uuid.Parse(changesetIDParam)
		if parseErr != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_CHANGESET_ID", "invalid changeset ID")
			return
		}
		pr, err = h.pullRequestStore.GetByChangesetID(r.Context(), orgID, runID, changesetID)
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, models.SingleResponse[*models.PullRequest]{Data: nil})
			return
		}
		zerolog.Ctx(r.Context()).Error().Err(err).Str("session_id", runID.String()).Msg("failed to load PR for session")
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load pull request", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[*models.PullRequest]{Data: &pr})
}

// SetReviewLoopStore enables PR policy checks that depend on completed
// first-party review loops.
func (h *SessionHandler) SetReviewLoopStore(store *db.SessionReviewLoopStore) {
	h.reviewLoopStore = store
}

func (h *SessionHandler) SetReadinessStore(store *db.PRReadinessStore) {
	h.readinessStore = store
}

func (h *SessionHandler) SetReadinessRunner(runner interface {
	EnqueueRun(ctx context.Context, req prreadinesssvc.EnqueueRunRequest) (*models.PRReadinessRun, error)
}) {
	h.readinessRunner = runner
}

func (h *SessionHandler) GetReadiness(w http.ResponseWriter, r *http.Request) {
	if h.readinessStore == nil {
		writeError(w, r, http.StatusNotImplemented, "READINESS_NOT_CONFIGURED", "PR readiness is not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	if _, err := h.runStore.GetByID(r.Context(), orgID, sessionID); err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}
	run, err := h.readinessStore.GetLatestBySession(r.Context(), orgID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, models.SingleResponse[models.PRReadinessResponse]{Data: models.PRReadinessResponse{}})
			return
		}
		writeError(w, r, http.StatusInternalServerError, "READINESS_LOAD_FAILED", "failed to load PR readiness", err)
		return
	}
	role := models.Role(middleware.ActiveRoleFromContext(r.Context()))
	for i := range run.Checks {
		run.Checks[i] = run.Checks[i].WithEffectiveRole(role)
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PRReadinessResponse]{Data: models.PRReadinessResponse{Latest: run}})
}

func (h *SessionHandler) GetReadinessContext(w http.ResponseWriter, r *http.Request) {
	if h.readinessStore == nil {
		writeError(w, r, http.StatusNotImplemented, "READINESS_NOT_CONFIGURED", "PR readiness is not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	if _, err := h.runStore.GetByID(r.Context(), orgID, sessionID); err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}
	contextValue, err := h.readinessStore.GetContext(r.Context(), orgID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, models.SingleResponse[models.PRReadinessContext]{Data: models.PRReadinessContext{OrgID: orgID, SessionID: sessionID}})
			return
		}
		writeError(w, r, http.StatusInternalServerError, "READINESS_CONTEXT_LOAD_FAILED", "failed to load PR readiness context", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PRReadinessContext]{Data: contextValue})
}

func (h *SessionHandler) UpsertReadinessContext(w http.ResponseWriter, r *http.Request) {
	if h.readinessStore == nil {
		writeError(w, r, http.StatusNotImplemented, "READINESS_NOT_CONFIGURED", "PR readiness is not configured")
		return
	}
	switch models.Role(middleware.ActiveRoleFromContext(r.Context())) {
	case models.RoleAdmin, models.RoleMember, models.RoleBuilder:
	default:
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "insufficient permissions")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	if _, err := h.runStore.GetByID(r.Context(), orgID, sessionID); err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}
	var req struct {
		IssueLessReason string `json:"issue_less_reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	if len(req.IssueLessReason) > maxReadinessReasonLength {
		writeError(w, r, http.StatusBadRequest, "READINESS_REASON_TOO_LONG", fmt.Sprintf("issue_less_reason must not exceed %d characters", maxReadinessReasonLength))
		return
	}
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user is required")
		return
	}
	contextValue, err := h.readinessStore.UpsertContext(r.Context(), orgID, sessionID, req.IssueLessReason, user.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "READINESS_CONTEXT_SAVE_FAILED", "failed to save PR readiness context", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PRReadinessContext]{Data: contextValue})
}

func (h *SessionHandler) RunReadiness(w http.ResponseWriter, r *http.Request) {
	if h.readinessRunner == nil {
		writeError(w, r, http.StatusNotImplemented, "READINESS_NOT_CONFIGURED", "PR readiness is not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	session, err := h.runStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}
	if !h.requireSnapshotQuiescent(w, r, orgID, session, "running readiness checks") {
		return
	}
	var triggeredByUserID *uuid.UUID
	if user := middleware.UserFromContext(r.Context()); user != nil {
		triggeredByUserID = &user.ID
	}
	run, err := h.readinessRunner.EnqueueRun(r.Context(), prreadinesssvc.EnqueueRunRequest{
		OrgID:             orgID,
		Session:           session,
		TriggeredByUserID: triggeredByUserID,
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "READINESS_ENQUEUE_FAILED", "failed to enqueue PR readiness checks", err)
		return
	}
	writeJSON(w, http.StatusAccepted, models.SingleResponse[models.PRReadinessRun]{Data: *run})
}

// maxReadinessReasonLength caps free-text reasons (bypass reason, issue-less
// context) so they can't bloat their text columns under the 1 MB body limit.
const maxReadinessReasonLength = 2000

func (h *SessionHandler) CreateReadinessBypass(w http.ResponseWriter, r *http.Request) {
	if h.readinessStore == nil {
		writeError(w, r, http.StatusNotImplemented, "READINESS_NOT_CONFIGURED", "PR readiness is not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	session, err := h.runStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	if len(req.Reason) > maxReadinessReasonLength {
		writeError(w, r, http.StatusBadRequest, "READINESS_REASON_TOO_LONG", fmt.Sprintf("reason must not exceed %d characters", maxReadinessReasonLength))
		return
	}
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user is required")
		return
	}
	run, err := h.readinessStore.GetLatestBySession(r.Context(), orgID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusConflict, "READINESS_REQUIRED_BEFORE_BYPASS", "Readiness must complete before blockers can be bypassed")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "READINESS_LOAD_FAILED", "failed to load PR readiness", err)
		return
	}
	if run.Status == models.PRReadinessRunStatusQueued || run.Status == models.PRReadinessRunStatusRunning {
		writeError(w, r, http.StatusConflict, "READINESS_RUNNING", "Readiness is still running and cannot be bypassed")
		return
	}
	if run.EvaluatedWorkspaceRevision != session.WorkspaceRevision || stringPtrValue(run.EvaluatedSnapshotKey) != stringPtrValue(session.SnapshotKey) {
		writeError(w, r, http.StatusConflict, "READINESS_STALE", "Stale readiness cannot be bypassed; re-run checks first")
		return
	}
	policy, err := h.readinessStore.ResolvePolicy(r.Context(), orgID, session.RepositoryID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "READINESS_POLICY_LOAD_FAILED", "failed to load PR readiness policy", err)
		return
	}
	role := models.Role(middleware.ActiveRoleFromContext(r.Context()))
	bypass, err := h.readinessStore.CreateBypassWithPolicy(r.Context(), orgID, run.ID, user.ID, req.Reason, role, policy.Config)
	if err != nil {
		switch {
		case errors.Is(err, db.ErrBypassNotAllowed):
			writeError(w, r, http.StatusConflict, "READINESS_BYPASS_NOT_ALLOWED", err.Error())
		case errors.Is(err, db.ErrBypassNotEligible):
			writeError(w, r, http.StatusConflict, "READINESS_BYPASS_NOT_ELIGIBLE", err.Error())
		default:
			writeError(w, r, http.StatusInternalServerError, "READINESS_BYPASS_FAILED", "failed to record PR readiness bypass", err)
		}
		return
	}
	bypassID := bypass.ID.String()
	sessionIDCopy := sessionID
	details, _ := json.Marshal(map[string]any{
		"readiness_run_id": bypass.ReadinessRunID,
		"bypassed_checks":  bypass.BypassedChecks,
		"reason":           bypass.Reason,
	})
	emitUserAuditWithSession(h.audit, r, models.AuditActionPRReadinessBypassed, models.AuditResourcePRReadinessBypass, &bypassID, &sessionIDCopy, nil, json.RawMessage(details))
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.PRReadinessBypass]{Data: bypass})
}

func (h *SessionHandler) GetReadinessPolicy(w http.ResponseWriter, r *http.Request) {
	if h.readinessStore == nil {
		writeError(w, r, http.StatusNotImplemented, "READINESS_NOT_CONFIGURED", "PR readiness is not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	repositoryID, ok := parseOptionalUUIDQuery(w, r, "repository_id")
	if !ok {
		return
	}
	if !h.validateReadinessRepositoryScope(w, r, orgID, repositoryID) {
		return
	}
	policy, err := h.readinessStore.ResolvePolicy(r.Context(), orgID, repositoryID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "READINESS_POLICY_LOAD_FAILED", "failed to load PR readiness policy", err)
		return
	}
	counts, err := h.readinessStore.ListBypassCounts(r.Context(), orgID, repositoryID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "READINESS_BYPASS_COUNTS_LOAD_FAILED", "failed to load PR readiness bypass counts", err)
		return
	}
	policy.BypassCounts = &counts
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PRReadinessResolvedPolicy]{Data: policy})
}

func (h *SessionHandler) PutReadinessPolicy(w http.ResponseWriter, r *http.Request) {
	if h.readinessStore == nil {
		writeError(w, r, http.StatusNotImplemented, "READINESS_NOT_CONFIGURED", "PR readiness is not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	var req struct {
		RepositoryID *uuid.UUID                     `json:"repository_id,omitempty"`
		Config       models.PRReadinessPolicyConfig `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user is required")
		return
	}
	if !h.validateReadinessRepositoryScope(w, r, orgID, req.RepositoryID) {
		return
	}
	if err := req.Config.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "READINESS_POLICY_INVALID", "invalid PR readiness policy", err)
		return
	}
	record, err := h.readinessStore.SavePolicy(r.Context(), orgID, req.RepositoryID, req.Config, &user.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "READINESS_POLICY_SAVE_FAILED", "failed to save PR readiness policy", err)
		return
	}
	resourceID := record.ID.String()
	details, _ := json.Marshal(map[string]any{
		"repository_id": record.RepositoryID,
		"source":        "api",
	})
	emitUserAudit(h.audit, r, models.AuditActionPRReadinessPolicyUpdated, models.AuditResourcePRReadinessPolicy, &resourceID, json.RawMessage(details))
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PRReadinessPolicyRecord]{Data: record})
}

func (h *SessionHandler) ListReadinessCustomChecks(w http.ResponseWriter, r *http.Request) {
	if h.readinessStore == nil {
		writeError(w, r, http.StatusNotImplemented, "READINESS_NOT_CONFIGURED", "PR readiness is not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	repositoryID, ok := parseOptionalUUIDQuery(w, r, "repository_id")
	if !ok {
		return
	}
	if !h.validateReadinessRepositoryScope(w, r, orgID, repositoryID) {
		return
	}
	checks, err := h.readinessStore.ListCustomChecks(r.Context(), orgID, repositoryID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "READINESS_CUSTOM_CHECKS_LOAD_FAILED", "failed to load PR readiness custom checks", err)
		return
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.PRReadinessCustomCheck]{Data: checks})
}

func (h *SessionHandler) CreateReadinessCustomCheck(w http.ResponseWriter, r *http.Request) {
	_ = middleware.OrgIDFromContext(r.Context())
	h.saveReadinessCustomCheck(w, r, nil)
}

func (h *SessionHandler) UpdateReadinessCustomCheck(w http.ResponseWriter, r *http.Request) {
	_ = middleware.OrgIDFromContext(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "check_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid custom check ID")
		return
	}
	h.saveReadinessCustomCheck(w, r, &id)
}

func (h *SessionHandler) saveReadinessCustomCheck(w http.ResponseWriter, r *http.Request, id *uuid.UUID) {
	if h.readinessStore == nil {
		writeError(w, r, http.StatusNotImplemented, "READINESS_NOT_CONFIGURED", "PR readiness is not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	var req models.PRReadinessCustomCheck
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	if id != nil {
		req.ID = *id
	}
	req.Source = models.PRReadinessCustomCheckSourceOrgSettings
	if err := req.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "READINESS_CUSTOM_CHECK_INVALID", err.Error())
		return
	}
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user is required")
		return
	}
	if id == nil && !h.validateReadinessRepositoryScope(w, r, orgID, req.RepositoryID) {
		return
	}
	var check models.PRReadinessCustomCheck
	var err error
	if id != nil {
		check, err = h.readinessStore.UpdateCustomCheck(r.Context(), orgID, *id, req, &user.ID)
	} else {
		check, err = h.readinessStore.SaveCustomCheck(r.Context(), orgID, req, &user.ID)
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "READINESS_CUSTOM_CHECK_SAVE_FAILED", "failed to save PR readiness custom check", err)
		return
	}
	resourceID := check.ID.String()
	details, _ := json.Marshal(map[string]any{
		"check_key":     check.CheckKey,
		"repository_id": check.RepositoryID,
	})
	emitUserAudit(h.audit, r, models.AuditActionPRReadinessCustomCheckUpdated, models.AuditResourcePRReadinessCustomCheck, &resourceID, json.RawMessage(details))
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PRReadinessCustomCheck]{Data: check})
}

func (h *SessionHandler) DeleteReadinessCustomCheck(w http.ResponseWriter, r *http.Request) {
	if h.readinessStore == nil {
		writeError(w, r, http.StatusNotImplemented, "READINESS_NOT_CONFIGURED", "PR readiness is not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "check_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid custom check ID")
		return
	}
	if err := h.readinessStore.DeleteCustomCheck(r.Context(), orgID, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "custom check not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "READINESS_CUSTOM_CHECK_DELETE_FAILED", "failed to delete PR readiness custom check", err)
		return
	}
	resourceID := id.String()
	emitUserAudit(h.audit, r, models.AuditActionPRReadinessCustomCheckDeleted, models.AuditResourcePRReadinessCustomCheck, &resourceID, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *SessionHandler) validateReadinessRepositoryScope(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, repositoryID *uuid.UUID) bool {
	if repositoryID == nil {
		return true
	}
	if _, err := requireActiveRepo(r.Context(), h.repoStore, orgID, *repositoryID); err != nil {
		switch {
		case errors.Is(err, errRepoDisconnected):
			writeError(w, r, http.StatusBadRequest, "REPO_DISCONNECTED", "repository is disconnected; reconnect it before configuring PR readiness")
		case errors.Is(err, errRepoStoreUnconfigured):
			writeError(w, r, http.StatusInternalServerError, "REPO_STORE_UNCONFIGURED", "repository lookup not configured")
		default:
			writeError(w, r, http.StatusNotFound, "REPOSITORY_NOT_FOUND", "repository not found")
		}
		return false
	}
	return true
}

func parseOptionalUUIDQuery(w http.ResponseWriter, r *http.Request, key string) (*uuid.UUID, bool) {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return nil, true
	}
	id, err := uuid.Parse(value)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_QUERY", "invalid "+key)
		return nil, false
	}
	return &id, true
}

func (h *SessionHandler) requireBuilderReviewForCurrentSnapshot(w http.ResponseWriter, r *http.Request, orgID, sessionID uuid.UUID, snapshotKey string) bool {
	role := middleware.ActiveRoleFromContext(r.Context())
	if role != string(models.RoleBuilder) {
		return true
	}
	if h.reviewLoopStore == nil {
		writeError(w, r, http.StatusInternalServerError, "REVIEW_POLICY_UNAVAILABLE", "review policy is not configured")
		return false
	}
	loops, err := h.reviewLoopStore.ListLoopsBySession(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "REVIEW_POLICY_CHECK_FAILED", "failed to check review status", err)
		return false
	}
	for _, loop := range loops {
		if loop.Status == models.ReviewLoopStatusClean && loop.LatestCheckpointKey != nil && *loop.LatestCheckpointKey == snapshotKey {
			return true
		}
	}
	writeError(w, r, http.StatusConflict, "REVIEW_REQUIRED_BEFORE_PR", "Builders must run Review for the current snapshot before publishing pull request changes")
	return false
}

func (h *SessionHandler) requirePRReadinessForBuilder(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, session models.Session) bool {
	role := middleware.ActiveRoleFromContext(r.Context())
	if role != string(models.RoleBuilder) {
		return true
	}
	if h.readinessStore == nil {
		return h.requireBuilderReviewForCurrentSnapshot(w, r, orgID, session.ID, stringPtrValue(session.SnapshotKey))
	}
	resolved, err := h.readinessStore.ResolvePolicy(r.Context(), orgID, session.RepositoryID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "READINESS_POLICY_CHECK_FAILED", "failed to check PR readiness policy", err)
		return false
	}
	if !resolved.Config.RequiresRoleReadiness(models.RoleBuilder) {
		return true
	}
	run, err := h.readinessStore.GetLatestBySession(r.Context(), orgID, session.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusConflict, "READINESS_REQUIRED_BEFORE_PR", "Builders must run readiness checks before creating a PR")
			return false
		}
		writeError(w, r, http.StatusInternalServerError, "READINESS_POLICY_CHECK_FAILED", "failed to check PR readiness", err)
		return false
	}
	if run.Status == models.PRReadinessRunStatusQueued || run.Status == models.PRReadinessRunStatusRunning {
		writeError(w, r, http.StatusConflict, "READINESS_RUNNING", "PR readiness checks are still running")
		return false
	}
	if run.EvaluatedWorkspaceRevision != session.WorkspaceRevision || stringPtrValue(run.EvaluatedSnapshotKey) != stringPtrValue(session.SnapshotKey) {
		writeError(w, r, http.StatusConflict, "READINESS_STALE", "Readiness is stale after the latest file changes; re-run readiness checks")
		return false
	}
	if run.Status == models.PRReadinessRunStatusFailed {
		writeError(w, r, http.StatusConflict, "READINESS_BLOCKED", "PR readiness blockers must pass before builders can create a PR")
		return false
	}
	if blockers := run.UnbypassedBlockingCheckKeys(models.RoleBuilder); len(blockers) > 0 {
		writeError(w, r, http.StatusConflict, "READINESS_BLOCKED", "PR readiness blockers must pass or be bypassed before builders can create a PR")
		return false
	}
	return true
}

// CreatePR handles POST /sessions/{id}/pr — enqueues a job that pushes the
// session's snapshot to GitHub and opens a pull request. The session must
// still have a snapshot and must not already have an associated PR. While a
// prior attempt is in flight (queued or pushing), returns 409 to prevent
// double-submits; a failed prior attempt may be retried.
func (h *SessionHandler) CreatePR(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	session, err := h.runStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}
	targetChangeset, err := h.requestedChangeset(r.Context(), orgID, sessionID, r.URL.Query().Get("changeset_id"))
	if err != nil {
		switch {
		case errors.Is(err, errInvalidChangesetID):
			writeError(w, r, http.StatusBadRequest, "INVALID_CHANGESET_ID", "invalid pull request target", err)
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, r, http.StatusNotFound, "CHANGESET_NOT_FOUND", "pull request target not found")
		default:
			writeError(w, r, http.StatusInternalServerError, "CHANGESET_LOOKUP_FAILED", "failed to resolve the pull request target", err)
		}
		return
	}
	if !targetChangeset.IsPrimary {
		writeError(w, r, http.StatusConflict, "CHANGESET_NOT_MATERIALIZED", "this pull request branch must be materialized before it can be created")
		return
	}

	if session.SandboxState == models.SandboxStateDestroyed {
		writeError(w, r, http.StatusGone, "SNAPSHOT_EXPIRED", ghservice.SnapshotExpiredPRMessage)
		return
	}
	if session.SnapshotKey == nil || *session.SnapshotKey == "" {
		writeError(w, r, http.StatusConflict, "SNAPSHOT_NOT_CAPTURED", ghservice.SnapshotNotCapturedPRMessage)
		return
	}
	if !h.requireSnapshotQuiescent(w, r, orgID, session, "creating a PR") {
		return
	}

	// Non-primary changesets are rejected above with CHANGESET_NOT_MATERIALIZED,
	// so from here the target is always the primary changeset and only the
	// legacy session-scoped creation guards apply.
	switch session.PRCreationState {
	case models.PRCreationStateQueued, models.PRCreationStatePushing:
		writeError(w, r, http.StatusConflict, "PR_IN_FLIGHT", "PR creation already in progress")
		return
	case models.PRCreationStateSucceeded:
		// Succeeded means a PR row should exist; fall through to the
		// PR_EXISTS check below so the 409 path is consistent.
	}

	// Check whether a PR already exists for this session.
	_, prErr := h.pullRequestStore.GetPrimaryBySessionID(r.Context(), orgID, sessionID)
	if prErr == nil {
		writeError(w, r, http.StatusConflict, "PR_EXISTS", "a pull request already exists for this session")
		return
	}
	if !errors.Is(prErr, pgx.ErrNoRows) {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to check for existing PR", prErr)
		return
	}
	if targetChangeset.PRCreationState == models.PRCreationStateSucceeded || session.PRCreationState == models.PRCreationStateSucceeded {
		writeError(w, r, http.StatusConflict, "PR_ALREADY_CREATED", "PR creation already completed for this session")
		return
	}

	// Parse optional request body for per-PR overrides and authorship flow.
	var req struct {
		Draft          *bool  `json:"draft,omitempty"`
		AuthorMode     string `json:"author_mode,omitempty"`
		ResumeToken    string `json:"resume_token,omitempty"`
		MergeWhenReady bool   `json:"merge_when_ready,omitempty"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
			return
		}
	}
	authorMode, err := parsePRAuthorMode(req.AuthorMode)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	if req.ResumeToken != "" && len(h.prAuthSigningKey) > 0 {
		if claims, tokenErr := parsePRAuthResumeToken(h.prAuthSigningKey, req.ResumeToken, time.Now()); tokenErr == nil && claims.MergeWhenReady {
			req.MergeWhenReady = true
		}
	}

	orgSettings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred}
	if h.orgStore != nil {
		if org, orgErr := h.orgStore.GetByID(r.Context(), orgID); orgErr == nil {
			if parsed, parseErr := models.ParseOrgSettings(org.Settings); parseErr == nil {
				orgSettings = parsed
			}
		}
	}

	if h.maybeAutoRunPRReadinessOnCreatePR(w, r, orgID, session) {
		return
	}

	if !h.requirePRReadinessForBuilder(w, r, orgID, session) {
		return
	}

	if !h.requirePRAuthOrIntercept(w, r, prAuthInterceptParams{
		SessionID:            sessionID,
		OrgID:                orgID,
		Session:              &session,
		OrgSettings:          orgSettings,
		AuthorMode:           authorMode,
		ResumeToken:          req.ResumeToken,
		Action:               prAuthActionCreatePR,
		ActionDescription:    "create this pull request",
		ResumeExpiredMessage: "GitHub authorization completed, but the PR resume request expired. Please click Create PR again.",
		Draft:                req.Draft,
		MergeWhenReady:       req.MergeWhenReady,
	}) {
		return
	}

	changesetID := targetChangeset.ID
	payload := map[string]any{
		"session_id":     sessionID.String(),
		"changeset_id":   changesetID.String(),
		"org_id":         orgID.String(),
		"requested_role": middleware.ActiveRoleFromContext(r.Context()),
	}
	if req.Draft != nil {
		payload["draft"] = *req.Draft
	}
	if authorMode != prAuthorModeAuto {
		payload["author_mode"] = string(authorMode)
	}
	if req.MergeWhenReady {
		user := middleware.UserFromContext(r.Context())
		if user == nil {
			writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
			return
		}
		payload["merge_when_ready"] = true
		payload["requested_by_user_id"] = user.ID.String()
	}
	dedupeKey := db.OpenPRDedupeKey(changesetID)
	queued, err := h.enqueuePublishActionInTx(
		r.Context(),
		orgID,
		sessionID,
		"agent",
		"open_pr",
		payload,
		dedupeKey,
		func(ctx context.Context, sessions *db.SessionStore, changesets *db.SessionChangesetStore) (bool, error) {
			if h.changesetStore != nil {
				return changesets.TryMarkPRCreationQueued(ctx, orgID, sessionID, changesetID)
			}
			return sessions.TryMarkPRCreationQueued(ctx, orgID, sessionID)
		},
	)
	if err != nil {
		if txErr := (*publishActionTxError)(nil); errors.As(err, &txErr) && txErr.phase == "state" {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to mark PR creation as queued", err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue PR creation job", err)
		return
	}
	if !queued {
		writeError(w, r, http.StatusConflict, "PR_IN_FLIGHT", "PR creation already in progress")
		return
	}

	sessionIDStr := sessionID.String()
	prDetails := sessionAuditSnapshot(&session, nil, map[string]any{
		"job_type": "open_pr",
	})
	if req.Draft != nil {
		prDetails["draft"] = *req.Draft
	}
	if req.MergeWhenReady {
		prDetails["merge_when_ready"] = true
	}
	emitUserAuditWithSession(h.audit, r, models.AuditActionSessionPRRequested, models.AuditResourceSession, &sessionIDStr, &session.ID, nil,
		marshalAuditDetails(h.logger, prDetails))
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

func (h *SessionHandler) maybeAutoRunPRReadinessOnCreatePR(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, session models.Session) bool {
	if h.readinessStore == nil || h.readinessRunner == nil {
		return false
	}
	resolved, err := h.readinessStore.ResolvePolicy(r.Context(), orgID, session.RepositoryID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "READINESS_POLICY_CHECK_FAILED", "failed to check PR readiness policy", err)
		return true
	}
	if !resolved.Config.AutoRun.OnCreatePR {
		return false
	}
	latest, err := h.readinessStore.GetLatestBySession(r.Context(), orgID, session.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusInternalServerError, "READINESS_LOAD_FAILED", "failed to load PR readiness", err)
		return true
	}
	needsRun := errors.Is(err, pgx.ErrNoRows) ||
		latest == nil ||
		latest.Status == models.PRReadinessRunStatusQueued ||
		latest.Status == models.PRReadinessRunStatusRunning ||
		latest.EvaluatedWorkspaceRevision != session.WorkspaceRevision ||
		stringPtrValue(latest.EvaluatedSnapshotKey) != stringPtrValue(session.SnapshotKey)
	if !needsRun {
		return false
	}
	var userID *uuid.UUID
	if user := middleware.UserFromContext(r.Context()); user != nil {
		userID = &user.ID
	}
	run, err := h.readinessRunner.EnqueueRun(r.Context(), prreadinesssvc.EnqueueRunRequest{
		OrgID:             orgID,
		Session:           session,
		TriggeredByUserID: userID,
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "READINESS_ENQUEUE_FAILED", "failed to enqueue PR readiness checks", err)
		return true
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "readiness_queued", "readiness_run_id": run.ID})
	return true
}

// CreateBranch handles POST /sessions/{id}/branch — enqueues a job that
// pushes the session snapshot to GitHub without opening a pull request.
func (h *SessionHandler) CreateBranch(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	session, err := h.runStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}
	if session.SandboxState == models.SandboxStateDestroyed {
		writeError(w, r, http.StatusGone, "SNAPSHOT_EXPIRED", ghservice.SnapshotExpiredPRMessage)
		return
	}
	if session.SnapshotKey == nil || *session.SnapshotKey == "" {
		writeError(w, r, http.StatusConflict, "SNAPSHOT_NOT_CAPTURED", ghservice.SnapshotNotCapturedPRMessage)
		return
	}
	if !h.requireSnapshotQuiescent(w, r, orgID, session, "creating a branch") {
		return
	}
	switch session.BranchCreationState {
	case models.BranchCreationStateQueued, models.BranchCreationStatePushing:
		writeError(w, r, http.StatusConflict, "BRANCH_IN_FLIGHT", "branch creation already in progress")
		return
	}

	var req struct {
		AuthorMode  string `json:"author_mode,omitempty"`
		ResumeToken string `json:"resume_token,omitempty"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
			return
		}
	}
	authorMode, err := parsePRAuthorMode(req.AuthorMode)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}

	orgSettings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred}
	if h.orgStore != nil {
		if org, orgErr := h.orgStore.GetByID(r.Context(), orgID); orgErr == nil {
			if parsed, parseErr := models.ParseOrgSettings(org.Settings); parseErr == nil {
				orgSettings = parsed
			}
		}
	}
	if !h.requirePRAuthOrIntercept(w, r, prAuthInterceptParams{
		SessionID:            sessionID,
		OrgID:                orgID,
		Session:              &session,
		OrgSettings:          orgSettings,
		AuthorMode:           authorMode,
		ResumeToken:          req.ResumeToken,
		Action:               prAuthActionCreateBranch,
		ActionDescription:    "create this branch",
		ResumeExpiredMessage: "GitHub authorization completed, but the branch resume request expired. Please click Create branch again.",
	}) {
		return
	}

	payload := map[string]any{
		"session_id": sessionID.String(),
		"org_id":     orgID.String(),
	}
	if authorMode != prAuthorModeAuto {
		payload["author_mode"] = string(authorMode)
	}
	dedupeKey := fmt.Sprintf("create_branch:%s", sessionID)
	queued, err := h.enqueuePublishActionInTx(
		r.Context(),
		orgID,
		sessionID,
		"agent",
		"create_branch",
		payload,
		dedupeKey,
		func(ctx context.Context, sessions *db.SessionStore, _ *db.SessionChangesetStore) (bool, error) {
			return sessions.TryMarkBranchCreationQueued(ctx, orgID, sessionID)
		},
	)
	if err != nil {
		if txErr := (*publishActionTxError)(nil); errors.As(err, &txErr) && txErr.phase == "state" {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to mark branch creation as queued", err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue branch creation job", err)
		return
	}
	if !queued {
		writeError(w, r, http.StatusConflict, "BRANCH_IN_FLIGHT", "branch creation already in progress")
		return
	}

	sessionIDStr := sessionID.String()
	details := sessionAuditSnapshot(&session, nil, map[string]any{
		"job_type": "create_branch",
	})
	emitUserAuditWithSession(h.audit, r, models.AuditActionSessionBranchRequested, models.AuditResourceSession, &sessionIDStr, &session.ID, nil,
		marshalAuditDetails(h.logger, details))
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

// PushChangesToPR handles POST /sessions/{id}/pr/push — enqueues a job that
// pushes any uncommitted/unpushed changes from the session sandbox up to the
// existing PR's branch and updates the PR row's head_sha. The session must
// have an open PR and a still-restorable snapshot. While a prior push attempt
// is in flight (queued or pushing), returns 409 to prevent double-submits.
//
// Mirrors the validation, auth interception, and enqueue structure of CreatePR
// so the two endpoints behave identically from a client perspective. The only
// material differences are: (a) requires an existing PR (404 if none), (b)
// rejects PRs that are not in the "open" state, and (c) the dedupe key,
// queue/job names, and audit action are push-flavored.
func (h *SessionHandler) PushChangesToPR(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	session, err := h.runStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}

	if session.PRPushState == models.PRPushStateFailed &&
		session.PRPushErrorCode == models.PRPushErrorCodeBranchDiverged {
		writeError(w, r, http.StatusConflict, "PR_BRANCH_DIVERGED", ghservice.PushBranchDivergedPRMessage)
		return
	}

	if session.SandboxState == models.SandboxStateDestroyed {
		writeError(w, r, http.StatusGone, "SNAPSHOT_EXPIRED", ghservice.SnapshotExpiredPRMessage)
		return
	}
	if session.SnapshotKey == nil || *session.SnapshotKey == "" {
		writeError(w, r, http.StatusConflict, "SNAPSHOT_NOT_CAPTURED", ghservice.SnapshotNotCapturedPRMessage)
		return
	}
	if !h.requireSnapshotQuiescent(w, r, orgID, session, "pushing changes") {
		return
	}

	switch session.PRPushState {
	case models.PRPushStateQueued, models.PRPushStatePushing:
		writeError(w, r, http.StatusConflict, "PR_PUSH_IN_FLIGHT", "a push to this PR is already in progress")
		return
	}

	pr, prErr := h.pullRequestStore.GetPrimaryBySessionID(r.Context(), orgID, sessionID)
	if prErr != nil {
		if errors.Is(prErr, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NO_PR", "this session has no pull request to push to; create one first")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load pull request", prErr)
		return
	}
	if pr.Status != models.PullRequestStatusOpen {
		writeError(w, r, http.StatusConflict, "PR_CLOSED", "this pull request is no longer open")
		return
	}

	var req struct {
		AuthorMode  string `json:"author_mode,omitempty"`
		ResumeToken string `json:"resume_token,omitempty"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
			return
		}
	}
	authorMode, err := parsePRAuthorMode(req.AuthorMode)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}

	orgSettings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred}
	if h.orgStore != nil {
		if org, orgErr := h.orgStore.GetByID(r.Context(), orgID); orgErr == nil {
			if parsed, parseErr := models.ParseOrgSettings(org.Settings); parseErr == nil {
				orgSettings = parsed
			}
		}
	}

	if !h.requirePRReadinessForBuilder(w, r, orgID, session) {
		return
	}

	if !h.requirePRAuthOrIntercept(w, r, prAuthInterceptParams{
		SessionID:   sessionID,
		OrgID:       orgID,
		Session:     &session,
		OrgSettings: orgSettings,
		AuthorMode:  authorMode,
		ResumeToken: req.ResumeToken,
		Action:      prAuthActionPushChanges,
		// Draft is intentionally omitted: the push endpoint has no draft
		// toggle, and the field has no meaning once the PR row already
		// exists. Leaving it nil keeps the resume claim minimal.
		ActionDescription:    "push these changes",
		ResumeExpiredMessage: "GitHub authorization completed, but the resume request expired. Please click Push changes again.",
	}) {
		return
	}

	payload := map[string]any{
		"session_id": sessionID.String(),
		"org_id":     orgID.String(),
	}
	if authorMode != prAuthorModeAuto {
		payload["author_mode"] = string(authorMode)
	}
	dedupeKey := fmt.Sprintf("push_pr:%s", sessionID)
	// Atomically transition pr_push_state from any non-in-flight state to
	// 'queued'. The in-memory precheck above rejects the obvious case where
	// the column is already queued/pushing, but two concurrent requests can
	// both pass that check and reach this line. CAS resolves the race: the
	// loser sees rows-affected=0 and returns 409 before inserting a job.
	queued, err := h.enqueuePublishActionInTx(
		r.Context(),
		orgID,
		sessionID,
		"agent",
		"push_pr_changes",
		payload,
		dedupeKey,
		func(ctx context.Context, sessions *db.SessionStore, _ *db.SessionChangesetStore) (bool, error) {
			return sessions.TryMarkPRPushQueued(ctx, orgID, sessionID)
		},
	)
	if err != nil {
		if txErr := (*publishActionTxError)(nil); errors.As(err, &txErr) && txErr.phase == "state" {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to mark PR push as queued", err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue push job", err)
		return
	}
	if !queued {
		writeError(w, r, http.StatusConflict, "PR_PUSH_IN_FLIGHT", "a push to this PR is already in progress")
		return
	}

	sessionIDStr := sessionID.String()
	pushDetails := sessionAuditSnapshot(&session, nil, map[string]any{
		"job_type": "push_pr_changes",
	})
	emitUserAuditWithSession(h.audit, r, models.AuditActionSessionPRPushRequested, models.AuditResourceSession, &sessionIDStr, &session.ID, nil,
		marshalAuditDetails(h.logger, pushDetails))
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

// ListQuestions returns the questions for an agent run.
func (h *SessionHandler) ListQuestions(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid run ID")
		return
	}

	questions, err := h.questionStore.ListByRunID(r.Context(), orgID, runID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list questions", err)
		return
	}
	if questions == nil {
		questions = []models.SessionQuestion{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.SessionQuestion]{
		Data: questions,
	})
}

// AnswerQuestion records an answer to an agent run question.
func (h *SessionHandler) AnswerQuestion(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	qID, err := uuid.Parse(chi.URLParam(r, "qid"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid question ID")
		return
	}

	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user not found")
		return
	}

	var body struct {
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	if body.Answer == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_ANSWER", "answer is required")
		return
	}

	if err := h.questionStore.Answer(r.Context(), orgID, qID, body.Answer, user.ID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "ANSWER_FAILED", "failed to answer question", err)
		return
	}

	question, err := h.questionStore.GetByID(r.Context(), orgID, qID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "FETCH_FAILED", "failed to fetch updated question", err)
		return
	}

	qIDStr := qID.String()
	var sessionIDPtr *uuid.UUID
	if sessionID, parseErr := uuid.Parse(chi.URLParam(r, "id")); parseErr == nil {
		sessionIDPtr = &sessionID
	}
	questionDetails := map[string]any{
		"question_id":   question.ID.String(),
		"session_id":    question.SessionID.String(),
		"question_text": question.QuestionText,
		"status":        question.Status,
		"answer_length": len(body.Answer),
		"answered_by":   user.ID.String(),
		"option_count":  len(question.Options),
	}
	if question.BlocksPhase != nil {
		questionDetails["blocks_phase"] = *question.BlocksPhase
	}
	emitUserAuditWithSession(h.audit, r, models.AuditActionSessionQuestionAnswered, models.AuditResourceSession, &qIDStr, sessionIDPtr, nil,
		marshalAuditDetails(h.logger, questionDetails))
	writeJSON(w, http.StatusOK, models.SingleResponse[models.SessionQuestion]{Data: question})
}

func (h *SessionHandler) ListHumanInputRequests(w http.ResponseWriter, r *http.Request) {
	if h.humanInputService == nil {
		writeError(w, r, http.StatusNotImplemented, "NOT_CONFIGURED", "human input requests are not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	var filters db.HumanInputRequestFilters
	if statusParam := strings.TrimSpace(r.URL.Query().Get("status")); statusParam != "" {
		status := models.HumanInputRequestStatus(statusParam)
		if err := status.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_STATUS", "invalid human input request status")
			return
		}
		filters.Status = status
	}
	if threadParam := strings.TrimSpace(r.URL.Query().Get("thread_id")); threadParam != "" {
		threadID, err := uuid.Parse(threadParam)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_THREAD_ID", "invalid thread ID")
			return
		}
		filters.ThreadID = &threadID
	}

	requests, err := h.humanInputService.List(r.Context(), orgID, sessionID, filters)
	if err != nil {
		if errors.Is(err, humaninputsvc.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list human input requests", err)
		return
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.HumanInputRequest]{Data: requests})
}

func (h *SessionHandler) AnswerHumanInputRequest(w http.ResponseWriter, r *http.Request) {
	if h.humanInputService == nil {
		writeError(w, r, http.StatusNotImplemented, "NOT_CONFIGURED", "human input requests are not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	requestID, err := uuid.Parse(chi.URLParam(r, "request_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST_ID", "invalid human input request ID")
		return
	}
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user not found")
		return
	}

	var body models.HumanInputAnswerInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	result, err := h.humanInputService.Answer(r.Context(), humaninputsvc.AnswerInput{
		OrgID:     orgID,
		SessionID: sessionID,
		RequestID: requestID,
		UserID:    user.ID,
		Answer:    body,
	})
	if err != nil {
		writeHumanInputServiceError(w, r, err)
		return
	}
	answered := result.Request

	requestIDStr := requestID.String()
	answerLength := 0
	if result.Message != nil {
		answerLength = len(result.Message.Content)
	}
	details := map[string]any{
		"request_id":    requestID.String(),
		"session_id":    sessionID.String(),
		"request_kind":  string(answered.Kind),
		"status":        string(answered.Status),
		"answered_by":   user.ID.String(),
		"choice_count":  len(answered.Choices),
		"answer_length": answerLength,
	}
	emitUserAuditWithSession(h.audit, r, models.AuditActionSessionHumanInputAnswered, models.AuditResourceSession, &requestIDStr, &sessionID, nil,
		marshalAuditDetails(h.logger, details))
	h.emitHumanInputUpdateLog(r.Context(), orgID, sessionID, answered, models.HumanInputRequestStatusAnswered)
	writeJSON(w, http.StatusOK, models.SingleResponse[models.HumanInputRequest]{Data: answered})
}

func (h *SessionHandler) CancelHumanInputRequest(w http.ResponseWriter, r *http.Request) {
	if h.humanInputService == nil {
		writeError(w, r, http.StatusNotImplemented, "NOT_CONFIGURED", "human input requests are not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	requestID, err := uuid.Parse(chi.URLParam(r, "request_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST_ID", "invalid human input request ID")
		return
	}
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user not found")
		return
	}

	result, err := h.humanInputService.Cancel(r.Context(), humaninputsvc.CancelInput{
		OrgID:     orgID,
		SessionID: sessionID,
		RequestID: requestID,
		UserID:    user.ID,
	})
	if err != nil {
		writeHumanInputServiceError(w, r, err)
		return
	}
	cancelled := result.Request

	requestIDStr := requestID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionSessionHumanInputCancelled, models.AuditResourceSession, &requestIDStr, &sessionID, nil,
		marshalAuditDetails(h.logger, map[string]any{
			"request_id":   requestID.String(),
			"session_id":   sessionID.String(),
			"request_kind": string(cancelled.Kind),
			"cancelled_by": user.ID.String(),
		}))
	h.emitHumanInputUpdateLog(r.Context(), orgID, sessionID, cancelled, models.HumanInputRequestStatusCancelled)
	writeJSON(w, http.StatusOK, models.SingleResponse[models.HumanInputRequest]{Data: cancelled})
}

func writeHumanInputServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, humaninputsvc.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "human input request not found")
	case errors.Is(err, humaninputsvc.ErrSnapshotExpired):
		writeError(w, r, http.StatusGone, "SNAPSHOT_EXPIRED", "this session's environment has expired and can no longer be continued")
	case errors.Is(err, humaninputsvc.ErrInvalidAnswer):
		writeError(w, r, http.StatusBadRequest, "INVALID_ANSWER", err.Error())
	case errors.Is(err, humaninputsvc.ErrNotPending):
		writeError(w, r, http.StatusConflict, "NOT_PENDING", "human input request is no longer pending")
	case errors.Is(err, humaninputsvc.ErrRunningLimit):
		writeError(w, r, http.StatusConflict, "RUNNING_LIMIT", "this session already has the maximum number of tabs running concurrently")
	case errors.Is(err, humaninputsvc.ErrCheckpointPending):
		writeError(w, r, http.StatusConflict, "CHECKPOINT_PENDING", "the agent is still saving its pause checkpoint; try again once the session is awaiting input")
	case errors.Is(err, humaninputsvc.ErrNotResumable):
		writeError(w, r, http.StatusConflict, "NOT_RESUMABLE", "session must be awaiting input or otherwise resumable to answer human input")
	default:
		writeError(w, r, http.StatusInternalServerError, "HUMAN_INPUT_FAILED", "failed to update human input request", err)
	}
}

func (h *SessionHandler) emitHumanInputUpdateLog(ctx context.Context, orgID, sessionID uuid.UUID, req models.HumanInputRequest, status models.HumanInputRequestStatus) {
	if h.logStore == nil {
		return
	}
	message := "Human input request updated."
	switch status {
	case models.HumanInputRequestStatusAnswered:
		message = "Human input request answered."
	case models.HumanInputRequestStatusCancelled:
		message = "Human input request cancelled."
	}
	log := &models.SessionLog{
		SessionID:  sessionID,
		OrgID:      orgID,
		ThreadID:   req.ThreadID,
		Level:      "human_input",
		Message:    message,
		TurnNumber: req.TurnNumber,
		Metadata: marshalHumanInputLogMetadata(h.logger, map[string]interface{}{
			"event":                  string(sse.EventHumanInputUpdated),
			"human_input_request_id": req.ID.String(),
			"provider_request_id":    req.ProviderRequestID,
			"request_kind":           string(req.Kind),
			"status":                 string(status),
			"title":                  req.Title,
		}),
	}
	if err := h.logStore.Create(ctx, log); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("session_id", sessionID.String()).Str("request_id", req.ID.String()).Msg("failed to emit human input update log")
	}
}

func marshalHumanInputLogMetadata(logger zerolog.Logger, metadata map[string]interface{}) json.RawMessage {
	raw, err := json.Marshal(metadata)
	if err != nil {
		logger.Warn().Err(err).Msg("failed to marshal human input log metadata")
		return nil
	}
	return raw
}

// SendMessage handles POST /sessions/{id}/messages — sends a follow-up message
// to an idle multi-turn session and enqueues a continue_session job.
func (h *SessionHandler) SendMessage(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	if h.messageStore == nil {
		writeError(w, r, http.StatusNotImplemented, "NOT_CONFIGURED", "multi-turn sessions not configured")
		return
	}

	var body struct {
		Message                 string                         `json:"message"`
		Images                  []string                       `json:"images"`
		References              []models.SessionInputReference `json:"references"`
		Commands                []models.SessionInputCommand   `json:"commands"`
		PlanMode                bool                           `json:"plan_mode"`
		ResolveReviewCommentIDs []string                       `json:"resolve_review_comment_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	body.Message = strings.TrimSpace(body.Message)
	if body.Message == "" && len(body.Images) == 0 && len(body.References) == 0 {
		writeError(w, r, http.StatusBadRequest, "MISSING_MESSAGE", "message, images, or references are required")
		return
	}
	for _, reference := range body.References {
		if err := reference.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_REFERENCES", err.Error())
			return
		}
	}
	for _, command := range body.Commands {
		if err := command.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_COMMANDS", err.Error())
			return
		}
	}

	// Parse and dedupe optional review-comment IDs to resolve atomically with
	// the message create. Reject malformed UUIDs and overlong lists early so
	// the SQL phase only sees clean input.
	//
	// Authorization: SendMessage allows resolving any comment in the session,
	// regardless of authorship — the user is taking action on the comment by
	// addressing it in their message. This intentionally diverges from PATCH
	// /review-comments/{id}, which restricts edits to the comment's author.
	// Both surfaces share the same audit action so a downstream consumer can
	// still distinguish via the resolved_via_message flag in audit details.
	if len(body.ResolveReviewCommentIDs) > 0 && h.reviewCommentStore == nil {
		writeError(w, r, http.StatusBadRequest, "REVIEW_COMMENTS_NOT_CONFIGURED", "review comment resolution is not configured for this server")
		return
	}
	resolveCommentIDs, parseErr := parseAndDedupeReviewCommentIDs(body.ResolveReviewCommentIDs)
	if parseErr != nil {
		parseErr.write(w, r)
		return
	}

	// When plan mode is requested, prefix the message so the orchestrator
	// can detect it and instruct the coding agent to plan instead of execute.
	if body.PlanMode {
		body.Message = "[PLAN_MODE]\n" + body.Message
	}

	// Look up the session to check its current status.
	session, err := h.runStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}

	for _, command := range body.Commands {
		if command.AgentType != session.AgentType {
			writeError(w, r, http.StatusBadRequest, "COMMAND_AGENT_MISMATCH",
				fmt.Sprintf("command %q targets agent %q but session uses agent %q", command.Token, command.AgentType, session.AgentType))
			return
		}
	}

	// Reject early if the session's sandbox snapshot has been destroyed
	// (expired after 30 days). The session can no longer be resumed.
	if session.SandboxState == models.SandboxStateDestroyed {
		writeError(w, r, http.StatusGone, "SNAPSHOT_EXPIRED", "this session's environment has expired and can no longer be continued")
		return
	}
	if session.Status == models.SessionStatusAwaitingInput && body.Message == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_ANSWER", "answer text is required when replying to a pending session question")
		return
	}

	// Build the user message from the request context.
	user := middleware.UserFromContext(r.Context())
	var userID *uuid.UUID
	if user != nil {
		userID = &user.ID
	}
	msg := &models.SessionMessage{
		SessionID:  sessionID,
		OrgID:      orgID,
		UserID:     userID,
		TurnNumber: session.CurrentTurn + 1,
		Role:       models.MessageRoleUser,
		Content:    body.Message,
	}
	if len(body.Images) > 0 {
		msg.Attachments = body.Images
	}
	if len(body.References) > 0 {
		msg.References = body.References
	}
	if len(body.Commands) > 0 {
		msg.Commands = body.Commands
	}

	// Running-session fast path: no status change, no job, no question
	// handling. When there are no review comments to resolve, skip the tx
	// entirely so we don't take row locks on session_messages for the common
	// follow-up case. When there ARE comments to resolve, wrap both the
	// insert and the resolve in a tx so the two are atomic.
	if session.Status == models.SessionStatusRunning {
		if len(resolveCommentIDs) == 0 {
			if err := h.messageStore.Create(r.Context(), msg); err != nil {
				writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create message", err)
				return
			}
			h.maybeLinkLinearMidSession(r.Context(), orgID, sessionID, body.Message, linearReferenceText(body.References), userID)
			writeJSON(w, http.StatusCreated, models.SingleResponse[models.SessionMessage]{Data: *msg})
			return
		}

		tx, err := h.runStore.Begin(r.Context())
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "TX_BEGIN_FAILED", "failed to begin session transaction", err)
			return
		}
		committed := false
		defer func() {
			if committed {
				return
			}
			if rollbackErr := tx.Rollback(r.Context()); rollbackErr != nil {
				zerolog.Ctx(r.Context()).Error().Err(rollbackErr).Str("session_id", sessionID.String()).Msg("failed to rollback send message transaction")
			}
		}()
		txMessageStore := db.NewSessionMessageStore(tx)
		txReviewCommentStore := db.NewSessionReviewCommentStore(tx)

		if err := txMessageStore.Create(r.Context(), msg); err != nil {
			writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create message", err)
			return
		}
		resolvedComments, rerr := txReviewCommentStore.ValidateAndResolveByIDs(
			r.Context(), orgID, sessionID, resolveCommentIDs, currentResolutionPass(&session))
		if rerr != nil {
			if renderReviewCommentResolveError(w, r, rerr) {
				return
			}
			writeError(w, r, http.StatusInternalServerError, "REVIEW_COMMENT_RESOLVE_FAILED", "failed to resolve review comments", rerr)
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, r, http.StatusInternalServerError, "TX_COMMIT_FAILED", "failed to commit session follow-up", err)
			return
		}
		committed = true

		h.emitReviewCommentResolutionAudits(r, sessionID, msg.ID, resolvedComments)
		h.maybeLinkLinearMidSession(r.Context(), orgID, sessionID, body.Message, linearReferenceText(body.References), userID)
		writeJSON(w, http.StatusCreated, models.SingleResponse[models.SessionMessage]{Data: *msg})
		return
	}

	tx, err := h.runStore.Begin(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "TX_BEGIN_FAILED", "failed to begin session transaction", err)
		return
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := tx.Rollback(r.Context()); rollbackErr != nil {
			zerolog.Ctx(r.Context()).Error().Err(rollbackErr).Str("session_id", sessionID.String()).Msg("failed to rollback send message transaction")
		}
	}()

	txMessageStore := db.NewSessionMessageStore(tx)
	var txReviewCommentStore *db.SessionReviewCommentStore
	if len(resolveCommentIDs) > 0 {
		txReviewCommentStore = db.NewSessionReviewCommentStore(tx)
	}

	txRunStore := db.NewSessionStore(tx)
	txQuestionStore := db.NewSessionQuestionStore(tx)
	txHumanInputStore := db.NewSessionHumanInputRequestStore(tx)

	// Try claiming an idle session first, then fall back to resuming a
	// terminal session (completed/pr_created/failed/cancelled).
	var revertStatus models.SessionStatus
	claimed, claimErr := txRunStore.ClaimIdle(r.Context(), orgID, sessionID)
	if claimErr != nil {
		claimed, claimErr = txRunStore.ClaimForResume(r.Context(), orgID, sessionID)
		if claimErr != nil {
			writeError(w, r, http.StatusConflict, "NOT_RESUMABLE", "session must be idle, running, awaiting input, need guidance, or otherwise resumable to send a message")
			return
		}
		revertStatus = session.Status // preserve original status for revert
	} else {
		revertStatus = models.SessionStatusIdle
	}
	// Update turn number from the claimed session (may differ after status transition).
	session = claimed
	msg.TurnNumber = session.CurrentTurn + 1

	if err := txMessageStore.Create(r.Context(), msg); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create message", err)
		return
	}

	var resolvedComments []models.SessionReviewComment
	if txReviewCommentStore != nil {
		var rerr error
		resolvedComments, rerr = txReviewCommentStore.ValidateAndResolveByIDs(
			r.Context(), orgID, sessionID, resolveCommentIDs, currentResolutionPass(&session))
		if rerr != nil {
			if renderReviewCommentResolveError(w, r, rerr) {
				return
			}
			writeError(w, r, http.StatusInternalServerError, "REVIEW_COMMENT_RESOLVE_FAILED", "failed to resolve review comments", rerr)
			return
		}
	}

	// If the session was paused on a clarifying question, treat the follow-up
	// message as the answer so question state stays in sync with the resumed run.
	var answeredQuestion *models.SessionQuestion
	var answeredHumanInput *models.HumanInputRequest
	var humanInputRequestID *uuid.UUID
	if revertStatus == models.SessionStatusAwaitingInput && userID != nil && h.questionStore != nil {
		if h.humanInputStore != nil {
			request, err := txHumanInputStore.AnswerLatestPendingFreeTextBySession(r.Context(), orgID, sessionID, body.Message, *userID)
			if err != nil {
				if !errors.Is(err, pgx.ErrNoRows) {
					writeError(w, r, http.StatusInternalServerError, "ANSWER_FAILED", "failed to resolve pending human input request", err)
					return
				}
			} else {
				answeredHumanInput = &request
				humanInputRequestID = &request.ID
			}
		}
		question, err := txQuestionStore.AnswerLatestPendingBySession(r.Context(), orgID, sessionID, body.Message, *userID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				zerolog.Ctx(r.Context()).Warn().Str("session_id", sessionID.String()).Msg("awaiting_input session resumed without a pending question to answer")
			} else {
				writeError(w, r, http.StatusInternalServerError, "ANSWER_FAILED", "failed to resolve pending session question", err)
				return
			}
		} else {
			answeredQuestion = &question
		}
	}

	// Enqueue continue_session job. Dedupe at the session level — only one
	// continue_session can hold the shared sandbox at a time, so a duplicate
	// would race AcquireTurnHold and dead-letter via the orchestrator's
	// self-heal path. Pin to the worker that owns the live sandbox so the
	// job can't be claimed by a sibling node whose docker daemon doesn't
	// know about the container_id.
	dedupeKey := db.ContinueSessionDedupeKey(sessionID)
	payload := map[string]string{
		"session_id": sessionID.String(),
		"org_id":     orgID.String(),
	}
	if humanInputRequestID != nil {
		payload["human_input_request_id"] = humanInputRequestID.String()
	}
	if _, err := h.jobStore.EnqueueInTxWithOpts(r.Context(), tx, orgID, db.EnqueueOpts{
		Queue:        "agent",
		JobType:      "continue_session",
		Payload:      payload,
		Priority:     5,
		DedupeKey:    &dedupeKey,
		TargetNodeID: models.SessionWorkerTarget(&session),
	}); err != nil {
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue continue_session job", err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, r, http.StatusInternalServerError, "TX_COMMIT_FAILED", "failed to commit session follow-up", err)
		return
	}
	committed = true

	if answeredQuestion != nil {
		qIDStr := answeredQuestion.ID.String()
		questionDetails := map[string]any{
			"question_id":   answeredQuestion.ID.String(),
			"session_id":    answeredQuestion.SessionID.String(),
			"question_text": answeredQuestion.QuestionText,
			"status":        answeredQuestion.Status,
			"answer_length": len(body.Message),
			"answered_by":   userID.String(),
			"option_count":  len(answeredQuestion.Options),
			"auto_answered": true,
		}
		if answeredQuestion.BlocksPhase != nil {
			questionDetails["blocks_phase"] = *answeredQuestion.BlocksPhase
		}
		emitUserAuditWithSession(h.audit, r, models.AuditActionSessionQuestionAnswered, models.AuditResourceSession, &qIDStr, &sessionID, nil,
			marshalAuditDetails(h.logger, questionDetails))
	}
	if answeredHumanInput != nil {
		requestIDStr := answeredHumanInput.ID.String()
		emitUserAuditWithSession(h.audit, r, models.AuditActionSessionHumanInputAnswered, models.AuditResourceSession, &requestIDStr, &sessionID, nil,
			marshalAuditDetails(h.logger, map[string]any{
				"request_id":    answeredHumanInput.ID.String(),
				"session_id":    answeredHumanInput.SessionID.String(),
				"request_kind":  string(answeredHumanInput.Kind),
				"status":        string(answeredHumanInput.Status),
				"answer_length": len(body.Message),
				"answered_by":   userID.String(),
				"auto_answered": true,
			}))
	}
	h.emitReviewCommentResolutionAudits(r, sessionID, msg.ID, resolvedComments)
	h.maybeLinkLinearMidSession(r.Context(), orgID, sessionID, body.Message, linearReferenceText(body.References), userID)

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.SessionMessage]{Data: *msg})
}

// midSessionLinkTimeout caps the detached-context window we give the
// fire-and-forget mid-session linker. Detached from the request context so a
// user closing their browser tab between SendMessage's writeJSON and the
// allowlist+enqueue calls doesn't cancel the link work; bounded so a wedged
// DB connection can't hold the goroutine forever.
const midSessionLinkTimeout = 30 * time.Second

// maybeLinkLinearMidSession kicks off detection + async enqueue for a
// follow-up message body in a detached goroutine. It runs off the request
// path so SendMessage's running-session fast path stays free of the extra
// allowlist read and job-insert it would otherwise add to user-perceived
// latency. Failures are logged but never surface to the caller — the message
// has already been committed and the agent will see Linear refs as text
// regardless of whether the side-band link row gets created. This mirrors the
// design 62 fail-soft contract on the async post-create catch-up path.
func (h *SessionHandler) maybeLinkLinearMidSession(ctx context.Context, orgID, sessionID uuid.UUID, messageBody, referenceText string, userID *uuid.UUID) {
	linker := h.getLinearLinker()
	if linker == nil {
		return
	}
	detached := context.WithoutCancel(ctx)
	go func() {
		bgCtx, cancel := context.WithTimeout(detached, midSessionLinkTimeout)
		defer cancel()
		if err := linker.ResolveAndLinkMidSession(bgCtx, linear.MidSessionInput{
			OrgID:         orgID,
			SessionID:     sessionID,
			MessageBody:   messageBody,
			ReferenceText: referenceText,
			UserID:        userID,
		}); err != nil {
			h.logger.Warn().Err(err).
				Str("session_id", sessionID.String()).
				Msg("mid-session linear linking failed; follow-up message was sent but no link row was created")
		}
	}()
}

// emitReviewCommentResolutionAudits records one audit row per comment whose
// resolved state actually changed. Wraps the shared audit emitter on the
// SessionHandler so this surface stays consistent with the rest of
// SendMessage's audit shape.
func (h *SessionHandler) emitReviewCommentResolutionAudits(
	r *http.Request,
	sessionID uuid.UUID,
	messageID int64,
	resolved []models.SessionReviewComment,
) {
	emitReviewCommentResolutionAudits(h.audit, h.logger, r, sessionID, messageID, resolved)
}

// ListMessages handles GET /sessions/{id}/messages — returns the conversation messages.
func (h *SessionHandler) ListMessages(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	if h.messageStore == nil {
		writeJSON(w, http.StatusOK, models.ListResponse[models.SessionMessage]{Data: []models.SessionMessage{}})
		return
	}

	// Verify session exists and belongs to org.
	_, err = h.runStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}

	messages, err := h.messageStore.ListBySession(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list messages", err)
		return
	}
	if messages == nil {
		messages = []models.SessionMessage{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.SessionMessage]{Data: messages})
}

// GetTimeline handles GET /sessions/{id}/timeline — returns the server-owned session timeline.
func (h *SessionHandler) GetTimeline(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	_, err = h.runStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}

	var messages []models.SessionMessage
	if h.messageStore != nil {
		messages, err = h.messageStore.ListBySession(r.Context(), orgID, sessionID)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list messages", err)
			return
		}
	}
	if messages == nil {
		messages = []models.SessionMessage{}
	}

	logs, err := h.logStore.ListByRunID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list logs", err)
		return
	}
	if logs == nil {
		logs = []models.SessionLog{}
	}

	var humanInputs []models.HumanInputRequest
	if h.humanInputStore != nil {
		humanInputs, err = h.humanInputStore.ListBySession(r.Context(), orgID, sessionID, db.HumanInputRequestFilters{})
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list human input requests", err)
			return
		}
	}
	if humanInputs == nil {
		humanInputs = []models.HumanInputRequest{}
	}

	timeline := sessiontimeline.Compose(messages, logs, humanInputs)
	if timeline == nil {
		timeline = []models.SessionTimelineEntry{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.SessionTimelineResponseEntry]{
		Data: models.NewSessionTimelineResponseEntries(timeline),
	})
}

// EndSession handles POST /sessions/{id}/end — explicitly ends an idle session.
func (h *SessionHandler) EndSession(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	session, err := h.runStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}

	if session.Status != models.SessionStatusIdle {
		writeError(w, r, http.StatusConflict, "NOT_IDLE", "only idle sessions can be ended")
		return
	}

	if err := h.runStore.UpdateStatus(r.Context(), orgID, sessionID, models.SessionStatusCompleted); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to end session", err)
		return
	}

	changesetID, err := h.primaryChangesetID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PRIMARY_CHANGESET_FAILED", "failed to resolve the primary pull request target", err)
		return
	}
	payload := map[string]string{
		"session_id":   sessionID.String(),
		"changeset_id": changesetID.String(),
		"org_id":       orgID.String(),
	}
	if h.issueSnapshots != nil && session.CurrentTurn > 0 {
		if issueSnapshot, snapErr := h.issueSnapshots.GetByTurn(r.Context(), orgID, sessionID, session.CurrentTurn); snapErr == nil {
			payload["issue_snapshot_id"] = issueSnapshot.ID.String()
		}
	}
	dedupeKey := db.OpenPRDedupeKey(changesetID)
	queued, err := h.enqueuePublishActionInTx(
		r.Context(),
		orgID,
		sessionID,
		"default",
		"open_pr",
		payload,
		dedupeKey,
		func(ctx context.Context, sessions *db.SessionStore, changesets *db.SessionChangesetStore) (bool, error) {
			if h.changesetStore != nil {
				return changesets.TryMarkPRCreationQueued(ctx, orgID, sessionID, changesetID)
			}
			return sessions.TryMarkPRCreationQueued(ctx, orgID, sessionID)
		},
	)
	if err != nil {
		if txErr := (*publishActionTxError)(nil); errors.As(err, &txErr) && txErr.phase == "state" {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to mark PR creation as queued", err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue PR creation", err)
		return
	}
	if !queued {
		writeError(w, r, http.StatusConflict, "PR_IN_FLIGHT", "PR creation already in progress")
		return
	}

	// Snapshot cleanup is handled by the reaper, which will find this session
	// because it's now status=completed with sandbox_state=snapshotted.

	session.Status = models.SessionStatusCompleted
	h.enrichSessionLinks(r.Context(), orgID, &session)
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Session]{Data: session})
}

// CancelSession handles POST /sessions/{id}/cancel — cancels a running session
// by signalling the orchestrator to send SIGINT to the agent process.
//
// The response returns the session in its current state (still "running").
// The orchestrator updates the status asynchronously once the agent exits —
// typically to "idle" (if snapshot succeeds) or "cancelled" (if not).
// The frontend should poll for the final status.
func (h *SessionHandler) CancelSession(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	session, err := h.runStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}

	if session.Status != models.SessionStatusRunning {
		writeError(w, r, http.StatusConflict, "NOT_RUNNING", "only running sessions can be cancelled")
		return
	}

	if h.canceller == nil {
		writeError(w, r, http.StatusServiceUnavailable, "CANCEL_UNAVAILABLE", "session cancellation is not available")
		return
	}

	if err := h.runStore.RequestCancel(r.Context(), orgID, sessionID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CANCEL_REQUEST_FAILED", "failed to record cancel request", err)
		return
	}
	if body.Reason == "auto_repair_stop" {
		metrics.RecordPRAutoRepairStop(r.Context(), orgID.String(), "")
	}

	// Signal the orchestrator to send SIGINT to the agent.
	// The orchestrator will update the session status asynchronously when the
	// agent execution terminates (to idle or cancelled).
	if h.canceller.CancelSession(sessionID) {
		if _, err := consumeSessionCancelRequestDetached(r.Context(), h.runStore, orgID, sessionID); err != nil {
			h.logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to consume locally delivered session cancel request")
		}
	} else {
		// The session is marked as running but isn't tracked in this process'
		// registry. In production the API process is often not the worker that
		// owns the live agent, so route a best-effort cancel job to the recorded
		// sandbox worker before returning 202.
		h.logger.Warn().
			Str("session_id", sessionID.String()).
			Msg("cancel requested but session not found in local cancel registry")
		routed, err := h.routeRemoteCancel(r.Context(), orgID, session)
		if err != nil {
			h.logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to route cancel directly to worker")
		}
		if routed {
			writeJSON(w, http.StatusAccepted, models.SingleResponse[models.Session]{Data: session})
			return
		}
		if err := h.enqueueRemoteCancel(r.Context(), orgID, session); err != nil {
			writeError(w, r, http.StatusInternalServerError, "CANCEL_ENQUEUE_FAILED", "failed to route cancel to worker", err)
			return
		}
	}

	// Return the session as-is (still "running"). The status will be updated
	// asynchronously by the orchestrator once the agent exits.
	writeJSON(w, http.StatusAccepted, models.SingleResponse[models.Session]{Data: session})
}

func consumeSessionCancelRequestDetached(ctx context.Context, sessions *db.SessionStore, orgID, sessionID uuid.UUID) (bool, error) {
	if sessions == nil {
		return false, nil
	}
	consumeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return sessions.ConsumeCancelRequest(consumeCtx, orgID, sessionID)
}

func stringPtrValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func (h *SessionHandler) routeRemoteCancel(ctx context.Context, orgID uuid.UUID, session models.Session) (bool, error) {
	targetNodeID := models.SessionWorkerTarget(&session)
	if targetNodeID == nil || h.workerSelector == nil || h.workerClient == nil {
		return false, nil
	}
	if h.localNodeID != "" && *targetNodeID == h.localNodeID {
		return false, nil
	}
	worker, err := h.workerSelector.ResolveNode(ctx, *targetNodeID)
	if err != nil {
		return false, err
	}
	resp, err := h.workerClient.CancelSession(ctx, worker, previewsvc.RemoteCancelSessionRequest{
		OrgID:     orgID,
		SessionID: session.ID,
	})
	if err != nil {
		return false, err
	}
	return resp != nil && resp.Accepted, nil
}

func (h *SessionHandler) enqueueRemoteCancel(ctx context.Context, orgID uuid.UUID, session models.Session) error {
	targetNodeID := models.SessionWorkerTarget(&session)
	if targetNodeID == nil {
		h.logger.Warn().
			Str("session_id", session.ID.String()).
			Msg("cancel requested for running session without recorded worker target")
		return nil
	}
	if h.jobStore == nil {
		h.logger.Warn().
			Str("session_id", session.ID.String()).
			Str("worker_node_id", *targetNodeID).
			Msg("cancel requested but job store unavailable for worker routing")
		return nil
	}
	dedupeKey := "cancel_session:" + session.ID.String()
	payload := map[string]string{
		"session_id": session.ID.String(),
		"org_id":     orgID.String(),
	}
	if _, err := h.jobStore.EnqueueWithOpts(ctx, orgID, db.EnqueueOpts{
		Queue:        "agent",
		JobType:      "cancel_session",
		Payload:      payload,
		Priority:     10,
		DedupeKey:    &dedupeKey,
		TargetNodeID: targetNodeID,
	}); err != nil {
		return fmt.Errorf("enqueue cancel_session: %w", err)
	}
	return nil
}

func isTerminalStatus(status models.SessionStatus) bool {
	switch status {
	case "completed", "pr_created", "failed", "cancelled", "skipped":
		return true
	}
	return false
}

// CreateManual creates a new manual session from a user-provided message.
func (h *SessionHandler) CreateManual(w http.ResponseWriter, r *http.Request) {
	h.createManual(w, r, models.SessionOriginManual, false)
}

// CreateExternal creates a session through the service-account API.
func (h *SessionHandler) CreateExternal(w http.ResponseWriter, r *http.Request) {
	h.createManual(w, r, models.SessionOriginExternalAPI, true)
}

func (h *SessionHandler) createManual(w http.ResponseWriter, r *http.Request, origin models.SessionOrigin, requireRepository bool) {
	orgID := middleware.OrgIDFromContext(r.Context())

	var body struct {
		Message         string                         `json:"message"`
		Attachments     []string                       `json:"attachments"`
		Images          []string                       `json:"images"`
		References      []models.SessionInputReference `json:"references"`
		Commands        []models.SessionInputCommand   `json:"commands"`
		AgentType       string                         `json:"agent_type"`
		Model           string                         `json:"model"`
		ReasoningEffort string                         `json:"reasoning_effort"`
		AutonomyLevel   string                         `json:"autonomy_level"`
		TokenMode       string                         `json:"token_mode"`
		RepositoryID    string                         `json:"repository_id"`
		Branch          string                         `json:"branch"`
		TargetBranch    string                         `json:"target_branch"`
		Metadata        json.RawMessage                `json:"metadata"`
		// LinearPrivate suppresses every Linear write; the agent still gets
		// linked-issue context locally. Frozen at session create.
		LinearPrivate bool `json:"linear_private,omitempty"`
		// LinearStateSyncDisabled gates only workflow-state transitions
		// (issue moved to "In Progress" / "In Review" / "Done"). The
		// attachment + rolling comment still post — they are visibility
		// signals, not state mutation. Use LinearPrivate to suppress all
		// Linear writes.
		LinearStateSyncDisabled bool `json:"linear_state_sync_disabled,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	body.Message = strings.TrimSpace(body.Message)
	if len(body.Images) == 0 && len(body.Attachments) > 0 {
		body.Images = body.Attachments
	}
	// Empty message is allowed when the user attached images or is starting
	// from a linked Linear issue. Mobile screenshot/photo-first flows rely on
	// this: the UI explicitly advertises image-only session starts, and the
	// initial turn persists those attachments even when the text prompt is
	// blank. Linear detection runs after body validation, so we accept
	// "looks like a Linear ref somewhere in the inputs" as the relaxation
	// signal here. The check is tightened with the team-key allowlist so a
	// "FOO-123"-shaped string for an unknown team no longer waves the request
	// past — preventing sessions with an empty user turn that silently no-op
	// inside the linker.
	if body.Message == "" && len(body.Images) == 0 && !h.canBypassMissingMessageForLinear(r.Context(), orgID, body.References) {
		writeError(w, r, http.StatusBadRequest, "MISSING_MESSAGE", "message, images, or a linked Linear issue are required")
		return
	}
	if requireRepository && strings.TrimSpace(body.RepositoryID) == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_REPOSITORY_ID", "repository_id is required")
		return
	}
	for _, reference := range body.References {
		if err := reference.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_REFERENCES", err.Error())
			return
		}
	}
	for _, command := range body.Commands {
		if err := command.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_COMMANDS", err.Error())
			return
		}
	}

	// Resolve repository for the manual session so the orchestrator can
	// clone the codebase into the sandbox.
	var repoID *uuid.UUID
	if body.RepositoryID != "" {
		parsed, err := uuid.Parse(body.RepositoryID)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", "invalid repository_id")
			return
		}
		if _, err := requireActiveRepo(r.Context(), h.repoStore, orgID, parsed); err != nil {
			switch {
			case errors.Is(err, errRepoDisconnected):
				writeError(w, r, http.StatusBadRequest, "REPO_DISCONNECTED", "repository is disconnected; reconnect it to start new sessions")
			case errors.Is(err, errRepoStoreUnconfigured):
				writeError(w, r, http.StatusInternalServerError, "REPO_STORE_UNCONFIGURED", "repository lookup not configured")
			default:
				writeError(w, r, http.StatusNotFound, "REPOSITORY_NOT_FOUND", "repository not found")
			}
			return
		}
		if token := middleware.APITokenFromContext(r.Context()); token != nil && !apiTokenAllowsRepository(token, parsed) {
			writeError(w, r, http.StatusForbidden, "FORBIDDEN", "API token is not allowed to access this repository")
			return
		}
		repoID = &parsed
	}

	var targetBranch *string
	branchName := firstNonEmptyString(body.TargetBranch, body.Branch)
	if branchName != "" {
		b := strings.TrimSpace(branchName)
		if !isValidGitRef(b) {
			writeError(w, r, http.StatusBadRequest, "INVALID_BRANCH", "branch contains invalid characters")
			return
		}
		targetBranch = &b
	}

	org, err := h.orgStore.GetByID(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "DEFAULT_AGENT_LOOKUP_FAILED", "failed to load organization settings", err)
		return
	}
	orgSettings, parseErr := models.ParseOrgSettings(org.Settings)
	if parseErr != nil {
		zerolog.Ctx(r.Context()).Warn().Err(parseErr).Msg("failed to parse org settings, using defaults")
	}

	// Admin gate on the per-session Linear policy flags. EffectiveAllow-
	// PerSessionOverrides() resolves nil → true, so the gate is permissive
	// by default: an org with no LinearAutomation settings (or with
	// allow_per_session_overrides explicitly absent) lets users opt out of
	// Linear writes on a per-session basis. Admins enforce "every session
	// must sync to Linear" by setting allow_per_session_overrides=false
	// explicitly — only that case rejects requests. Read the double-negative
	// here as: "block if the user wants to silence writes AND the admin has
	// explicitly forbidden per-session overrides."
	if (body.LinearPrivate || body.LinearStateSyncDisabled) && !orgSettings.LinearAutomation.EffectiveAllowPerSessionOverrides() {
		writeError(w, r, http.StatusForbidden, "LINEAR_PER_SESSION_OVERRIDES_DISABLED", "linear_private and linear_state_sync_disabled are not permitted for this organization")
		return
	}

	agentType := models.AgentType(body.AgentType)
	if body.Model == "" && agentType == "" && h.userStore != nil {
		if user := middleware.UserFromContext(r.Context()); user != nil {
			userWithSettings, err := h.userStore.GetByIDGlobalWithSettings(r.Context(), user.ID)
			if err != nil {
				zerolog.Ctx(r.Context()).Warn().Err(err).Msg("failed to load user settings for default model")
			} else if userWithSettings.Settings.CodingAgentModelDefault != "" {
				resolvedAgentType := models.AgentTypeForModel(userWithSettings.Settings.CodingAgentModelDefault)
				if resolvedAgentType != "" {
					body.Model = userWithSettings.Settings.CodingAgentModelDefault
					agentType = resolvedAgentType
				}
			}
		}
	}
	if agentType == "" {
		agentType = orgSettings.DefaultAgentType
		if agentType == "" {
			agentType = models.DefaultDefaultAgentType
		}
	}
	if err := agentType.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_AGENT_TYPE", err.Error())
		return
	}

	for _, command := range body.Commands {
		if command.AgentType != agentType {
			writeError(w, r, http.StatusBadRequest, "COMMAND_AGENT_MISMATCH",
				fmt.Sprintf("command %q targets agent %q but session uses agent %q", command.Token, command.AgentType, agentType))
			return
		}
	}

	var modelOverride *string
	if body.Model != "" {
		if err := models.ValidateModelForAgentType(agentType, body.Model); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_MODEL", err.Error())
			return
		}
		modelOverride = &body.Model
	}

	reasoningOverride, err := parseReasoningEffortForAgent(agentType, body.ReasoningEffort)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REASONING_EFFORT", err.Error())
		return
	}

	autonomyLevel := body.AutonomyLevel
	if autonomyLevel == "" {
		autonomyLevel = string(models.DefaultSessionAutonomy)
	}
	if err := models.SessionAutonomy(autonomyLevel).Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_AUTONOMY_LEVEL", "autonomy_level must be one of: full, semi, supervised")
		return
	}

	tokenMode := body.TokenMode
	if tokenMode == "" {
		tokenMode = "low"
	}
	validTokenModes := map[string]bool{"low": true, "high": true}
	if !validTokenModes[tokenMode] {
		writeError(w, r, http.StatusBadRequest, "INVALID_TOKEN_MODE", "token_mode must be one of: low, high")
		return
	}

	title := manualSessionTitle(body.Message)

	var manualTriggeredByUserID *uuid.UUID
	if user := middleware.UserFromContext(r.Context()); user != nil {
		manualTriggeredByUserID = &user.ID
	}

	session := &models.Session{
		OrgID:                   orgID,
		Origin:                  origin,
		InteractionMode:         models.SessionInteractionModeInteractive,
		ValidationPolicy:        models.SessionValidationPolicyOnSessionEnd,
		AgentType:               agentType,
		Status:                  models.SessionStatusPending,
		AutonomyLevel:           models.SessionAutonomy(autonomyLevel),
		TokenMode:               models.SessionTokenMode(tokenMode),
		ModelOverride:           modelOverride,
		ReasoningEffort:         reasoningOverride,
		TriggeredByUserID:       manualTriggeredByUserID,
		Title:                   &title,
		PMApproach:              &title,
		TargetBranch:            targetBranch,
		RepositoryID:            repoID,
		LinearPrivate:           body.LinearPrivate,
		LinearStateSyncDisabled: body.LinearStateSyncDisabled,
	}
	if h.capabilityService != nil {
		snapshot, err := h.capabilityService.ResolveForSession(r.Context(), agentcapabilities.ResolveInput{
			OrgID:         orgID,
			RepositoryID:  repoID,
			SessionOrigin: origin,
		})
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "CAPABILITY_RESOLUTION_FAILED", "failed to resolve session capabilities", err)
			return
		}
		session.CapabilitySnapshot = snapshot
	}
	if err := h.runStore.Create(r.Context(), session); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create manual session", err)
		return
	}
	if origin == models.SessionOriginExternalAPI && h.attributionStore != nil && len(body.Metadata) > 0 && string(body.Metadata) != "null" {
		attribution := &models.SessionAttribution{
			OrgID:          orgID,
			SessionID:      session.ID,
			Source:         models.SessionAttributionSourceExternalAPI,
			SourceMetadata: body.Metadata,
		}
		if err := h.attributionStore.Create(r.Context(), attribution); err != nil {
			zerolog.Ctx(r.Context()).Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to create external api session attribution")
		}
	}

	// Persist the initial user message as a turn-0 record so that attachments
	// (uploaded images) are displayed alongside the prompt in the chat timeline.
	if h.messageStore != nil {
		initMsg := &models.SessionMessage{
			SessionID:  session.ID,
			OrgID:      orgID,
			ThreadID:   session.PrimaryThreadID,
			TurnNumber: 0,
			Role:       models.MessageRoleUser,
			Content:    body.Message,
		}
		if user := middleware.UserFromContext(r.Context()); user != nil {
			initMsg.UserID = &user.ID
		}
		if len(body.Images) > 0 {
			initMsg.Attachments = body.Images
		}
		if len(body.References) > 0 {
			initMsg.References = body.References
		}
		if len(body.Commands) > 0 {
			initMsg.Commands = body.Commands
		}
		if err := h.messageStore.Create(r.Context(), initMsg); err != nil {
			zerolog.Ctx(r.Context()).Warn().Err(err).Msg("failed to create initial session message — continuing without it")
		}
	}

	// Check concurrency before any Linear linking side effects. The session
	// row and turn-0 message are already persisted to preserve historical
	// CreateManual behavior, but we must not link or post to Linear for a run
	// that will never be enqueued.
	runningCount, err := h.runStore.CountRunningByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CONCURRENCY_CHECK_FAILED", "failed to check running sessions", err)
		return
	}
	maxConcurrent := orgSettings.MaxConcurrentRuns
	if maxConcurrent <= 0 {
		maxConcurrent = models.DefaultMaxConcurrentRuns
	}
	if runningCount >= maxConcurrent {
		writeError(w, r, http.StatusTooManyRequests, "CONCURRENCY_LIMIT",
			fmt.Sprintf("Too many sessions running (%d/%d). Please wait for a session to finish before starting a new one.", runningCount, maxConcurrent))
		return
	}

	// Resolve linked Linear issues before enqueueing the agent run. Per
	// design 62 §"Pre-start preparation step", turn 1 cannot start until the
	// primary Linear issue has its context snapshot captured — but the
	// session-create response should still come back fast. The linker
	// chooses inline vs queued based on a strict latency budget.
	//
	// Repo-less sessions (issue-only start, design 62 §"Issue-only session
	// start") still get linked: SessionIssueLinkStore.CreateAllowingNullRepo
	// already accepts a NULL repository on the link row when the underlying
	// session has no repo, so we don't pre-filter on body.RepositoryID here.
	linker := h.getLinearLinker()
	if linker == nil && (body.LinearPrivate || body.LinearStateSyncDisabled) {
		// User asked for Linear-aware behavior but no Linear integration is
		// wired into this handler. The flags are persisted (so a later
		// install backfills the right policy), but nothing will read them
		// until then — surface this to dogfooders rather than failing silently.
		//
		// Intentionally NOT a 4xx: the design treats these flags as policy
		// hints captured at the moment of intent. Rejecting the request when
		// the integration is absent would push operators to either re-create
		// sessions after install or pollute clients with feature-flag
		// branching. A future cleanup pass that "fixes" this by returning
		// 422 should not — the persisted flags are the contract.
		h.logger.Warn().
			Str("session_id", session.ID.String()).
			Bool("linear_private", body.LinearPrivate).
			Bool("linear_state_sync_disabled", body.LinearStateSyncDisabled).
			Msg("linear policy flags set on session but no Linear integration is configured; flags persisted but currently unused")
	}
	if linker != nil {
		userID := manualTriggeredByUserID
		linearResult, linkErr := linker.ResolveAndLinkAtCreate(r.Context(), linear.CreateInput{
			OrgID:                   orgID,
			SessionID:               session.ID,
			MessageBody:             body.Message,
			SessionTitle:            title,
			BranchName:              body.Branch,
			ReferenceText:           linearReferenceText(body.References),
			RepositoryID:            repoID,
			UserID:                  userID,
			LinearPrivate:           body.LinearPrivate,
			LinearStateSyncDisabled: body.LinearStateSyncDisabled,
		})
		if linkErr != nil {
			errMsg := "Linear context could not be prepared. Retry the session after the Linear integration recovers."
			// Sidebar rendering contract: session-sidebar.tsx displays
			// `session.failure_explanation || session.error` for any row
			// with status="failed", and sessionTitle() falls back to
			// "Session XXXXXXXX" when the title is empty. So an
			// orphaned-failed Linear session — title set from
			// manualSessionTitle, no agent run, this errMsg in
			// SessionResult.Error — renders cleanly with both the title
			// and the user-visible explanation. Don't change errMsg to a
			// raw stack-trace shape without also updating that contract.
			//
			// Use a detached context for the failure write so a cancelled
			// request context (client disconnect) doesn't leave the session
			// stuck in "pending" forever — the agent run hasn't been
			// enqueued yet, so without this transition the row is orphaned.
			//
			// Backstop: if this 5s detached write also fails (DB hiccup,
			// process death), agent.SessionReaper Phase 0
			// (FailureCategoryStuckPending, 10-minute cutoff) sweeps stale
			// pending rows on its next tick. The detached write is the
			// fast path that surfaces the failure to the user immediately;
			// the reaper is the eventual-consistency safety net.
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := h.runStore.UpdateResult(cleanupCtx, orgID, session.ID, models.SessionStatusFailed, &models.SessionResult{Error: &errMsg}); err != nil {
				h.logger.Error().Err(err).Str("session_id", session.ID.String()).Msg("failed to mark session failed after linear pre-start linking error; SessionReaper Phase 0 will reap the stuck-pending row within max_pending_age")
			}
			cancel()
			h.logger.Warn().Err(linkErr).Str("session_id", session.ID.String()).Msg("linear pre-start linking failed; refusing to enqueue agent run")
			writeError(w, r, http.StatusInternalServerError, "LINEAR_PREPARE_FAILED", "failed to prepare Linear context", linkErr)
			return
		}
		if linearResult.PrimaryIdentifier != "" {
			if err := h.runStore.SetLinearIdentifierHint(r.Context(), orgID, session.ID, linearResult.PrimaryIdentifier); err != nil {
				h.logger.Warn().Err(err).Str("session_id", session.ID.String()).Str("identifier", linearResult.PrimaryIdentifier).Msg("failed to persist linear identifier hint; branch naming will fall back to non-linear slug")
			} else {
				session.LinearIdentifierHint = &linearResult.PrimaryIdentifier
			}
			// Use the Linear issue title as the session title when the user
			// gave us nothing useful. Honors design 62 §"Issue-only session
			// start".
			if linearResult.PrimaryTitle != "" && shouldOverrideTitleWithLinearIssue(session.Title) {
				newTitle := linearResult.PrimaryTitle
				if err := h.runStore.UpdateTitleWithSource(r.Context(), orgID, session.ID, newTitle, models.SessionTitleSourceIssue); err != nil {
					h.logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to override session title with linear issue title; keeping placeholder title")
				} else {
					session.Title = &newTitle
				}
			}
		}
	}

	dedupeKey := db.RunAgentDedupeKey(session.ID)
	payload := db.RunAgentPayload(session)
	if _, err := h.jobStore.Enqueue(r.Context(), orgID, "agent", "run_agent", payload, 5, &dedupeKey); err != nil {
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue manual session", err)
		return
	}

	// Generate a concise session title via LLM (with a short timeout so the
	// request doesn't block for too long).
	if h.llmClient != nil && body.Message != "" {
		if err := h.generateSessionTitle(r.Context(), session, orgID, body.Message, models.SessionTitleSourceGenerated); err != nil {
			writeError(w, r, http.StatusInternalServerError, "TITLE_GENERATION_FAILED", "failed to generate session title", err)
			return
		}
	}

	manualSessionIDStr := session.ID.String()
	h.enrichSessionLinks(r.Context(), orgID, session)
	emitUserAuditWithSession(h.audit, r, models.AuditActionSessionCreated, models.AuditResourceSession, &manualSessionIDStr, &session.ID, nil,
		sessionCreateAuditDetails(h.logger, session, nil, map[string]any{
			"manual_session": origin == models.SessionOriginManual,
			"external_api":   origin == models.SessionOriginExternalAPI,
			"image_count":    len(body.Images),
		}))
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.Session]{Data: *session})
}

func (h *SessionHandler) generateSessionTitle(parent context.Context, session *models.Session, orgID uuid.UUID, message string, source models.SessionTitleSource) error {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()

	generated, err := h.llmClient.Complete(ctx, prompts.SessionTitleGenerationPrompt(), message)
	if err != nil {
		return fmt.Errorf("llm completion: %w", err)
	}

	title, ok := services.CleanTitle(generated)
	if !ok {
		return nil
	}

	if err := h.runStore.UpdateTitleWithSource(ctx, orgID, session.ID, title, source); err != nil {
		return fmt.Errorf("update title: %w", err)
	}
	session.Title = &title
	return nil
}

func buildManualSessionDescription(message string, images []string) string {
	if len(images) == 0 {
		return message
	}

	var b strings.Builder
	b.WriteString(message)
	b.WriteString("\n\n### Attached images\n")
	for _, imageURL := range images {
		if strings.TrimSpace(imageURL) == "" {
			continue
		}
		b.WriteString("- ")
		b.WriteString(imageURL)
		b.WriteString("\n")
	}

	return strings.TrimSpace(b.String())
}

// defaultManualSessionTitle is the placeholder title we apply when a manual
// session has no message body to derive a title from. The Linear-issue-only
// fast path overwrites this with the linked issue's title, which means the
// constant doubles as a sentinel — exposed for that comparison.
//
// CAUTION: shouldOverrideTitleWithLinearIssue compares trimmed titles to
// this exact literal. Renaming the constant — including for i18n or copy
// updates — silently changes which historical sessions are eligible for
// Linear-title overwrite (every existing row whose title still equals the
// old literal will stop matching). If this value ever needs to change,
// migrate existing rows in the same change or switch to a non-displayable
// sentinel column rather than a string compare.
const defaultManualSessionTitle = "Manual Session"

func manualSessionTitle(message string) string {
	trimmed := strings.TrimSpace(strings.ToValidUTF8(message, ""))
	if trimmed == "" {
		return defaultManualSessionTitle
	}

	if idx := strings.Index(trimmed, "\n"); idx > 0 {
		trimmed = trimmed[:idx]
	}

	return truncateUTF8Title(trimmed, 120)
}

func truncateUTF8Title(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	truncated := value[:maxBytes]
	for len(truncated) > 0 && !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return strings.TrimSpace(truncated) + "..."
}

// shouldOverrideTitleWithLinearIssue reports whether a Linear primary
// resolution should overwrite the session title. We replace the title only
// when the user gave us nothing useful — empty, whitespace, or the
// default placeholder — never when the user typed a real subject line.
func shouldOverrideTitleWithLinearIssue(current *string) bool {
	if current == nil {
		return true
	}
	trimmed := strings.TrimSpace(*current)
	if trimmed == "" {
		return true
	}
	return trimmed == defaultManualSessionTitle
}

// ArchiveSession marks a session as archived, hiding it from default list views.
func (h *SessionHandler) ArchiveSession(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user not authenticated")
		return
	}
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	var auditDetails json.RawMessage
	var auditLoadErr error
	var snapshotKey *string
	// Load the session once up front for archive auditing and snapshot cleanup.
	if h.audit != nil || h.snapshotStore != nil {
		session, err := h.runStore.GetByID(r.Context(), orgID, sessionID)
		if err != nil {
			if h.audit != nil {
				auditLoadErr = err
			}
		} else {
			snapshotKey = session.SnapshotKey
			if h.audit != nil {
				auditDetails = sessionArchiveAuditDetails(h.logger, &session, models.AuditActionSessionArchived, &user.ID)
			}
		}
	}

	if err := h.runStore.Archive(r.Context(), orgID, sessionID, user.ID); err != nil {
		if errors.Is(err, db.ErrSessionAlreadyArchived) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found or already archived")
		} else {
			zerolog.Ctx(r.Context()).Error().
				Err(err).
				Str("session_id", sessionID.String()).
				Str("org_id", orgID.String()).
				Str("user_id", user.ID.String()).
				Msg("failed to archive session")
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to archive session", err)
		}
		return
	}
	h.enqueueSlackArchiveReactionIfLinked(r.Context(), orgID, sessionID)
	if auditLoadErr != nil {
		zerolog.Ctx(r.Context()).Warn().
			Err(auditLoadErr).
			Str("session_id", sessionID.String()).
			Msg("failed to load session details for archive audit")
	}

	if h.snapshotStore != nil {
		if err := storage.CleanupSessionSnapshot(r.Context(), h.snapshotStore, h.runStore, orgID, sessionID, snapshotKey); err != nil {
			zerolog.Ctx(r.Context()).Warn().Err(err).
				Str("session_id", sessionID.String()).
				Msg("failed to clean up snapshot on session archive")
		}
	}

	sessionIDStr := sessionID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionSessionArchived, models.AuditResourceSession, &sessionIDStr, &sessionID, nil, auditDetails)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *SessionHandler) enqueueSlackArchiveReactionIfLinked(ctx context.Context, orgID, sessionID uuid.UUID) {
	if h.slackSessionLinks == nil || h.jobStore == nil {
		return
	}
	if _, err := h.slackSessionLinks.GetBySession(ctx, orgID, sessionID); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			zerolog.Ctx(ctx).Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to load Slack session link for archive reaction")
		}
		return
	}
	payload := models.SlackAddSessionReactionJobPayload{
		OrgID:        orgID.String(),
		SessionID:    sessionID.String(),
		ReactionName: models.SlackReactionSessionArchived,
	}
	dedupeKey := "slack_reaction:" + sessionID.String() + ":" + models.SlackReactionSessionArchived
	if _, err := h.jobStore.Enqueue(ctx, orgID, "default", "slack_add_session_reaction", payload, 3, &dedupeKey); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to enqueue Slack archive reaction")
	}
}

// UnarchiveSession removes the archived flag from a session.
func (h *SessionHandler) UnarchiveSession(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	var auditActorID *uuid.UUID
	if user := middleware.UserFromContext(r.Context()); user != nil {
		auditActorID = &user.ID
	}
	var auditDetails json.RawMessage
	var auditLoadErr error
	if h.audit != nil {
		session, err := h.runStore.GetByID(r.Context(), orgID, sessionID)
		if err != nil {
			auditLoadErr = err
		} else {
			auditDetails = sessionArchiveAuditDetails(h.logger, &session, models.AuditActionSessionUnarchived, auditActorID)
		}
	}

	if err := h.runStore.Unarchive(r.Context(), orgID, sessionID); err != nil {
		if errors.Is(err, db.ErrSessionNotArchived) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found or not archived")
		} else {
			zerolog.Ctx(r.Context()).Error().
				Err(err).
				Str("session_id", sessionID.String()).
				Str("org_id", orgID.String()).
				Msg("failed to unarchive session")
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to unarchive session", err)
		}
		return
	}
	if auditLoadErr != nil {
		zerolog.Ctx(r.Context()).Warn().
			Err(auditLoadErr).
			Str("session_id", sessionID.String()).
			Msg("failed to load session details for unarchive audit")
	}

	sessionIDStr := sessionID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionSessionUnarchived, models.AuditResourceSession, &sessionIDStr, &sessionID, nil, auditDetails)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// isValidGitRef checks whether s is a plausible git branch/ref name. It
// delegates to the shared gitref validator so the API, worker, and slackbot
// paths all enforce identical rules (see internal/gitref).
func isValidGitRef(s string) bool {
	return gitref.IsValidRef(s)
}
