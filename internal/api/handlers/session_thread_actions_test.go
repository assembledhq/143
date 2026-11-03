package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/thread"
)

// mockFileEventStoreForHandlers is the handler-package version of the
// service-package mock. Keeping it next to the handler tests avoids
// reaching into another test binary's symbols.
type mockFileEventStoreForHandlers struct {
	listBySessionFn func(ctx context.Context, orgID, sessionID uuid.UUID, since *time.Time) ([]models.SessionThreadFileEvent, error)
}

func (m *mockFileEventStoreForHandlers) ListBySession(ctx context.Context, orgID, sessionID uuid.UUID, since *time.Time) ([]models.SessionThreadFileEvent, error) {
	if m.listBySessionFn != nil {
		return m.listBySessionFn(ctx, orgID, sessionID, since)
	}
	return nil, nil
}

// newThreadHandlerWithFileEvents builds a handler with the optional
// file-event store wired. ListThreadFileEvents tests need it; tests for
// cancel / fork / revert do not.
func newThreadHandlerWithFileEvents(t *testing.T) (*SessionThreadHandler, *threadTestDeps, *mockFileEventStoreForHandlers) {
	t.Helper()
	deps := &threadTestDeps{
		threadStore:  &mockThreadStore{},
		sessionStore: &mockSessionStoreForThread{},
		messageStore: &mockMessageStore{},
		logStore:     &mockLogStore{},
		jobStore:     &mockJobStore{},
	}
	svc := thread.NewService(
		deps.threadStore,
		deps.sessionStore,
		deps.messageStore,
		deps.logStore,
		deps.jobStore,
		zerolog.Nop(),
	)
	fileEvents := &mockFileEventStoreForHandlers{}
	svc.SetFileEventStore(fileEvents)
	return NewSessionThreadHandler(svc), deps, fileEvents
}

// ----------------------------------------------------------------------------
// CancelThread
// ----------------------------------------------------------------------------

func TestSessionThreadHandler_CancelThread(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	tests := []struct {
		name         string
		setupDeps    func(deps *threadTestDeps)
		expectedCode int
		expectedKey  string
	}{
		{
			name: "200 on a running thread",
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusRunning}, nil
				}
			},
			expectedCode: http.StatusOK,
		},
		{
			name: "404 when the thread does not exist",
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, errors.New("missing")
				}
			},
			expectedCode: http.StatusNotFound,
			expectedKey:  "NOT_FOUND",
		},
		{
			name: "409 when the thread is not in an active state",
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusCompleted}, nil
				}
			},
			expectedCode: http.StatusConflict,
			expectedKey:  "NOT_CANCELLABLE",
		},
		{
			name: "404 when the thread is on a different session",
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: uuid.New(), OrgID: orgID, Status: models.ThreadStatusRunning}, nil
				}
			},
			expectedCode: http.StatusNotFound,
			expectedKey:  "NOT_FOUND",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h, deps := newThreadHandler(t)
			tt.setupDeps(deps)

			r := threadRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/threads/"+threadID.String()+"/cancel", "", orgID, map[string]string{
				"id":  sessionID.String(),
				"tid": threadID.String(),
			})
			w := httptest.NewRecorder()
			h.CancelThread(w, r)
			require.Equal(t, tt.expectedCode, w.Code)
			if tt.expectedKey != "" {
				require.Contains(t, w.Body.String(), tt.expectedKey, "error code should be surfaced")
			}
		})
	}
}

// ----------------------------------------------------------------------------
// ListThreadFileEvents
// ----------------------------------------------------------------------------

func TestSessionThreadHandler_ListThreadFileEvents_PassesSinceParam(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	since := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	h, deps, fileEvents := newThreadHandlerWithFileEvents(t)
	deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
		return models.Session{ID: sessionID, OrgID: orgID}, nil
	}
	var capturedSince *time.Time
	fileEvents.listBySessionFn = func(_ context.Context, _, _ uuid.UUID, s *time.Time) ([]models.SessionThreadFileEvent, error) {
		capturedSince = s
		return nil, nil
	}

	r := threadRequest(http.MethodGet, "/api/v1/sessions/"+sessionID.String()+"/thread-file-events?since="+since.Format(time.RFC3339Nano), "", orgID, map[string]string{
		"id": sessionID.String(),
	})
	w := httptest.NewRecorder()
	h.ListThreadFileEvents(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, capturedSince, "since should be parsed and passed to the service")
	require.True(t, capturedSince.Equal(since), "since timestamp must round-trip exactly")
}

func TestSessionThreadHandler_ListThreadFileEvents_BadSinceIsIgnored(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()

	h, deps, fileEvents := newThreadHandlerWithFileEvents(t)
	deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
		return models.Session{ID: sessionID, OrgID: orgID}, nil
	}
	var capturedSince *time.Time
	fileEvents.listBySessionFn = func(_ context.Context, _, _ uuid.UUID, s *time.Time) ([]models.SessionThreadFileEvent, error) {
		capturedSince = s
		return nil, nil
	}

	// Garbage `since` should not 400 — the endpoint is advisory and partial
	// data is preferred over a hard error.
	r := threadRequest(http.MethodGet, "/api/v1/sessions/"+sessionID.String()+"/thread-file-events?since=not-a-date", "", orgID, map[string]string{
		"id": sessionID.String(),
	})
	w := httptest.NewRecorder()
	h.ListThreadFileEvents(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	require.Nil(t, capturedSince, "unparseable since must fall back to nil, not pass through")
}

func TestSessionThreadHandler_ListThreadFileEvents_NeverNilArray(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()

	h, deps, fileEvents := newThreadHandlerWithFileEvents(t)
	deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
		return models.Session{ID: sessionID, OrgID: orgID}, nil
	}
	fileEvents.listBySessionFn = func(_ context.Context, _, _ uuid.UUID, _ *time.Time) ([]models.SessionThreadFileEvent, error) {
		return nil, nil
	}
	r := threadRequest(http.MethodGet, "/api/v1/sessions/"+sessionID.String()+"/thread-file-events", "", orgID, map[string]string{
		"id": sessionID.String(),
	})
	w := httptest.NewRecorder()
	h.ListThreadFileEvents(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	// Empty list should serialize as [] for the frontend, not null.
	require.Contains(t, w.Body.String(), `"data":[]`, "nil slice should serialize as empty array")
}

// ----------------------------------------------------------------------------
// ForkThread
// ----------------------------------------------------------------------------

func TestSessionThreadHandler_ForkThread(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	tests := []struct {
		name         string
		body         string
		setupDeps    func(deps *threadTestDeps)
		expectedCode int
		expectedKey  string
	}{
		{
			name: "202 with a job id on the happy path",
			body: `{"label":"Risky branch"}`,
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Label: "Codex"}, nil
				}
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID}, nil
				}
			},
			expectedCode: http.StatusAccepted,
		},
		{
			name: "202 with empty body — label is optional",
			body: ``,
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Label: "Codex"}, nil
				}
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID}, nil
				}
			},
			expectedCode: http.StatusAccepted,
		},
		{
			name: "404 when source thread is missing",
			body: `{}`,
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, errors.New("missing")
				}
			},
			expectedCode: http.StatusNotFound,
			expectedKey:  "NOT_FOUND",
		},
		{
			name: "400 when JSON body is malformed",
			body: `{not json`,
			setupDeps: func(deps *threadTestDeps) {
				// no-op — we should error before hitting any store.
			},
			expectedCode: http.StatusBadRequest,
			expectedKey:  "INVALID_BODY",
		},
		{
			name: "500 when enqueue fails",
			body: `{}`,
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID}, nil
				}
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID}, nil
				}
				deps.jobStore.enqueueFn = func(_ context.Context, _ uuid.UUID, _, _ string, _ any, _ int, _ *string) (uuid.UUID, error) {
					return uuid.Nil, errors.New("queue down")
				}
			},
			expectedCode: http.StatusInternalServerError,
			expectedKey:  "ENQUEUE_FAILED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h, deps := newThreadHandler(t)
			tt.setupDeps(deps)

			r := threadRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/threads/"+threadID.String()+"/fork", tt.body, orgID, map[string]string{
				"id":  sessionID.String(),
				"tid": threadID.String(),
			})
			w := httptest.NewRecorder()
			h.ForkThread(w, r)
			require.Equal(t, tt.expectedCode, w.Code, "body: %s", w.Body.String())
			if tt.expectedKey != "" {
				require.Contains(t, w.Body.String(), tt.expectedKey)
			}
		})
	}
}

// ----------------------------------------------------------------------------
// RevertThread
// ----------------------------------------------------------------------------

func TestSessionThreadHandler_RevertThread(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	diff := "diff --git a/foo b/foo\n--- a/foo\n+++ b/foo\n"

	tests := []struct {
		name         string
		setupDeps    func(deps *threadTestDeps)
		expectedCode int
		expectedKey  string
	}{
		{
			name: "202 on a thread with a diff",
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Diff: &diff}, nil
				}
			},
			expectedCode: http.StatusAccepted,
		},
		{
			name: "422 when the thread has no diff",
			setupDeps: func(deps *threadTestDeps) {
				empty := ""
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Diff: &empty}, nil
				}
			},
			expectedCode: http.StatusUnprocessableEntity,
			expectedKey:  "REVERT_FAILED",
		},
		{
			name: "404 when the thread is missing",
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, errors.New("missing")
				}
			},
			expectedCode: http.StatusNotFound,
			expectedKey:  "NOT_FOUND",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h, deps := newThreadHandler(t)
			tt.setupDeps(deps)

			r := threadRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/threads/"+threadID.String()+"/revert", "", orgID, map[string]string{
				"id":  sessionID.String(),
				"tid": threadID.String(),
			})
			w := httptest.NewRecorder()
			h.RevertThread(w, r)
			require.Equal(t, tt.expectedCode, w.Code, "body: %s", w.Body.String())
			if tt.expectedKey != "" {
				require.Contains(t, w.Body.String(), tt.expectedKey)
			}
		})
	}
}

// Sanity-check that both new actions reject malformed UUID path params with
// 400 INVALID_ID instead of trickling down to the store.
func TestSessionThreadHandler_InvalidUUIDsReturn400(t *testing.T) {
	t.Parallel()
	h, _ := newThreadHandler(t)
	endpoints := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		params  map[string]string
	}{
		{"cancel-bad-session", h.CancelThread, map[string]string{"id": "not-a-uuid", "tid": uuid.New().String()}},
		{"cancel-bad-thread", h.CancelThread, map[string]string{"id": uuid.New().String(), "tid": "not-a-uuid"}},
		{"fork-bad-session", h.ForkThread, map[string]string{"id": "not-a-uuid", "tid": uuid.New().String()}},
		{"revert-bad-thread", h.RevertThread, map[string]string{"id": uuid.New().String(), "tid": "not-a-uuid"}},
		{"file-events-bad-session", h.ListThreadFileEvents, map[string]string{"id": "not-a-uuid"}},
	}
	for _, e := range endpoints {
		t.Run(e.name, func(t *testing.T) {
			t.Parallel()
			r := threadRequest(http.MethodPost, "/", "", uuid.New(), e.params)
			w := httptest.NewRecorder()
			e.handler(w, r)
			require.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
			require.True(t, strings.Contains(w.Body.String(), "INVALID_ID"), "should surface INVALID_ID code")
		})
	}
}
