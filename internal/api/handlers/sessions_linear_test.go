package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/linear"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// fakeLinearLinker is a unit-test stub for the linearSessionLinker interface.
// CreateManual reaches it through SetLinearLinker; we can't use the real
// service in handler tests because it pulls in the integrations stack.
type fakeLinearLinker struct {
	called int
	gotIn  linear.CreateInput
	result linear.CreateResult
	err    error
	// disabled makes Enabled return false while preserving the default
	// active shape expected by most CreateManual tests.
	disabled bool
	// teamKeys is what TeamKeyAllowlist returns. Defaults to all-known so
	// existing tests that exercise issue-only session start (where the
	// MISSING_MESSAGE bypass requires the bare-identifier prefix to be in
	// the allowlist) continue to pass without each test having to wire it.
	teamKeys map[string]bool
	// midSessionMu guards the mid-session observation fields. SendMessage
	// dispatches the linker call to a detached goroutine, so the test
	// goroutine and the handler goroutine touch these concurrently — without
	// the mutex the race detector flags every assertion.
	midSessionMu     sync.Mutex
	midSessionCalled int
	midSessionGotIn  linear.MidSessionInput
	midSessionErr    error
}

func (f *fakeLinearLinker) Enabled(context.Context, uuid.UUID) bool {
	return !f.disabled
}

func (f *fakeLinearLinker) ResolveAndLinkAtCreate(_ context.Context, in linear.CreateInput) (linear.CreateResult, error) {
	f.called++
	f.gotIn = in
	if f.err != nil {
		return linear.CreateResult{}, f.err
	}
	return f.result, nil
}

func (f *fakeLinearLinker) ResolveAndLinkMidSession(_ context.Context, in linear.MidSessionInput) error {
	f.midSessionMu.Lock()
	defer f.midSessionMu.Unlock()
	f.midSessionCalled++
	f.midSessionGotIn = in
	return f.midSessionErr
}

// midSessionObservation snapshots the mid-session call counters under the
// mutex so callers can assert without racing the handler goroutine.
func (f *fakeLinearLinker) midSessionObservation() (int, linear.MidSessionInput) {
	f.midSessionMu.Lock()
	defer f.midSessionMu.Unlock()
	return f.midSessionCalled, f.midSessionGotIn
}

func (f *fakeLinearLinker) TeamKeyAllowlist(_ context.Context, _ uuid.UUID) (map[string]bool, error) {
	if f.teamKeys != nil {
		return f.teamKeys, nil
	}
	// Permissive default — every common bare-identifier prefix in test
	// fixtures resolves. Tests that need to exercise the "unknown team"
	// branch should set teamKeys to a tighter map (or {}).
	return map[string]bool{"ACS": true}, nil
}

type countingLinearTitleLLM struct {
	called int
}

func (c *countingLinearTitleLLM) Complete(context.Context, string, string) (string, error) {
	c.called++
	return "LLM title should not be used", nil
}

func TestLooksLikeLinearReference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		refs []models.SessionInputReference
		want bool
	}{
		{name: "empty slice", refs: nil, want: false},
		{
			name: "linear url in display",
			refs: []models.SessionInputReference{{Display: "https://linear.app/acme/issue/ACS-1234"}},
			want: true,
		},
		{
			name: "bare linear-shaped identifier in display",
			refs: []models.SessionInputReference{{Display: "ACS-1234"}},
			want: true,
		},
		{
			name: "non-linear text",
			refs: []models.SessionInputReference{{Display: "internal/api/handlers/sessions.go"}},
			want: false,
		},
		{
			name: "linear ref hidden behind a non-linear ref still counts",
			refs: []models.SessionInputReference{
				{Display: "internal/api/handlers/sessions.go"},
				{Display: "https://linear.app/acme/issue/ACS-1234"},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, looksLikeLinearReference(tt.refs))
		})
	}
}

func TestShouldOverrideTitleWithLinearIssue(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		current *string
		want    bool
	}{
		{name: "nil title", current: nil, want: true},
		{name: "empty string", current: stringPtr(""), want: true},
		{name: "whitespace only", current: stringPtr("   \t\n"), want: true},
		{name: "default placeholder", current: stringPtr(defaultManualSessionTitle), want: true},
		{name: "user-set title", current: stringPtr("Fix the OAuth bug"), want: false},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, shouldOverrideTitleWithLinearIssue(tt.current))
		})
	}
}

func TestSessionHandler_SetLinearLinker(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := newSessionHandler(t, mock)
	require.Nil(t, handler.getLinearLinker(), "linker should default to nil so CreateManual treats Linear refs as opaque text")

	handler.SetLinearLinker(&fakeLinearLinker{})
	require.NotNil(t, handler.getLinearLinker(), "SetLinearLinker should wire the injected linker so CreateManual can resolve the primary issue")
}

// TestSessionHandler_CreateManual_LinearLinkerNilWithFlags verifies that the
// dogfood-warning branch fires (and doesn't 4xx) when LinearPrivate is set
// but no Linear linker is wired. The flags must still persist so a later
// integration install backfills the right policy — see the handler comment
// at the call site.
func TestSessionHandler_CreateManual_LinearLinkerNilWithFlags(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now()
	runID := uuid.New()
	messageID := int64(1)
	jobID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "test-org", nil, now, now))
	expectManualSessionCreate(mock, runID, now)
	mock.ExpectQuery("INSERT INTO session_messages").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(messageID, now))
	mock.ExpectQuery("SELECT count").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	handler := newSessionHandler(t, mock)
	// Intentionally NOT calling SetLinearLinker — the warning branch only
	// fires when h.linearLinker is nil but the body opted into the flags.
	require.Nil(t, handler.getLinearLinker())

	body := `{"message":"Fix the login bug","agent_type":"claude_code","linear_private":true,"linear_state_sync_disabled":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/manual", strings.NewReader(body))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreateManual(w, req)

	require.Equal(t, http.StatusCreated, w.Code, "session create should still succeed when the linker is unwired")
	require.Contains(t, w.Body.String(), "claude_code")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_CreateManual_EmptyLinearURLRequiresActiveLinear(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)
	linker := &fakeLinearLinker{disabled: true}
	handler.SetLinearLinker(linker)

	body := `{"message":"","agent_type":"claude_code","references":[{"kind":"app","id":"linear","display":"https://linear.app/acme/issue/ACS-1234"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/manual", strings.NewReader(body))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.CreateManual(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "empty-message Linear URL starts should require an active Linear integration")
	require.Contains(t, w.Body.String(), "MISSING_MESSAGE", "disabled Linear should not authorize the issue-only bypass")
	require.Equal(t, 0, linker.called, "disabled Linear should be rejected before the linker performs side effects")
	require.NoError(t, mock.ExpectationsWereMet(), "disabled empty-message bypass should not touch the database")
}

// TestSessionHandler_CreateManual_LinearLinkerSuccess covers the happy
// path: linker resolves a primary, the handler persists the identifier
// hint, overrides the placeholder title with the issue title, and proceeds
// to enqueue the agent run.
func TestSessionHandler_CreateManual_LinearLinkerSuccess(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now()
	runID := uuid.New()
	messageID := int64(1)
	jobID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "test-org", nil, now, now))
	expectManualSessionCreate(mock, runID, now)
	mock.ExpectQuery("INSERT INTO session_messages").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(messageID, now))

	mock.ExpectQuery("SELECT count").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	// SetLinearIdentifierHint UPDATE.
	mock.ExpectExec("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// UpdateTitle UPDATE — fired because the session arrived with no
	// user-supplied subject (empty message exercising the issue-only fast
	// path) so the linker title takes over.
	mock.ExpectExec("UPDATE sessions SET title").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	handler := newSessionHandler(t, mock)
	linker := &fakeLinearLinker{
		result: linear.CreateResult{
			PrepareInline:     true,
			PrimaryIdentifier: "ACS-1234",
			PrimaryTitle:      "Fix the OAuth callback",
		},
	}
	handler.SetLinearLinker(linker)

	// Empty message body + Linear-shaped reference exercises both
	// looksLikeLinearReference (so MISSING_MESSAGE doesn't 400) and the
	// shouldOverrideTitleWithLinearIssue branch (the manualSessionTitle
	// placeholder gets replaced by the linker's PrimaryTitle). The
	// reference uses a real picker kind ("app") whose display we set to
	// the Linear key — looksLikeLinearReference scans the display string
	// regardless of kind.
	body := `{"message":"","agent_type":"claude_code","references":[{"kind":"app","id":"linear","display":"ACS-1234"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/manual", strings.NewReader(body))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreateManual(w, req)

	require.Equal(t, http.StatusCreated, w.Code, "linker success path should still create the session")
	require.Equal(t, 1, linker.called, "the handler should call the linker exactly once per create")
	require.Equal(t, orgID, linker.gotIn.OrgID)
	require.Contains(t, linker.gotIn.ReferenceText, "ACS-1234", "the reference text should be threaded through to the linker")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_CreateManual_LinearReferenceIDIsSentToLinker(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now()
	runID := uuid.New()
	messageID := int64(1)
	jobID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "test-org", nil, now, now))
	expectManualSessionCreate(mock, runID, now)
	mock.ExpectQuery("INSERT INTO session_messages").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(messageID, now))
	mock.ExpectQuery("SELECT count").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	handler := newSessionHandler(t, mock)
	linker := &fakeLinearLinker{}
	handler.SetLinearLinker(linker)

	body := `{"message":"","agent_type":"claude_code","references":[{"kind":"app","id":"ACS-1234","display":"Linear issue"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/manual", strings.NewReader(body))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.CreateManual(w, req)

	require.Equal(t, http.StatusCreated, w.Code, "ID-only Linear reference should create an issue-only session")
	require.Equal(t, 1, linker.called, "the handler should call the linker")
	require.Contains(t, linker.gotIn.ReferenceText, "ACS-1234", "reference IDs should be threaded through to the linker")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreateManual_ChecksConcurrencyBeforeLinearLinking(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now()
	runID := uuid.New()
	messageID := int64(1)
	settings := `{"max_concurrent_runs":1}`

	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "test-org", []byte(settings), now, now))
	expectManualSessionCreate(mock, runID, now)
	mock.ExpectQuery("INSERT INTO session_messages").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(messageID, now))
	mock.ExpectQuery("SELECT count").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))

	handler := newSessionHandler(t, mock)
	linker := &fakeLinearLinker{}
	handler.SetLinearLinker(linker)

	body := `{"message":"Fix ACS-1234","agent_type":"claude_code"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/manual", strings.NewReader(body))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.CreateManual(w, req)

	require.Equal(t, http.StatusTooManyRequests, w.Code, "concurrency limit should reject the session")
	require.Equal(t, 0, linker.called, "the handler should not perform Linear side effects after a concurrency rejection")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreateManual_LinearIssueOnlySkipsLLMTitleGeneration(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now()
	runID := uuid.New()
	messageID := int64(1)
	jobID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "test-org", nil, now, now))
	expectManualSessionCreate(mock, runID, now)
	mock.ExpectQuery("INSERT INTO session_messages").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(messageID, now))
	mock.ExpectQuery("SELECT count").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE sessions SET title").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	handler := newSessionHandler(t, mock)
	llm := &countingLinearTitleLLM{}
	handler.llmClient = llm
	handler.SetLinearLinker(&fakeLinearLinker{
		result: linear.CreateResult{
			PrepareInline:     true,
			PrimaryIdentifier: "ACS-1234",
			PrimaryTitle:      "Fix the OAuth callback",
		},
	})

	body := `{"message":"","agent_type":"claude_code","references":[{"kind":"app","id":"linear","display":"ACS-1234"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/manual", strings.NewReader(body))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.CreateManual(w, req)

	require.Equal(t, http.StatusCreated, w.Code, "Linear issue-only create should succeed")
	require.Equal(t, 0, llm.called, "empty issue-only sessions should keep the Linear title and skip LLM title generation")
	require.Contains(t, w.Body.String(), "Fix the OAuth callback", "response should keep the Linear issue title")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// TestSessionHandler_CreateManual_LinearLinkerError covers the failure
// branch: the linker returns an error, the handler marks the session
// "failed" via a detached cleanup context, and returns 500. Critically,
// we must not enqueue the agent run — verifying the absence of an INSERT
// INTO jobs expectation is part of the contract.
func TestSessionHandler_CreateManual_LinearLinkerError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now()
	runID := uuid.New()
	messageID := int64(1)

	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "test-org", nil, now, now))
	expectManualSessionCreate(mock, runID, now)
	mock.ExpectQuery("INSERT INTO session_messages").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(messageID, now))
	mock.ExpectQuery("SELECT count").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

	// UpdateResult on the failure path: the handler issues an UPDATE
	// sessions ... RETURNING via session_store.updateResultRow. Return a
	// row so the scan succeeds and the cleanup write completes cleanly.
	mock.ExpectQuery("UPDATE sessions").
		WithArgs(sessionAnyArgs(13)...).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, uuid.Nil, orgID, "claude_code", "failed", "auto", "standard",
				nil, nil, nil, nil,
				nil, false, nil, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "idle", nil,
				nil, nil, nil, nil, nil, nil, nil,
				nil, nil,
				nil,
				"none", (*string)(nil),
				nil,
				nil,
				nil,
				now,
			),
		)

	handler := newSessionHandler(t, mock)
	linkerErr := errors.New("linear graphql 401")
	linker := &fakeLinearLinker{err: linkerErr}
	handler.SetLinearLinker(linker)

	body := `{"message":"Fix the login bug ACS-1234","agent_type":"claude_code"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/manual", strings.NewReader(body))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreateManual(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "linker failure should surface as 500")
	require.Contains(t, w.Body.String(), "LINEAR_PREPARE_FAILED")
	require.Equal(t, 1, linker.called)
	require.NoError(t, mock.ExpectationsWereMet(), "the handler must not enqueue the agent run after a linker failure")
}

// TestSessionHandler_CreateManual_RejectsLinearOverridesWhenAdminLockedOut
// pins the admin gate on the per-session Linear policy flags. When an org
// admin has set linear_automation.allow_per_session_overrides=false, a user
// who tries to create a session with linear_private or
// linear_state_sync_disabled must get 403 — not a silently-honored override
// that violates the org's "every session syncs to Linear" policy.
//
// The mock asserts ZERO writes after the org lookup: any session insert,
// message insert, or job enqueue would mean the gate failed open.
func TestSessionHandler_CreateManual_RejectsLinearOverridesWhenAdminLockedOut(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{
			name: "linear_private blocked",
			body: `{"message":"Fix the login bug","agent_type":"claude_code","linear_private":true}`,
		},
		{
			name: "linear_state_sync_disabled blocked",
			body: `{"message":"Fix the login bug","agent_type":"claude_code","linear_state_sync_disabled":true}`,
		},
		{
			name: "both flags blocked",
			body: `{"message":"Fix the login bug","agent_type":"claude_code","linear_private":true,"linear_state_sync_disabled":true}`,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			orgID := uuid.New()
			now := time.Now()

			// Settings JSON with the override gate explicitly disabled. The
			// pointer-typed bool is what makes nil-vs-false distinguishable;
			// we serialize the explicit-false form here.
			settings := []byte(`{"linear_automation":{"allow_per_session_overrides":false}}`)
			mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
				WithArgs(pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
					AddRow(orgID, "test-org", settings, now, now))

			handler := newSessionHandler(t, mock)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/manual", strings.NewReader(tt.body))
			ctx := middleware.WithOrgID(req.Context(), orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.CreateManual(w, req)

			require.Equal(t, http.StatusForbidden, w.Code, "org admin gate must reject per-session Linear overrides with 403")
			require.Contains(t, w.Body.String(), "LINEAR_PER_SESSION_OVERRIDES_DISABLED", "error code must identify the gate so clients can surface a useful message")
			require.NoError(t, mock.ExpectationsWereMet(), "no session, message, or job inserts should fire when the gate rejects the request")
		})
	}
}

// TestSessionHandler_CreateManual_AllowsLinearOverridesByDefault is the
// other half of the gate contract: the default org settings (no explicit
// allow_per_session_overrides value) must NOT regress current behavior.
// Users continue to set linear_private freely until an admin opts in.
func TestSessionHandler_CreateManual_AllowsLinearOverridesByDefault(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now()
	runID := uuid.New()
	messageID := int64(1)
	jobID := uuid.New()

	// Default settings — no linear_automation key at all means the gate
	// stays open per EffectiveAllowPerSessionOverrides()'s nil → true rule.
	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "test-org", nil, now, now))
	expectManualSessionCreate(mock, runID, now)
	mock.ExpectQuery("INSERT INTO session_messages").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(messageID, now))
	mock.ExpectQuery("SELECT count").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	handler := newSessionHandler(t, mock)

	body := `{"message":"Fix the login bug","agent_type":"claude_code","linear_private":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/manual", strings.NewReader(body))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreateManual(w, req)

	require.Equal(t, http.StatusCreated, w.Code, "default settings must keep the per-session override path open")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestSessionHandler_SendMessage_FiresMidSessionLinker pins the contract for
// follow-up Linear linking. When a user posts a message into an existing
// session and the body contains a Linear ref, the handler must hand it off to
// the linker — otherwise the in-session "Link Linear issue" affordance is a
// label without a backing action and the LinkedIssueChips list never updates.
func TestSessionHandler_SendMessage_FiresMidSessionLinker(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()

	// Running-session fast path: no tx, no enqueue. We pin this branch
	// because it's the leanest mock; the linker call site is shared across
	// the other two branches and is exercised by the unit test above.
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil, // triggered_by_user_id
				nil, 2, now, "running", nil,
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)
	mock.ExpectQuery("INSERT INTO session_messages").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))

	handler := newSessionHandler(t, mock)
	linker := &fakeLinearLinker{}
	handler.SetLinearLinker(linker)

	body := `{"message":"Could you also look at https://linear.app/acme/issue/ACS-9?"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/messages", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "member"})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.SendMessage(w, req)

	require.Equal(t, http.StatusCreated, w.Code, "follow-up should still succeed")
	// SendMessage dispatches the linker call to a detached goroutine so the
	// running-session fast path doesn't pay the allowlist+enqueue latency.
	// Wait for it before asserting — the assertion otherwise races the
	// goroutine and flakes on slow CI.
	require.Eventually(t, func() bool {
		called, _ := linker.midSessionObservation()
		return called == 1
	}, time.Second, 5*time.Millisecond, "SendMessage must hand follow-up bodies to the mid-session linker so the in-session linking action backs onto a real link row")
	called, gotIn := linker.midSessionObservation()
	require.Equal(t, 1, called)
	require.Equal(t, orgID, gotIn.OrgID, "mid-session call should preserve org scope")
	require.Equal(t, sessionID, gotIn.SessionID, "mid-session call should target the receiving session")
	require.Contains(t, gotIn.MessageBody, "ACS-9", "the linker should see the user-typed body so detection can find the Linear ref")
	require.NotNil(t, gotIn.UserID, "linking attribution should carry the sender's user id")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// TestSessionHandler_SendMessage_SwallowsMidSessionLinkerError is the fail-
// soft contract: even if the linker returns an error (Linear API outage,
// allowlist DB hiccup), the user's follow-up message must still succeed and
// return 201. The linker's failure is logged but never bubbles into the
// response — matching the design 62 "no Linear writes block the user".
func TestSessionHandler_SendMessage_SwallowsMidSessionLinkerError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 2, now, "running", nil,
				nil, nil, nil, nil, nil, nil,
				nil, nil,
				nil,
				"idle",
				(*string)(nil),
				nil,
				now,
			),
		)
	mock.ExpectQuery("INSERT INTO session_messages").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))

	handler := newSessionHandler(t, mock)
	linker := &fakeLinearLinker{midSessionErr: errors.New("linear unreachable")}
	handler.SetLinearLinker(linker)

	body := `{"message":"see ACS-9"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/messages", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "member"})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.SendMessage(w, req)

	require.Equal(t, http.StatusCreated, w.Code, "linker errors must not surface to the user; the message itself succeeded")
	// Async dispatch — wait before asserting that the linker was reached at
	// all. The error path is still fail-soft: the goroutine logs and exits.
	require.Eventually(t, func() bool {
		called, _ := linker.midSessionObservation()
		return called == 1
	}, time.Second, 5*time.Millisecond)
	require.NoError(t, mock.ExpectationsWereMet())
}
