package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/linear"
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
}

func (f *fakeLinearLinker) ResolveAndLinkAtCreate(_ context.Context, in linear.CreateInput) (linear.CreateResult, error) {
	f.called++
	f.gotIn = in
	if f.err != nil {
		return linear.CreateResult{}, f.err
	}
	return f.result, nil
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
	require.Nil(t, handler.linearLinker, "linker should default to nil so CreateManual treats Linear refs as opaque text")

	handler.SetLinearLinker(&fakeLinearLinker{})
	require.NotNil(t, handler.linearLinker, "SetLinearLinker should wire the injected linker so CreateManual can resolve the primary issue")
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
	require.Nil(t, handler.linearLinker)

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

	mock.ExpectQuery("SELECT count").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
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
	require.Equal(t, "ACS-1234", linker.gotIn.ReferenceText, "the reference text should be threaded through to the linker")
	require.NoError(t, mock.ExpectationsWereMet())
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
