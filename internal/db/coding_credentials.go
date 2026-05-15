// Package db — coding_credentials store.
//
// CodingCredentialStore is the single source of truth for coding-agent
// credentials at every scope. See docs/design/future/65-unified-coding-credentials.md.
//
// Anchors:
//   - One row per credential. user_id IS NULL means org-scoped.
//   - Every mutation takes Scope; the store re-asserts in transaction that
//     the loaded row's (org_id, user_id) matches Scope and returns
//     ErrCodingCredentialNotFound on mismatch.
//   - ListResolvable is the entire fallback algorithm: personal stack first
//     (when UserID != nil), then org stack, ordered by priority within each.
package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/assembledhq/143/internal/crypto"
	"github.com/assembledhq/143/internal/models"
)

const codingCredentialsColumns = "id, org_id, user_id, provider, label, config, priority, status, created_by, last_verified_at, rate_limited_until, rate_limited_observed_at, rate_limit_message, created_at, updated_at" // #nosec G101 -- SQL column list

// ErrCodingCredentialNotFound is returned by every Get/mutation method when
// either no row matches or the row matches but lives in a different scope.
// We deliberately conflate the two cases so id enumeration cannot distinguish
// "exists in another user's stack" from "does not exist".
var ErrCodingCredentialNotFound = errors.New("coding credential not found")

// ErrAllCredentialsShed is returned by PickRunnable/PickRunnableMulti when
// the resolver found candidates but every one of them is currently in the
// in-process shed cache (rate-limited or auth-rejected within the health
// TTL). Distinct from ErrCodingCredentialNotFound — callers that want to
// distinguish "user has no creds" (config error: prompt to add one) from
// "all creds are temporarily down" (transient: surface and let the caller
// decide whether to retry) can errors.Is on this sentinel.
var ErrAllCredentialsShed = errors.New("all eligible coding credentials are currently shed")

// ErrCodingCredentialLabelTaken is returned when a row already exists at
// (org_id, user_id, provider, label) and is not eligible to be overwritten
// (i.e. it is active or invalid). The embedded ExistingStatus tells the
// caller which.
type ErrCodingCredentialLabelTaken struct {
	Label          string
	ExistingStatus string
}

func (e *ErrCodingCredentialLabelTaken) Error() string {
	switch e.ExistingStatus {
	case models.CodingCredentialStatusActive:
		return fmt.Sprintf("a credential with label %q is already connected", e.Label)
	case models.CodingCredentialStatusInvalid:
		return fmt.Sprintf("a credential with label %q is invalid — disconnect it first", e.Label)
	case "":
		// Lookup-after-conflict failed (transaction snapshot, race, etc.) so
		// we don't know the existing row's status. Surface a friendly message
		// rather than the raw "status \"\"" debug string.
		return fmt.Sprintf("a credential with label %q already exists", e.Label)
	default:
		return fmt.Sprintf("a credential with label %q already exists (status %q)", e.Label, e.ExistingStatus)
	}
}

// CreateOpts groups the optional knobs on a Create call. Most callers only set
// CreatedBy; reorderHook is exposed for tests that want to inspect the
// just-allocated priority.
type CreateOpts struct {
	CreatedBy *uuid.UUID
	Status    string // defaults to "active"
}

// CodingCredentialStore is the unified credential store.
type CodingCredentialStore struct {
	db     DBTX
	crypto *crypto.Service // nil = dev mode

	// resolverCache memoizes ListResolvable for 30s. Hot path on every session
	// start; an org with a stable stack and 1k sessions/min produces 1k
	// identical reads otherwise.
	resolverCache *resolverCache

	// health caches short-TTL "do not pick" markers per credential id. Written
	// when an upstream call returns 429 or auth-rejected; consulted by
	// PickRunnable to skip a credential without a DB write.
	health *healthCache

	// rng is injectable for deterministic tests of the same-priority
	// distribution path.
	rng   *rand.Rand
	rngMu sync.Mutex
	clock func() time.Time

	// mirrorLogf surfaces structural drift detected during a mirror write
	// (e.g. a legacy anthropic row with both APIKey and Subscription set).
	// Production wires the application logger via SetMirrorLogger; nil is
	// treated as a silent no-op for tests.
	mirrorLogf func(format string, args ...any)

	// mirrorDriftTotal counts every detected legacy-row drift case (e.g.
	// dual-set Anthropic APIKey+Subscription rows). mirrorFailureTotal counts
	// every mirror-write or cascade error returned to the legacy store.
	// Both are observable via MirrorDriftCount / MirrorFailureCount so the
	// telemetry pipeline can alert on persistent dual-write inconsistency.
	// Persistent non-zero values mean the unified table is drifting from the
	// legacy stores; the cleanup PR retires the mirror, so this signal only
	// matters during the rollout window.
	mirrorDriftTotal              atomic.Uint64
	mirrorFailureTotal            atomic.Uint64
	mirrorNaturalKeyFallbackTotal atomic.Uint64
}

// SetMirrorLogger installs the structured-log hook used when the mirror
// detects drift in a legacy row. Production wires the application logger;
// tests usually leave it nil.
//
// lint:allow-no-orgid reason="process-wide logger configuration; not tenant data"
func (s *CodingCredentialStore) SetMirrorLogger(logf func(format string, args ...any)) {
	s.mirrorLogf = logf
}

func (s *CodingCredentialStore) mirrorWarn(format string, args ...any) {
	if s == nil || s.mirrorLogf == nil {
		return
	}
	s.mirrorLogf(format, args...)
}

// recordMirrorDrift increments the drift counter for an observed structural
// inconsistency (e.g. a legacy Anthropic row with both APIKey and Subscription
// set). Distinct from recordMirrorFailure: drift means a legacy row is
// malformed but the mirror succeeded; failure means the mirror itself errored.
func (s *CodingCredentialStore) recordMirrorDrift() {
	if s == nil {
		return
	}
	s.mirrorDriftTotal.Add(1)
}

// recordMirrorFailure increments the failure counter for an unsuccessful
// mirror write (DB error, cascade error, encryption failure).
func (s *CodingCredentialStore) recordMirrorFailure() {
	if s == nil {
		return
	}
	s.mirrorFailureTotal.Add(1)
}

// MirrorDriftCount returns the running total of detected drift events. A
// non-zero value during the dual-write window indicates legacy data that
// would not round-trip cleanly into the unified schema; alert on a
// non-trivial baseline rather than the first hit, since dual-set legacy rows
// can pre-date validation.
//
// lint:allow-no-orgid reason="process-wide observability counter; not tenant data"
func (s *CodingCredentialStore) MirrorDriftCount() uint64 {
	if s == nil {
		return 0
	}
	return s.mirrorDriftTotal.Load()
}

// MirrorFailureCount returns the running total of mirror-write failures. A
// sustained non-zero rate means the unified table is drifting from the
// legacy stores; investigate before letting the cleanup PR retire the mirror.
//
// lint:allow-no-orgid reason="process-wide observability counter; not tenant data"
func (s *CodingCredentialStore) MirrorFailureCount() uint64 {
	if s == nil {
		return 0
	}
	return s.mirrorFailureTotal.Load()
}

// recordMirrorNaturalKeyFallback increments when upsertMirroredRow's
// insert-by-id collides with the (org_id, user_id, provider, label) unique
// index and falls back to updating the existing natural-key row. The fallback
// leaves legacy id and unified id divergent for that pair, so we want to
// confirm the path is unused before the cleanup PR deletes it.
func (s *CodingCredentialStore) recordMirrorNaturalKeyFallback() {
	if s == nil {
		return
	}
	s.mirrorNaturalKeyFallbackTotal.Add(1)
}

// MirrorNaturalKeyFallbackCount returns how many times the mirror has had to
// reconcile a row by natural key instead of by id. Expected to stay at 0 in
// production; a non-zero value means an out-of-band writer (e.g. the SQL
// data-copy migration) landed at the same (scope, provider, label) before the
// mirror caught up. Read this before retiring the fallback in the cleanup PR.
//
// lint:allow-no-orgid reason="process-wide observability counter; not tenant data"
func (s *CodingCredentialStore) MirrorNaturalKeyFallbackCount() uint64 {
	if s == nil {
		return 0
	}
	return s.mirrorNaturalKeyFallbackTotal.Load()
}

// NewCodingCredentialStore constructs a store with default cache TTLs.
//
// Cache TTLs:
//   - resolverCache (30s): caches the per-(scope, provider) candidate list so a
//     burst of agent picks doesn't hammer the DB. Cap is short because new
//     credentials and CRUD edits should be visible quickly.
//   - health (75s): caches "do not pick" markers from upstream rate-limit /
//     auth-rejected signals. Sized to outlast Anthropic's typical 60s rate-
//     limit recovery window plus a small buffer; shorter values caused the
//     same shed credential to be re-picked into the same upstream limit
//     before it had cleared.
//
// The 75s > 30s skew is intentional. A user who manually fixes a shed
// credential (e.g. rotates a key) will still wait out the remaining health-
// cache TTL before that credential is picked again — a worst-case ~45s after
// the resolver cache turns over. We accept that latency to keep the shed
// signal effective; the alternative (aligning at 30s) would let a freshly
// rate-limited credential be re-picked the moment the resolver cache flips,
// nullifying the shed.
func NewCodingCredentialStore(dbtx DBTX, cryptoSvc *crypto.Service) *CodingCredentialStore {
	return &CodingCredentialStore{
		db:            dbtx,
		crypto:        cryptoSvc,
		resolverCache: newResolverCache(30 * time.Second),
		health:        newHealthCache(75 * time.Second),
		rng:           rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0)), // #nosec G404,G115 -- non-security load distribution jitter; not used for secrets or authorization
		clock:         time.Now,
	}
}

// SetRNG overrides the internal RNG for deterministic tests of PickRunnable.
//
// lint:allow-no-orgid reason="process-wide RNG configuration; not tenant data"
func (s *CodingCredentialStore) SetRNG(r *rand.Rand) {
	s.rngMu.Lock()
	defer s.rngMu.Unlock()
	s.rng = r
}

// SetClock overrides the internal clock for cache-expiry tests.
//
// lint:allow-no-orgid reason="process-wide clock injection; not tenant data"
func (s *CodingCredentialStore) SetClock(now func() time.Time) {
	s.clock = now
	s.resolverCache.clock = now
	s.health.clock = now
}

// MarkRateLimited records a short-TTL "do not pick" marker for a credential.
// Called by the agent runtime via AgentEnv.ShedRateLimited when a finished
// run reports a 429-class signal in result.Error. The id is already org-scoped
// because it can only be obtained through a prior scoped pick; the in-process
// health cache keys by id alone.
//
// lint:allow-no-orgid reason="id was obtained from a scoped Pick; in-process cache keys by id only"
func (s *CodingCredentialStore) MarkRateLimited(id uuid.UUID) {
	s.health.shed(id)
}

// MarkRateLimitedForScope records a durable temporary rate-limit marker and
// also sheds the credential in memory so this worker skips it immediately.
func (s *CodingCredentialStore) MarkRateLimitedForScope(ctx context.Context, scope models.Scope, id uuid.UUID, limit models.CodingCredentialRateLimit) error {
	s.health.shed(id)
	until := limit.Until
	if until.IsZero() {
		until = s.clock().Add(s.health.ttl)
	}
	var message *string
	if limit.Message != "" {
		message = &limit.Message
	}
	var provider models.ProviderName
	if err := s.withScopedRowTx(ctx, scope, id, func(tx pgx.Tx, rowProvider models.ProviderName) error {
		provider = rowProvider
		args := pgx.NamedArgs{
			"id":          id,
			"org_id":      scope.OrgID,
			"until":       until,
			"message":     message,
			"observed_at": s.clock(),
		}
		var query string
		if scope.IsPersonal() {
			args["user_id"] = *scope.UserID
			query = `UPDATE coding_credentials
				 SET rate_limited_until = @until,
				     rate_limited_observed_at = @observed_at,
				     rate_limit_message = @message,
				     updated_at = now()
				 WHERE id = @id AND org_id = @org_id AND user_id = @user_id`
		} else {
			query = `UPDATE coding_credentials
				 SET rate_limited_until = @until,
				     rate_limited_observed_at = @observed_at,
				     rate_limit_message = @message,
				     updated_at = now()
				 WHERE id = @id AND org_id = @org_id AND user_id IS NULL`
		}
		tag, execErr := tx.Exec(ctx, query, args)
		if execErr != nil {
			return fmt.Errorf("mark credential rate limited: %w", execErr)
		}
		if tag.RowsAffected() == 0 {
			return ErrCodingCredentialNotFound
		}
		return nil
	}); err != nil {
		return err
	}
	s.invalidate(scope, provider)
	return nil
}

// MarkAuthRejected records a "do not pick" marker following an auth failure.
// Called by the agent runtime via AgentEnv.ShedAuthRejected when a finished
// run reports a 401-class signal that the token-expired retry could not
// recover from. The OAuth services independently flip the credential's
// persisted status to "invalid"; the in-memory marker prevents repeat picks
// before that write propagates through the resolver cache.
//
// lint:allow-no-orgid reason="id was obtained from a scoped Pick; in-process cache keys by id only"
func (s *CodingCredentialStore) MarkAuthRejected(id uuid.UUID) {
	s.health.shed(id)
}

// MarkAuthRejectedForScope marks a credential invalid after a hard upstream
// auth rejection and also sheds it in memory for this worker immediately.
func (s *CodingCredentialStore) MarkAuthRejectedForScope(ctx context.Context, scope models.Scope, id uuid.UUID) error {
	s.health.shed(id)
	return s.UpdateStatus(ctx, scope, id, models.CodingCredentialStatusInvalid)
}

// ----- Lookup -----

// Get returns a single credential by id, scoped to (org_id, user_id) so that
// id enumeration cannot reach into another user's or another org's stack.
func (s *CodingCredentialStore) Get(ctx context.Context, scope models.Scope, id uuid.UUID) (*models.DecryptedCodingCredential, error) {
	row, err := s.fetchRowByID(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	return s.decryptRow(*row)
}

// GetByProviderAndLabel returns the (provider, label) credential within scope.
func (s *CodingCredentialStore) GetByProviderAndLabel(ctx context.Context, scope models.Scope, provider models.ProviderName, label string) (*models.DecryptedCodingCredential, error) {
	args := pgx.NamedArgs{
		"org_id":   scope.OrgID,
		"provider": string(provider),
		"label":    label,
	}
	var query string
	if scope.IsPersonal() {
		args["user_id"] = *scope.UserID
		query = `SELECT ` + codingCredentialsColumns + `
			FROM coding_credentials
			WHERE org_id = @org_id AND user_id = @user_id AND provider = @provider AND label = @label`
	} else {
		query = `SELECT ` + codingCredentialsColumns + `
			FROM coding_credentials
			WHERE org_id = @org_id AND user_id IS NULL AND provider = @provider AND label = @label`
	}
	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query credential by provider+label: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.CodingCredential])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCodingCredentialNotFound
		}
		return nil, fmt.Errorf("get credential by provider+label: %w", err)
	}
	return s.decryptRow(row)
}

// ListByScope returns every active+disabled+pending row in the given scope.
// Used by both settings pages.
func (s *CodingCredentialStore) ListByScope(ctx context.Context, scope models.Scope) ([]models.DecryptedCodingCredential, error) {
	args := pgx.NamedArgs{"org_id": scope.OrgID}
	var query string
	if scope.IsPersonal() {
		args["user_id"] = *scope.UserID
		query = `SELECT ` + codingCredentialsColumns + `
			FROM coding_credentials
			WHERE org_id = @org_id AND user_id = @user_id AND status != 'disabled'
			ORDER BY priority, created_at`
	} else {
		query = `SELECT ` + codingCredentialsColumns + `
			FROM coding_credentials
			WHERE org_id = @org_id AND user_id IS NULL AND status != 'disabled'
			ORDER BY priority, created_at`
	}
	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query credentials by scope: %w", err)
	}
	dbRows, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[models.CodingCredential])
	if err != nil {
		return nil, fmt.Errorf("collect credentials by scope: %w", err)
	}
	return s.decryptRows(dbRows)
}

// ListByProvider lists every active+pending row within scope for one provider.
func (s *CodingCredentialStore) ListByProvider(ctx context.Context, scope models.Scope, provider models.ProviderName) ([]models.DecryptedCodingCredential, error) {
	args := pgx.NamedArgs{"org_id": scope.OrgID, "provider": string(provider)}
	var query string
	if scope.IsPersonal() {
		args["user_id"] = *scope.UserID
		query = `SELECT ` + codingCredentialsColumns + `
			FROM coding_credentials
			WHERE org_id = @org_id AND user_id = @user_id AND provider = @provider AND status != 'disabled'
			ORDER BY priority, created_at`
	} else {
		query = `SELECT ` + codingCredentialsColumns + `
			FROM coding_credentials
			WHERE org_id = @org_id AND user_id IS NULL AND provider = @provider AND status != 'disabled'
			ORDER BY priority, created_at`
	}
	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query credentials by provider: %w", err)
	}
	dbRows, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[models.CodingCredential])
	if err != nil {
		return nil, fmt.Errorf("collect credentials by provider: %w", err)
	}
	return s.decryptRows(dbRows)
}

// ListResolvable is the resolver hot path. Returns the ordered list a
// runtime caller would walk: personal rows for the user (when userID != nil)
// then org rows, each tier ordered by priority then created_at. Filters to
// status='active' rows — disabled/invalid/pending_auth do not enter the
// runnable stack.
//
// Implementation issues two narrow lookups against the partial index. Both
// halves return rows in their final concatenated order, so app-side
// concatenation costs no sort step.
func (s *CodingCredentialStore) ListResolvable(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) ([]models.DecryptedCodingCredential, error) {
	if cached, ok := s.resolverCache.get(orgID, userID, provider); ok {
		return cached, nil
	}

	resolved := make([]models.DecryptedCodingCredential, 0, 4)

	if userID != nil {
		personal, err := s.queryResolverHalf(ctx, orgID, userID, provider)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, personal...)
	}

	org, err := s.queryResolverHalf(ctx, orgID, nil, provider)
	if err != nil {
		return nil, err
	}
	resolved = append(resolved, org...)

	s.resolverCache.put(orgID, userID, provider, resolved)
	return resolved, nil
}

// ListResolvableMulti returns the resolver list for several providers in a
// single round trip. Equivalent to calling ListResolvable per provider but
// folds the per-provider partial-index seeks into one query each for the
// personal and org halves, which matters on cold caches (e.g. the account
// settings page renders the effective-resolution block across every coding
// provider on first load).
//
// The returned map always contains an entry for every requested provider,
// possibly with a nil slice. Cached entries are served from the resolver
// cache without contributing to the round trip; uncached entries are queried
// in bulk and cached on the way out.
func (s *CodingCredentialStore) ListResolvableMulti(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, providers []models.ProviderName) (map[models.ProviderName][]models.DecryptedCodingCredential, error) {
	out := make(map[models.ProviderName][]models.DecryptedCodingCredential, len(providers))
	uncached := make([]models.ProviderName, 0, len(providers))
	for _, p := range providers {
		if cached, ok := s.resolverCache.get(orgID, userID, p); ok {
			out[p] = cached
			continue
		}
		out[p] = nil
		uncached = append(uncached, p)
	}
	if len(uncached) == 0 {
		return out, nil
	}

	// Issue one query per scope half for all uncached providers. Postgres
	// can satisfy these from the same partial-resolver indexes used by the
	// per-provider seek; the savings come from amortising round-trip and
	// pgx allocation overhead across providers.
	if userID != nil {
		personal, err := s.queryResolverHalfMulti(ctx, orgID, userID, uncached)
		if err != nil {
			return nil, err
		}
		for p, rows := range personal {
			out[p] = append(out[p], rows...)
		}
	}
	org, err := s.queryResolverHalfMulti(ctx, orgID, nil, uncached)
	if err != nil {
		return nil, err
	}
	for p, rows := range org {
		out[p] = append(out[p], rows...)
	}

	for _, p := range uncached {
		s.resolverCache.put(orgID, userID, p, out[p])
	}
	return out, nil
}

// queryResolverHalfMulti is queryResolverHalf for a slice of providers, using
// `provider = ANY(@providers)` in one statement. Returns a map keyed by
// provider name with the rows pre-bucketed and sorted within each bucket by
// (priority, created_at) — matching the contract of ListResolvable for a
// single provider.
func (s *CodingCredentialStore) queryResolverHalfMulti(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, providers []models.ProviderName) (map[models.ProviderName][]models.DecryptedCodingCredential, error) {
	if len(providers) == 0 {
		return nil, nil
	}
	providerStrs := make([]string, len(providers))
	for i, p := range providers {
		providerStrs[i] = string(p)
	}
	args := pgx.NamedArgs{"org_id": orgID, "providers": providerStrs}
	var query string
	if userID != nil {
		args["user_id"] = *userID
		query = `SELECT ` + codingCredentialsColumns + `
			FROM coding_credentials
			WHERE org_id = @org_id AND provider = ANY(@providers) AND user_id = @user_id AND status = 'active'
			ORDER BY provider, priority, created_at`
	} else {
		query = `SELECT ` + codingCredentialsColumns + `
			FROM coding_credentials
			WHERE org_id = @org_id AND provider = ANY(@providers) AND user_id IS NULL AND status = 'active'
			ORDER BY provider, priority, created_at`
	}
	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query resolver half multi: %w", err)
	}
	dbRows, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[models.CodingCredential])
	if err != nil {
		return nil, fmt.Errorf("collect resolver half multi: %w", err)
	}
	decrypted, err := s.decryptRows(dbRows)
	if err != nil {
		return nil, err
	}
	bucketed := make(map[models.ProviderName][]models.DecryptedCodingCredential, len(providers))
	for _, p := range providers {
		bucketed[p] = nil
	}
	for _, row := range decrypted {
		bucketed[row.Provider] = append(bucketed[row.Provider], row)
	}
	return bucketed, nil
}

// queryResolverHalf hits the partial resolver index for one (scope, provider)
// half. userID nil → org rows; userID != nil → that user's personal rows.
func (s *CodingCredentialStore) queryResolverHalf(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) ([]models.DecryptedCodingCredential, error) {
	args := pgx.NamedArgs{"org_id": orgID, "provider": string(provider)}
	var query string
	if userID != nil {
		args["user_id"] = *userID
		query = `SELECT ` + codingCredentialsColumns + `
			FROM coding_credentials
			WHERE org_id = @org_id AND provider = @provider AND user_id = @user_id AND status = 'active'
			ORDER BY priority, created_at`
	} else {
		query = `SELECT ` + codingCredentialsColumns + `
			FROM coding_credentials
			WHERE org_id = @org_id AND provider = @provider AND user_id IS NULL AND status = 'active'
			ORDER BY priority, created_at`
	}
	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query resolver half: %w", err)
	}
	dbRows, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[models.CodingCredential])
	if err != nil {
		return nil, fmt.Errorf("collect resolver half: %w", err)
	}
	return s.decryptRows(dbRows)
}

// PickRunnable is the runtime selection path. ListResolvable + tier-walking
// + random-with-shedding within each tier (priority group). Returns
// ErrCodingCredentialNotFound when the resolver returned zero rows;
// ErrAllCredentialsShed when rows existed but every tier was filtered out by
// the in-process shed cache.
//
// Random selection avoids the per-(scope, provider, priority) hotspot that
// strict round-robin would introduce — see design doc § "Same-priority
// distribution".
func (s *CodingCredentialStore) PickRunnable(ctx context.Context, scope models.Scope, provider models.ProviderName) (*models.DecryptedCodingCredential, error) {
	creds, err := s.ListResolvable(ctx, scope.OrgID, scope.UserID, provider)
	if err != nil {
		return nil, err
	}
	if len(creds) == 0 {
		return nil, ErrCodingCredentialNotFound
	}

	for _, tier := range groupByPriorityAndScope(creds) {
		eligible := s.filterAvailable(tier)
		if len(eligible) == 0 {
			continue
		}
		s.rngMu.Lock()
		idx := s.rng.IntN(len(eligible))
		s.rngMu.Unlock()
		picked := eligible[idx]
		return &picked, nil
	}
	return nil, ErrAllCredentialsShed
}

// PickRunnableMulti is PickRunnable across several provider names that all
// satisfy the same agent request (for example Anthropic API-key rows plus
// Anthropic subscription rows). It preserves the resolver invariant that every
// personal candidate is tried before any org fallback, then orders by the
// shared stack priority inside each scope.
func (s *CodingCredentialStore) PickRunnableMulti(ctx context.Context, scope models.Scope, providers []models.ProviderName) (*models.DecryptedCodingCredential, error) {
	if len(providers) == 0 {
		return nil, ErrCodingCredentialNotFound
	}
	uniqueProviders := make([]models.ProviderName, 0, len(providers))
	seenProviders := make(map[models.ProviderName]struct{}, len(providers))
	for _, provider := range providers {
		if provider == "" {
			continue
		}
		if _, ok := seenProviders[provider]; ok {
			continue
		}
		seenProviders[provider] = struct{}{}
		uniqueProviders = append(uniqueProviders, provider)
	}
	if len(uniqueProviders) == 0 {
		return nil, ErrCodingCredentialNotFound
	}

	resolvedByProvider, err := s.ListResolvableMulti(ctx, scope.OrgID, scope.UserID, uniqueProviders)
	if err != nil {
		return nil, err
	}
	creds := make([]models.DecryptedCodingCredential, 0)
	for _, provider := range uniqueProviders {
		creds = append(creds, resolvedByProvider[provider]...)
	}
	if len(creds) == 0 {
		return nil, ErrCodingCredentialNotFound
	}
	sortResolvedCredentialRows(creds)

	for _, tier := range groupByPriorityAndScope(creds) {
		eligible := s.filterAvailable(tier)
		if len(eligible) == 0 {
			continue
		}
		s.rngMu.Lock()
		idx := s.rng.IntN(len(eligible))
		s.rngMu.Unlock()
		picked := eligible[idx]
		return &picked, nil
	}
	return nil, ErrAllCredentialsShed
}

func sortResolvedCredentialRows(creds []models.DecryptedCodingCredential) {
	sort.SliceStable(creds, func(i, j int) bool {
		leftPersonal := creds[i].UserID != nil
		rightPersonal := creds[j].UserID != nil
		if leftPersonal != rightPersonal {
			return leftPersonal
		}
		if creds[i].Priority != creds[j].Priority {
			return creds[i].Priority < creds[j].Priority
		}
		if !creds[i].CreatedAt.Equal(creds[j].CreatedAt) {
			return creds[i].CreatedAt.Before(creds[j].CreatedAt)
		}
		return false
	})
}

// groupByPriorityAndScope walks an already-sorted ListResolvable result and
// emits contiguous tiers. Two rows belong to the same tier iff they have the
// same scope (both personal-for-this-user or both org) AND the same priority.
// Crossing scope is always a new tier so personal #N never blurs into org #N.
func groupByPriorityAndScope(creds []models.DecryptedCodingCredential) [][]models.DecryptedCodingCredential {
	if len(creds) == 0 {
		return nil
	}
	out := make([][]models.DecryptedCodingCredential, 0, 2)
	cur := []models.DecryptedCodingCredential{creds[0]}
	for i := 1; i < len(creds); i++ {
		prev := cur[len(cur)-1]
		next := creds[i]
		samePriority := prev.Priority == next.Priority
		sameScope := (prev.UserID == nil) == (next.UserID == nil)
		if samePriority && sameScope {
			cur = append(cur, next)
			continue
		}
		out = append(out, cur)
		cur = []models.DecryptedCodingCredential{next}
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func (s *CodingCredentialStore) filterAvailable(creds []models.DecryptedCodingCredential) []models.DecryptedCodingCredential {
	eligible := s.health.filter(creds)
	now := s.clock()
	out := make([]models.DecryptedCodingCredential, 0, len(eligible))
	for _, cred := range eligible {
		if credentialRateLimitedAt(cred, now) {
			continue
		}
		out = append(out, cred)
	}
	return out
}

func credentialRateLimitedAt(cred models.DecryptedCodingCredential, now time.Time) bool {
	return cred.Status == models.CodingCredentialStatusActive &&
		cred.RateLimitedUntil != nil &&
		cred.RateLimitedUntil.After(now)
}

// ----- Mutation -----

// Create inserts a new credential at the bottom of the scope's stack and
// returns the new id.
func (s *CodingCredentialStore) Create(ctx context.Context, scope models.Scope, label string, cfg models.ProviderConfig, opts CreateOpts) (*uuid.UUID, error) {
	encrypted, err := s.marshalAndEncrypt(cfg)
	if err != nil {
		return nil, err
	}
	status := opts.Status
	if status == "" {
		status = models.CodingCredentialStatusActive
	}

	var id uuid.UUID
	err = s.withScopeLock(ctx, scope, func(tx pgx.Tx, nextPriority int) error {
		args := pgx.NamedArgs{
			"org_id":     scope.OrgID,
			"user_id":    scope.UserID,
			"provider":   string(cfg.Provider()),
			"label":      label,
			"config":     encrypted,
			"status":     status,
			"created_by": opts.CreatedBy,
			"priority":   nextPriority,
		}
		// On conflict (label collision) we surface a typed error so the API
		// layer can render a meaningful message. Disabled and pending_auth
		// rows are eligible for resurrection — disabled rows take the new
		// tail-slot priority (their old slot was relinquished when they were
		// soft-deleted), while pending_auth rows keep their existing priority
		// so a pending OAuth flow that completes does not jump the stack
		// behind unrelated rows added in the meantime. Active and invalid
		// rows are not eligible: the WHERE clause makes RETURNING return no
		// rows, and the lookup-after-conflict below surfaces
		// ErrCodingCredentialLabelTaken with the existing status.
		query := `
			INSERT INTO coding_credentials
				(org_id, user_id, provider, label, config, status, created_by, priority)
			VALUES (@org_id, @user_id, @provider, @label, @config, @status, @created_by, @priority)
			ON CONFLICT (org_id, user_id, provider, label) DO UPDATE
			SET config = EXCLUDED.config,
			    status = EXCLUDED.status,
			    last_verified_at = NULL,
			    updated_at = now(),
			    priority = CASE WHEN coding_credentials.status = 'disabled'
			                    THEN EXCLUDED.priority
			                    ELSE coding_credentials.priority END
			WHERE coding_credentials.status IN ('disabled', 'pending_auth')
			RETURNING id`

		scanErr := tx.QueryRow(ctx, query, args).Scan(&id)
		if scanErr == nil {
			return nil
		}
		if !errors.Is(scanErr, pgx.ErrNoRows) {
			return fmt.Errorf("insert credential: %w", scanErr)
		}
		// Active or invalid row blocked the insert; report typed error.
		var existingStatus string
		lookupArgs := pgx.NamedArgs{
			"org_id":   scope.OrgID,
			"user_id":  scope.UserID,
			"provider": string(cfg.Provider()),
			"label":    label,
		}
		var lookupQuery string
		if scope.IsPersonal() {
			lookupQuery = `SELECT status FROM coding_credentials
				WHERE org_id = @org_id AND user_id = @user_id AND provider = @provider AND label = @label`
		} else {
			lookupQuery = `SELECT status FROM coding_credentials
				WHERE org_id = @org_id AND user_id IS NULL AND provider = @provider AND label = @label`
		}
		_ = tx.QueryRow(ctx, lookupQuery, lookupArgs).Scan(&existingStatus)
		return &ErrCodingCredentialLabelTaken{Label: label, ExistingStatus: existingStatus}
	})
	if err != nil {
		return nil, err
	}
	s.invalidate(scope, cfg.Provider())
	return &id, nil
}

// InsertPendingAuth inserts a credential in pending_auth status — used by the
// initiate-OAuth flows. Refuses to overwrite an active or invalid row at the
// same (scope, provider, label) and returns ErrCodingCredentialLabelTaken.
func (s *CodingCredentialStore) InsertPendingAuth(ctx context.Context, scope models.Scope, label string, cfg models.ProviderConfig, createdBy *uuid.UUID) (*uuid.UUID, error) {
	return s.Create(ctx, scope, label, cfg, CreateOpts{
		CreatedBy: createdBy,
		Status:    models.CodingCredentialStatusPendingAuth,
	})
}

// PromotePending exchanges a pending_auth row for an active one with the
// final config (e.g. real OAuth tokens). Scope-checked — calling with a
// different Scope than the row's owner returns ErrCodingCredentialNotFound.
//
// The scope assertion + UPDATE run in one transaction with FOR UPDATE so a
// concurrent re-parent or delete cannot race the write.
func (s *CodingCredentialStore) PromotePending(ctx context.Context, scope models.Scope, id uuid.UUID, cfg models.ProviderConfig) error {
	encrypted, err := s.marshalAndEncrypt(cfg)
	if err != nil {
		return err
	}
	var provider models.ProviderName
	if err := s.withScopedRowTx(ctx, scope, id, func(tx pgx.Tx, rowProvider models.ProviderName) error {
		if rowProvider != cfg.Provider() {
			return fmt.Errorf("provider mismatch: row is %q, config is %q", rowProvider, cfg.Provider())
		}
		provider = rowProvider
		args := pgx.NamedArgs{"id": id, "config": encrypted, "org_id": scope.OrgID}
		var query string
		if scope.IsPersonal() {
			args["user_id"] = *scope.UserID
			query = `UPDATE coding_credentials
			 SET config = @config, status = 'active', last_verified_at = now(), updated_at = now()
			 WHERE id = @id AND org_id = @org_id AND user_id = @user_id`
		} else {
			query = `UPDATE coding_credentials
			 SET config = @config, status = 'active', last_verified_at = now(), updated_at = now()
			 WHERE id = @id AND org_id = @org_id AND user_id IS NULL`
		}
		tag, execErr := tx.Exec(ctx, query, args)
		if execErr != nil {
			return fmt.Errorf("promote pending: %w", execErr)
		}
		if tag.RowsAffected() == 0 {
			return ErrCodingCredentialNotFound
		}
		return nil
	}); err != nil {
		return err
	}
	s.invalidate(scope, provider)
	return nil
}

// UpdateConfig overwrites the encrypted config for an active credential.
func (s *CodingCredentialStore) UpdateConfig(ctx context.Context, scope models.Scope, id uuid.UUID, cfg models.ProviderConfig) error {
	encrypted, err := s.marshalAndEncrypt(cfg)
	if err != nil {
		return err
	}
	var provider models.ProviderName
	if err := s.withScopedRowTx(ctx, scope, id, func(tx pgx.Tx, rowProvider models.ProviderName) error {
		if rowProvider != cfg.Provider() {
			return fmt.Errorf("provider mismatch: row is %q, config is %q", rowProvider, cfg.Provider())
		}
		provider = rowProvider
		args := pgx.NamedArgs{"id": id, "config": encrypted, "org_id": scope.OrgID}
		var query string
		if scope.IsPersonal() {
			args["user_id"] = *scope.UserID
			query = `UPDATE coding_credentials
			 SET config = @config, status = 'active', updated_at = now()
			 WHERE id = @id AND org_id = @org_id AND user_id = @user_id AND status != 'disabled'`
		} else {
			query = `UPDATE coding_credentials
			 SET config = @config, status = 'active', updated_at = now()
			 WHERE id = @id AND org_id = @org_id AND user_id IS NULL AND status != 'disabled'`
		}
		tag, execErr := tx.Exec(ctx, query, args)
		if execErr != nil {
			return fmt.Errorf("update config: %w", execErr)
		}
		if tag.RowsAffected() == 0 {
			return ErrCodingCredentialNotFound
		}
		return nil
	}); err != nil {
		return err
	}
	s.invalidate(scope, provider)
	return nil
}

// Rename updates the label.
func (s *CodingCredentialStore) Rename(ctx context.Context, scope models.Scope, id uuid.UUID, label string) error {
	var provider models.ProviderName
	if err := s.withScopedRowTx(ctx, scope, id, func(tx pgx.Tx, rowProvider models.ProviderName) error {
		provider = rowProvider
		args := pgx.NamedArgs{"id": id, "label": label, "org_id": scope.OrgID}
		var query string
		if scope.IsPersonal() {
			args["user_id"] = *scope.UserID
			query = `UPDATE coding_credentials
			 SET label = @label, updated_at = now()
			 WHERE id = @id AND org_id = @org_id AND user_id = @user_id`
		} else {
			query = `UPDATE coding_credentials
			 SET label = @label, updated_at = now()
			 WHERE id = @id AND org_id = @org_id AND user_id IS NULL`
		}
		tag, execErr := tx.Exec(ctx, query, args)
		if execErr != nil {
			if isUniqueViolation(execErr) {
				return &ErrCodingCredentialLabelTaken{Label: label}
			}
			return fmt.Errorf("rename credential: %w", execErr)
		}
		if tag.RowsAffected() == 0 {
			return ErrCodingCredentialNotFound
		}
		return nil
	}); err != nil {
		return err
	}
	s.invalidate(scope, provider)
	return nil
}

// UpdateStatus moves a credential between active / disabled / pending_auth /
// invalid. Disable() is a thin wrapper that calls UpdateStatus(disabled).
func (s *CodingCredentialStore) UpdateStatus(ctx context.Context, scope models.Scope, id uuid.UUID, status string) error {
	switch status {
	case models.CodingCredentialStatusActive,
		models.CodingCredentialStatusDisabled,
		models.CodingCredentialStatusPendingAuth,
		models.CodingCredentialStatusInvalid:
	default:
		return fmt.Errorf("invalid status: %q", status)
	}
	var provider models.ProviderName
	if err := s.withScopedRowTx(ctx, scope, id, func(tx pgx.Tx, rowProvider models.ProviderName) error {
		provider = rowProvider
		args := pgx.NamedArgs{"id": id, "status": status, "org_id": scope.OrgID}
		var query string
		if scope.IsPersonal() {
			args["user_id"] = *scope.UserID
			query = `UPDATE coding_credentials
				 SET status = @status, updated_at = now()
				 WHERE id = @id AND org_id = @org_id AND user_id = @user_id`
		} else {
			query = `UPDATE coding_credentials
				 SET status = @status, updated_at = now()
				 WHERE id = @id AND org_id = @org_id AND user_id IS NULL`
		}
		tag, execErr := tx.Exec(ctx, query, args)
		if execErr != nil {
			return fmt.Errorf("update status: %w", execErr)
		}
		if tag.RowsAffected() == 0 {
			return ErrCodingCredentialNotFound
		}
		return nil
	}); err != nil {
		return err
	}
	s.invalidate(scope, provider)
	return nil
}

// Disable soft-deletes a credential by flipping status to "disabled".
func (s *CodingCredentialStore) Disable(ctx context.Context, scope models.Scope, id uuid.UUID) error {
	return s.UpdateStatus(ctx, scope, id, models.CodingCredentialStatusDisabled)
}

// Reorder bulk-rewrites the priority of every row referenced in orderedIDs.
// Used by the rare "reset stack" flow and by tests; the UI drag-drop path is
// Move, which only touches the ids that actually shift.
func (s *CodingCredentialStore) Reorder(ctx context.Context, scope models.Scope, orderedIDs []uuid.UUID) error {
	if len(orderedIDs) == 0 {
		return nil
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := s.acquireScopeLockTx(ctx, tx, scope); err != nil {
		return err
	}

	stack, err := s.fetchStackTx(ctx, tx, scope)
	if err != nil {
		return err
	}
	if !sameUUIDSet(orderedIDs, stack) {
		return fmt.Errorf("ordered_ids must exactly match the active credential stack for the requested scope")
	}

	for idx, id := range orderedIDs {
		if err := s.assertScopeAndProviderTx(ctx, tx, scope, id); err != nil {
			return err
		}
		tag, execErr := s.updatePriorityTx(ctx, tx, scope, id, idx+1)
		if execErr != nil {
			return fmt.Errorf("reorder credential %s: %w", id, execErr)
		}
		if tag.RowsAffected() == 0 {
			return ErrCodingCredentialNotFound
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit reorder: %w", err)
	}
	s.invalidateScope(scope)
	return nil
}

// Move repositions one row within its scope's stack. Exactly one of
// MovePosition's fields must be set. Recomputes contiguous priorities for
// the affected rows in a single transaction.
func (s *CodingCredentialStore) Move(ctx context.Context, scope models.Scope, id uuid.UUID, pos models.MoveCodingCredentialInput) error {
	if err := pos.Validate(); err != nil {
		return err
	}

	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := s.acquireScopeLockTx(ctx, tx, scope); err != nil {
		return err
	}

	if err := s.assertScopeAndProviderTx(ctx, tx, scope, id); err != nil {
		return err
	}

	// Snapshot current ordering for the scope, excluding disabled rows.
	stack, err := s.fetchStackTx(ctx, tx, scope)
	if err != nil {
		return err
	}

	// Drop the moving id from the snapshot, then re-insert at the new index.
	without := make([]uuid.UUID, 0, len(stack))
	var movingFound bool
	for _, sid := range stack {
		if sid == id {
			movingFound = true
			continue
		}
		without = append(without, sid)
	}
	if !movingFound {
		return ErrCodingCredentialNotFound
	}

	insertAt := len(without) // ToBottom by default
	switch {
	case pos.ToTop:
		insertAt = 0
	case pos.ToBottom:
		insertAt = len(without)
	case pos.BeforeID != nil:
		idx := indexOf(without, *pos.BeforeID)
		if idx < 0 {
			return fmt.Errorf("before_id not found in scope")
		}
		insertAt = idx
	case pos.AfterID != nil:
		idx := indexOf(without, *pos.AfterID)
		if idx < 0 {
			return fmt.Errorf("after_id not found in scope")
		}
		insertAt = idx + 1
	}

	newOrder := make([]uuid.UUID, 0, len(stack))
	newOrder = append(newOrder, without[:insertAt]...)
	newOrder = append(newOrder, id)
	newOrder = append(newOrder, without[insertAt:]...)

	// Apply the new priorities. Only rewrite rows whose priority actually
	// changed — keeps writes proportional to the number of rows that moved
	// rather than stack size.
	//
	// The id list comes from fetchStackTx (already scope-bounded one
	// statement earlier) so it is safe by construction. We still re-anchor
	// the read to the same scope as defense-in-depth — if a future refactor
	// ever lets a non-scoped id slip into newOrder, this filter prevents the
	// in-tx fetch from leaking another tenant's priority into the rewrite.
	currentPriority := map[uuid.UUID]int{}
	priorityArgs := pgx.NamedArgs{"ids": newOrder, "org_id": scope.OrgID}
	var priorityQuery string
	if scope.IsPersonal() {
		priorityArgs["user_id"] = *scope.UserID
		priorityQuery = `SELECT id, priority FROM coding_credentials
			WHERE id = ANY(@ids) AND status != 'disabled'
			  AND org_id = @org_id AND user_id = @user_id`
	} else {
		priorityQuery = `SELECT id, priority FROM coding_credentials
			WHERE id = ANY(@ids) AND status != 'disabled'
			  AND org_id = @org_id AND user_id IS NULL`
	}
	rows, err := tx.Query(ctx, priorityQuery, priorityArgs)
	if err != nil {
		return fmt.Errorf("fetch current priorities: %w", err)
	}
	for rows.Next() {
		var rid uuid.UUID
		var p int
		if err := rows.Scan(&rid, &p); err != nil {
			rows.Close()
			return fmt.Errorf("scan current priority: %w", err)
		}
		currentPriority[rid] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate current priorities: %w", err)
	}

	for idx, rid := range newOrder {
		newPriority := idx + 1
		if currentPriority[rid] == newPriority {
			continue
		}
		if _, execErr := s.updatePriorityTx(ctx, tx, scope, rid, newPriority); execErr != nil {
			return fmt.Errorf("apply move priority: %w", execErr)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit move: %w", err)
	}
	s.invalidateScope(scope)
	return nil
}

// JanitorDeletePendingAuthOlderThan drops pending_auth rows older than ttl.
// Driven by an external cron — see design doc § "Pending-auth TTL".
//
// No resolver-cache invalidation: pending_auth rows are filtered out of
// ListResolvable (status = 'active' only), so they never enter the cache to
// begin with. The cache hit path is unaffected by this sweep.
//
// lint:allow-no-orgid reason="cross-org janitor sweep; runs as a system task with no caller scope"
func (s *CodingCredentialStore) JanitorDeletePendingAuthOlderThan(ctx context.Context, ttl time.Duration) (int64, error) {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM coding_credentials
		 WHERE status = 'pending_auth' AND created_at < now() - @ttl::interval`,
		pgx.NamedArgs{"ttl": fmt.Sprintf("%d seconds", int(ttl.Seconds()))},
	)
	if err != nil {
		return 0, fmt.Errorf("janitor sweep: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ----- Internals -----

// withScopedRowTx begins a transaction, locks the row identified by id with
// SELECT ... FOR UPDATE, asserts (org_id, user_id) matches scope, and invokes
// fn with the tx and the row's provider. The fn must perform its UPDATE on tx
// so the work happens under the same lock. Scope mismatch is conflated with
// "row missing" so id enumeration cannot tell which is which.
func (s *CodingCredentialStore) withScopedRowTx(ctx context.Context, scope models.Scope, id uuid.UUID, fn func(tx pgx.Tx, provider models.ProviderName) error) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	provider, err := s.lockAndAssertScope(ctx, tx, scope, id)
	if err != nil {
		return err
	}
	if err := fn(tx, provider); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// lockAndAssertScope loads (org_id, user_id, provider) under a row lock and
// returns the provider when the row matches scope. Returns
// ErrCodingCredentialNotFound for any mismatch.
func (s *CodingCredentialStore) lockAndAssertScope(ctx context.Context, tx pgx.Tx, scope models.Scope, id uuid.UUID) (models.ProviderName, error) {
	var orgID uuid.UUID
	var userID *uuid.UUID
	var provider string
	args := pgx.NamedArgs{"id": id, "org_id": scope.OrgID}
	var query string
	if scope.IsPersonal() {
		args["user_id"] = *scope.UserID
		query = `SELECT org_id, user_id, provider FROM coding_credentials
			WHERE id = @id AND org_id = @org_id AND user_id = @user_id
			FOR UPDATE`
	} else {
		query = `SELECT org_id, user_id, provider FROM coding_credentials
			WHERE id = @id AND org_id = @org_id AND user_id IS NULL
			FOR UPDATE`
	}
	err := tx.QueryRow(ctx, query, args).Scan(&orgID, &userID, &provider)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrCodingCredentialNotFound
		}
		return "", fmt.Errorf("scope-check credential: %w", err)
	}
	if orgID != scope.OrgID || !sameUserPointer(userID, scope.UserID) {
		return "", ErrCodingCredentialNotFound
	}
	return models.ProviderName(provider), nil
}

// assertScopeAndProviderTx is the legacy two-return-value variant kept for
// the Reorder/Move callers that don't need the row's provider — they only
// need to verify the row belongs to the scope before issuing per-row UPDATEs.
func (s *CodingCredentialStore) assertScopeAndProviderTx(ctx context.Context, tx pgx.Tx, scope models.Scope, id uuid.UUID) error {
	_, err := s.lockAndAssertScope(ctx, tx, scope, id)
	return err
}

func sameUserPointer(a, b *uuid.UUID) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func (s *CodingCredentialStore) fetchRowByID(ctx context.Context, scope models.Scope, id uuid.UUID) (*models.CodingCredential, error) {
	args := pgx.NamedArgs{"id": id, "org_id": scope.OrgID}
	var query string
	if scope.IsPersonal() {
		args["user_id"] = *scope.UserID
		query = `SELECT ` + codingCredentialsColumns + ` FROM coding_credentials
			WHERE id = @id AND org_id = @org_id AND user_id = @user_id`
	} else {
		query = `SELECT ` + codingCredentialsColumns + ` FROM coding_credentials
			WHERE id = @id AND org_id = @org_id AND user_id IS NULL`
	}
	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query credential: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.CodingCredential])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCodingCredentialNotFound
		}
		return nil, fmt.Errorf("fetch credential: %w", err)
	}
	if row.OrgID != scope.OrgID {
		return nil, ErrCodingCredentialNotFound
	}
	if !sameUserPointer(row.UserID, scope.UserID) {
		return nil, ErrCodingCredentialNotFound
	}
	return &row, nil
}

func (s *CodingCredentialStore) fetchStackTx(ctx context.Context, tx pgx.Tx, scope models.Scope) ([]uuid.UUID, error) {
	args := pgx.NamedArgs{"org_id": scope.OrgID}
	var query string
	if scope.IsPersonal() {
		args["user_id"] = *scope.UserID
		query = `SELECT id FROM coding_credentials
			WHERE org_id = @org_id AND user_id = @user_id AND status != 'disabled'
			ORDER BY priority, created_at`
	} else {
		query = `SELECT id FROM coding_credentials
			WHERE org_id = @org_id AND user_id IS NULL AND status != 'disabled'
			ORDER BY priority, created_at`
	}
	rows, err := tx.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("fetch stack: %w", err)
	}
	defer rows.Close()
	out := make([]uuid.UUID, 0)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan stack id: %w", err)
		}
		out = append(out, id)
	}
	return out, nil
}

func sameUUIDSet(a, b []uuid.UUID) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[uuid.UUID]int, len(a))
	for _, id := range a {
		seen[id]++
	}
	for _, id := range b {
		if seen[id] == 0 {
			return false
		}
		seen[id]--
		if seen[id] == 0 {
			delete(seen, id)
		}
	}
	return len(seen) == 0
}

// withScopeLock acquires a per-scope advisory lock to serialize stack-priority
// updates inside the surrounding transaction. Without it, concurrent Create,
// Reorder, and Move calls could compute from stale priorities and emit
// duplicate slot numbers. Priority is per-stack (not per-provider), so the
// lock key omits provider — concurrent writes for different providers in the
// same scope must still serialize through the same lock.
func (s *CodingCredentialStore) withScopeLock(ctx context.Context, scope models.Scope, fn func(tx pgx.Tx, nextPriority int) error) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := s.acquireScopeLockTx(ctx, tx, scope); err != nil {
		return err
	}

	args := pgx.NamedArgs{"org_id": scope.OrgID}
	var query string
	if scope.IsPersonal() {
		args["user_id"] = *scope.UserID
		query = `SELECT COALESCE(MAX(priority), 0) + 1 FROM coding_credentials
			WHERE org_id = @org_id AND user_id = @user_id AND status != 'disabled'`
	} else {
		query = `SELECT COALESCE(MAX(priority), 0) + 1 FROM coding_credentials
			WHERE org_id = @org_id AND user_id IS NULL AND status != 'disabled'`
	}
	var nextPriority int
	if err := tx.QueryRow(ctx, query, args).Scan(&nextPriority); err != nil {
		return fmt.Errorf("compute next priority: %w", err)
	}

	if err := fn(tx, nextPriority); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *CodingCredentialStore) acquireScopeLockTx(ctx context.Context, tx pgx.Tx, scope models.Scope) error {
	lockKey := fmt.Sprintf("coding_credentials:%s:%s", scope.OrgID, scopePtrKey(scope.UserID))
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		lockKey,
	); err != nil {
		return fmt.Errorf("acquire scope lock: %w", err)
	}
	return nil
}

func (s *CodingCredentialStore) updatePriorityTx(ctx context.Context, tx pgx.Tx, scope models.Scope, id uuid.UUID, priority int) (pgconn.CommandTag, error) {
	args := pgx.NamedArgs{"priority": priority, "id": id, "org_id": scope.OrgID}
	if scope.IsPersonal() {
		args["user_id"] = *scope.UserID
		return tx.Exec(ctx,
			`UPDATE coding_credentials
			 SET priority = @priority, updated_at = now()
			 WHERE id = @id AND org_id = @org_id AND user_id = @user_id`,
			args,
		)
	}
	return tx.Exec(ctx,
		`UPDATE coding_credentials
		 SET priority = @priority, updated_at = now()
		 WHERE id = @id AND org_id = @org_id AND user_id IS NULL`,
		args,
	)
}

func scopePtrKey(u *uuid.UUID) string {
	if u == nil {
		return "org"
	}
	return u.String()
}

func (s *CodingCredentialStore) beginTx(ctx context.Context) (pgx.Tx, error) {
	starter, ok := s.db.(TxStarter)
	if !ok {
		return nil, errors.New("coding credential store db does not support transactions")
	}
	tx, err := starter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	return tx, nil
}

func (s *CodingCredentialStore) marshalAndEncrypt(cfg models.ProviderConfig) ([]byte, error) {
	plaintext, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	if s.crypto != nil {
		ct, encErr := s.crypto.Encrypt(plaintext)
		if encErr != nil {
			return nil, fmt.Errorf("encrypt config: %w", encErr)
		}
		return ct, nil
	}
	return crypto.DevEncrypt(plaintext), nil
}

func (s *CodingCredentialStore) decrypt(data []byte) ([]byte, error) {
	if s.crypto != nil {
		return s.crypto.Decrypt(data)
	}
	return crypto.DevDecrypt(data)
}

func (s *CodingCredentialStore) decryptRow(row models.CodingCredential) (*models.DecryptedCodingCredential, error) {
	plaintext, err := s.decrypt(row.Config)
	if err != nil {
		return nil, fmt.Errorf("decrypt %s config: %w", row.Provider, err)
	}
	cfg, err := models.ParseCodingProviderConfig(row.Provider, plaintext)
	if err != nil {
		return nil, fmt.Errorf("parse %s config: %w", row.Provider, err)
	}
	return &models.DecryptedCodingCredential{
		ID:                    row.ID,
		OrgID:                 row.OrgID,
		UserID:                row.UserID,
		Provider:              row.Provider,
		Label:                 row.Label,
		Config:                cfg,
		Priority:              row.Priority,
		Status:                row.Status,
		CreatedBy:             row.CreatedBy,
		LastVerifiedAt:        row.LastVerifiedAt,
		RateLimitedUntil:      row.RateLimitedUntil,
		RateLimitedObservedAt: row.RateLimitedObservedAt,
		RateLimitMessage:      row.RateLimitMessage,
		CreatedAt:             row.CreatedAt,
		UpdatedAt:             row.UpdatedAt,
	}, nil
}

func (s *CodingCredentialStore) decryptRows(rows []models.CodingCredential) ([]models.DecryptedCodingCredential, error) {
	out := make([]models.DecryptedCodingCredential, 0, len(rows))
	for _, row := range rows {
		dec, err := s.decryptRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, *dec)
	}
	return out, nil
}

func (s *CodingCredentialStore) invalidate(scope models.Scope, provider models.ProviderName) {
	s.resolverCache.invalidate(scope.OrgID, scope.UserID, provider)
	// An org-row mutation can affect every personal user's resolved view.
	// We don't track individual user_ids in the cache key for invalidation,
	// so a coarse scope-level wipe is the safe thing to do for org changes.
	//
	// The personal branch only invalidates the creating user's key on purpose:
	// a personal-scoped row is never visible to another user's resolver, so
	// no broader wipe is needed. If a future change ever lets one user's
	// personal row affect another user's resolution (e.g. introducing a
	// "team default" flag on the personal table), this invalidate must also
	// fan out — otherwise stale cached resolutions outlive the write.
	if scope.IsOrg() {
		s.resolverCache.invalidateOrg(scope.OrgID, provider)
	}
}

// invalidateScope drops cache entries affected by a stack-level mutation
// (Reorder, Move). Personal mutations only affect the requesting user's
// resolved view, so we wipe just that user's keys instead of the whole org —
// the org tail concatenated onto every other user's resolution is unchanged.
// Org mutations affect every user because their resolved lists end with the
// org rows.
func (s *CodingCredentialStore) invalidateScope(scope models.Scope) {
	if scope.IsPersonal() {
		s.resolverCache.invalidateUser(scope.OrgID, *scope.UserID)
		return
	}
	s.resolverCache.invalidateAll(scope.OrgID)
}

func indexOf(s []uuid.UUID, target uuid.UUID) int {
	for i, v := range s {
		if v == target {
			return i
		}
	}
	return -1
}

// ----- resolver cache -----

type resolverCacheKey struct {
	orgID    uuid.UUID
	userKey  uuid.UUID // zero UUID for org-scoped
	provider models.ProviderName
}

type resolverCacheEntry struct {
	value  []models.DecryptedCodingCredential
	expiry time.Time
}

type resolverCache struct {
	mu    sync.RWMutex
	ttl   time.Duration
	data  map[resolverCacheKey]resolverCacheEntry
	clock func() time.Time
}

func newResolverCache(ttl time.Duration) *resolverCache {
	return &resolverCache{
		ttl:   ttl,
		data:  make(map[resolverCacheKey]resolverCacheEntry),
		clock: time.Now,
	}
}

func (c *resolverCache) keyFor(orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) resolverCacheKey {
	uk := uuid.Nil
	if userID != nil {
		uk = *userID
	}
	return resolverCacheKey{orgID: orgID, userKey: uk, provider: provider}
}

func (c *resolverCache) get(orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) ([]models.DecryptedCodingCredential, bool) {
	if c == nil {
		return nil, false
	}
	key := c.keyFor(orgID, userID, provider)
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.data[key]
	if !ok {
		return nil, false
	}
	if c.clock().After(entry.expiry) {
		return nil, false
	}
	// Shallow copy: the slice header is fresh, but the
	// DecryptedCodingCredential structs (and any pointer fields they hold —
	// UserID, CreatedBy, LastVerifiedAt) still alias the cached entry.
	// Callers MUST treat the return value as read-only; mutating a pointer
	// field will corrupt the next cache hit. The runtime callers (resolver
	// + Pick paths) only ever read, so this is sufficient and avoids the
	// per-hit allocation cost of a deep copy on the hottest path in the
	// store.
	out := make([]models.DecryptedCodingCredential, len(entry.value))
	copy(out, entry.value)
	return out, true
}

func (c *resolverCache) put(orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName, val []models.DecryptedCodingCredential) {
	if c == nil {
		return
	}
	stored := make([]models.DecryptedCodingCredential, len(val))
	copy(stored, val)
	key := c.keyFor(orgID, userID, provider)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = resolverCacheEntry{value: stored, expiry: c.clock().Add(c.ttl)}
}

func (c *resolverCache) invalidate(orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) {
	if c == nil {
		return
	}
	key := c.keyFor(orgID, userID, provider)
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.data, key)
}

// invalidateOrg drops every cache entry in the org for one provider — a
// coarse wipe used after org-row changes that can affect every user's
// resolved view.
func (c *resolverCache) invalidateOrg(orgID uuid.UUID, provider models.ProviderName) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.data {
		if k.orgID == orgID && k.provider == provider {
			delete(c.data, k)
		}
	}
}

// invalidateAll drops every cache entry in the org. Used when the entire
// stack is reordered.
func (c *resolverCache) invalidateAll(orgID uuid.UUID) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.data {
		if k.orgID == orgID {
			delete(c.data, k)
		}
	}
}

// invalidateUser drops every cache entry in the org that belongs to one
// user's personal stack. Used by personal-scope Reorder/Move so an unrelated
// user's resolver cache is not blown away by another user's drag-drop.
func (c *resolverCache) invalidateUser(orgID uuid.UUID, userID uuid.UUID) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.data {
		if k.orgID == orgID && k.userKey == userID {
			delete(c.data, k)
		}
	}
}

// ----- health cache -----

type healthCache struct {
	mu    sync.RWMutex
	ttl   time.Duration
	data  map[uuid.UUID]time.Time
	clock func() time.Time
}

func newHealthCache(ttl time.Duration) *healthCache {
	return &healthCache{
		ttl:   ttl,
		data:  make(map[uuid.UUID]time.Time),
		clock: time.Now,
	}
}

func (h *healthCache) shed(id uuid.UUID) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.data[id] = h.clock().Add(h.ttl)
}

func (h *healthCache) isShed(id uuid.UUID) bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	expiry, ok := h.data[id]
	h.mu.RUnlock()
	if !ok {
		return false
	}
	if h.clock().After(expiry) {
		// Lazy clean up expired entry.
		h.mu.Lock()
		if exp, ok := h.data[id]; ok && h.clock().After(exp) {
			delete(h.data, id)
		}
		h.mu.Unlock()
		return false
	}
	return true
}

func (h *healthCache) filter(creds []models.DecryptedCodingCredential) []models.DecryptedCodingCredential {
	if h == nil {
		return creds
	}
	out := make([]models.DecryptedCodingCredential, 0, len(creds))
	for _, c := range creds {
		if !h.isShed(c.ID) {
			out = append(out, c)
		}
	}
	return out
}
