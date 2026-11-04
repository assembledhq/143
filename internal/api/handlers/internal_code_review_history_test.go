package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type priorHistoryStub struct {
	authErr, targetErr, saveErr error
	index                       []db.CodeReviewHistoryRound
	report                      db.CodeReviewHistoryReport
	findings                    []db.CodeReviewHistoryFinding
	threads                     []db.CodeReviewHistoryThread
	results                     []db.CodeReviewHistoryAgentResult
	messages                    []db.CodeReviewHistoryMessage
	receipts                    map[string]json.RawMessage
	metadata                    []json.RawMessage
	loads                       int
	threadFound, cursorValid    bool
	messageCursor               priorReviewMessageCursor
}

func (s *priorHistoryStub) AuthorizeSource(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) error {
	return s.authErr
}
func (s *priorHistoryStub) ListPriorRounds(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, *uuid.UUID, int) ([]db.CodeReviewHistoryRound, error) {
	return s.index, nil
}
func (s *priorHistoryStub) GetPriorRound(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) (db.CodeReviewHistoryReport, error) {
	s.loads++
	return s.report, s.targetErr
}
func (s *priorHistoryStub) ListPriorFindings(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID, time.Time, int) ([]db.CodeReviewHistoryFinding, error) {
	return s.findings, nil
}
func (s *priorHistoryStub) ListPriorThreads(context.Context, uuid.UUID, uuid.UUID, time.Time, int) ([]db.CodeReviewHistoryThread, error) {
	return s.threads, nil
}
func (s *priorHistoryStub) ListPriorAgentResults(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID, time.Time, int) ([]db.CodeReviewHistoryAgentResult, error) {
	return s.results, nil
}
func (s *priorHistoryStub) HasPriorThread(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, time.Time) (bool, error) {
	return s.threadFound, nil
}
func (s *priorHistoryStub) ValidMessageCursor(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, time.Time, int64, int) (bool, error) {
	return s.cursorValid, nil
}
func (s *priorHistoryStub) ListPriorMessages(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ uuid.UUID, _ time.Time, id int64, offset int, _ int) ([]db.CodeReviewHistoryMessage, error) {
	s.messageCursor = priorReviewMessageCursor{ID: id, Offset: offset}
	return s.messages, nil
}
func (s *priorHistoryStub) GetReceipt(_ context.Context, _ uuid.UUID, _ uuid.UUID, key string) (json.RawMessage, error) {
	if v, ok := s.receipts[key]; ok {
		return v, nil
	}
	return nil, pgx.ErrNoRows
}
func (s *priorHistoryStub) SaveReceipt(_ context.Context, _ uuid.UUID, _ uuid.UUID, key string, content, metadata json.RawMessage) (json.RawMessage, error) {
	if s.saveErr != nil {
		return nil, s.saveErr
	}
	s.receipts[key] = content
	s.metadata = append(s.metadata, metadata)
	return content, nil
}

type priorHistoryFixture struct {
	handler  *InternalCodeReviewHistoryHandler
	store    *priorHistoryStub
	session  models.Session
	threadID uuid.UUID
	secret   string
}

func newPriorHistoryFixture(t *testing.T) priorHistoryFixture {
	t.Helper()
	repoID := uuid.New()
	session := models.Session{ID: uuid.New(), OrgID: uuid.New(), RepositoryID: &repoID, Origin: models.SessionOriginCodeReview, RevisionContext: json.RawMessage(`{"request_context":{"use_review_history":true}}`)}
	store := &priorHistoryStub{receipts: map[string]json.RawMessage{}, threadFound: true, cursorValid: true, index: []db.CodeReviewHistoryRound{}, findings: []db.CodeReviewHistoryFinding{}, threads: []db.CodeReviewHistoryThread{}, results: []db.CodeReviewHistoryAgentResult{}}
	f := priorHistoryFixture{store: store, session: session, threadID: uuid.New(), secret: "synthetic-review-history-secret"}
	f.handler = NewInternalCodeReviewHistoryHandler(store, stubInternalCodeReviewSessions{session: session}, f.secret)
	return f
}

func (f priorHistoryFixture) request(t *testing.T, path string, params map[string]string, threadID *uuid.UUID) *http.Request {
	t.Helper()
	token, err := auth.GenerateSessionThreadToken(f.secret, f.session.OrgID, *f.session.RepositoryID, f.session.ID, threadID, time.Hour)
	require.NoError(t, err, "sign fixture session token")
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestPriorReviewHistoryAuthorization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		change        func(*priorHistoryFixture)
		missingThread bool
		want          int
	}{
		{"opted-in orchestrator", func(*priorHistoryFixture) {}, false, 200},
		{"no opt-in", func(f *priorHistoryFixture) { f.session.RevisionContext = json.RawMessage(`{}`) }, false, 403},
		{"malformed opt-in", func(f *priorHistoryFixture) { f.session.RevisionContext = json.RawMessage(`{`) }, false, 403},
		{"ordinary session", func(f *priorHistoryFixture) { f.session.Origin = models.SessionOriginManual }, false, 403},
		{"session-only token", func(*priorHistoryFixture) {}, true, 403},
		{"reviewer thread", func(f *priorHistoryFixture) { f.store.authErr = pgx.ErrNoRows }, false, 403},
		{"authorization unavailable", func(f *priorHistoryFixture) { f.store.authErr = errors.New("unavailable") }, false, 500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newPriorHistoryFixture(t)
			tt.change(&f)
			f.handler.sessions = stubInternalCodeReviewSessions{session: f.session}
			thread := &f.threadID
			if tt.missingThread {
				thread = nil
			}
			rr := httptest.NewRecorder()
			f.handler.List(rr, f.request(t, "/prior-rounds", nil, thread))
			require.Equal(t, tt.want, rr.Code, "only a bound opted-in review orchestrator should reach history")
			require.Equal(t, 0, f.store.loads, "authorization and index discovery must not retrieve report bodies")
		})
	}
}

func TestPriorReviewHistoryTokenScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                string
		wrongOrg, wrongRepo bool
	}{{"other organization", true, false}, {"other repository", false, true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newPriorHistoryFixture(t)
			req := f.request(t, "/prior-rounds", nil, &f.threadID)
			stored := f.session
			if tt.wrongOrg {
				stored.OrgID = uuid.New()
			}
			if tt.wrongRepo {
				id := uuid.New()
				stored.RepositoryID = &id
			}
			f.handler.sessions = stubInternalCodeReviewSessions{session: stored}
			rr := httptest.NewRecorder()
			f.handler.List(rr, req)
			require.Equal(t, http.StatusForbidden, rr.Code, "session and token scope must agree before retrieval")
		})
	}
}

func TestPriorReviewHistoryIndexPagination(t *testing.T) {
	t.Parallel()
	f := newPriorHistoryFixture(t)
	first, second := uuid.New(), uuid.New()
	f.store.index = []db.CodeReviewHistoryRound{{SessionID: first}, {SessionID: second}}
	rr := httptest.NewRecorder()
	f.handler.List(rr, f.request(t, "/prior-rounds?limit=1", nil, &f.threadID))
	var response models.ListResponse[db.CodeReviewHistoryRound]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "index response should be valid JSON")
	require.Equal(t, models.ListResponse[db.CodeReviewHistoryRound]{Data: []db.CodeReviewHistoryRound{{SessionID: first}}, Meta: models.PaginationMeta{NextCursor: first.String()}}, response, "lookahead should expose a stable cursor without eagerly providing findings")
}

func TestPriorReviewHistoryReportReceipt(t *testing.T) {
	t.Parallel()
	f := newPriorHistoryFixture(t)
	sourceID := uuid.New()
	f.store.report = db.CodeReviewHistoryReport{CodeReviewHistoryRound: db.CodeReviewHistoryRound{SessionID: sourceID}, FinalReviewBody: "Original report", FinalReviewBodyRunes: 15}
	path := "/prior-rounds/" + sourceID.String()
	params := map[string]string{"session_id": sourceID.String()}
	first := httptest.NewRecorder()
	f.handler.Get(first, f.request(t, path, params, &f.threadID))
	require.Equal(t, http.StatusOK, first.Code, "the first report read should succeed")
	f.store.report.FinalReviewBody = "Changed after first retrieval"
	second := httptest.NewRecorder()
	f.handler.Get(second, f.request(t, path, params, &f.threadID))
	require.Equal(t, first.Body.String(), second.Body.String(), "a repeated read must return the exact recorded evidence")
	require.Equal(t, 1, f.store.loads, "receipt replay must not reread mutated history")
	require.Len(t, f.store.metadata, 1, "one successful read should have one immutable receipt")
	var metadata map[string]string
	require.NoError(t, json.Unmarshal(f.store.metadata[0], &metadata), "receipt metadata should be valid")
	require.Equal(t, map[string]string{"review_history_thread_id": f.threadID.String(), "source_session_id": sourceID.String(), "kind": "prior_round"}, metadata, "receipts must identify the consuming thread and prior round")
}

func TestPriorReviewHistoryFailurePaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, path string
		change     func(*priorHistoryFixture)
		want       int
		messages   bool
	}{
		{"invalid limit", "?limit=0", func(*priorHistoryFixture) {}, 400, false},
		{"invalid list cursor", "?cursor=bad", func(*priorHistoryFixture) {}, 400, false},
		{"cross PR or current/future target", "", func(f *priorHistoryFixture) { f.store.targetErr = pgx.ErrNoRows }, 404, false},
		{"unrecordable evidence", "", func(f *priorHistoryFixture) { f.store.saveErr = errors.New("unavailable") }, 500, false},
		{"foreign thread", "", func(f *priorHistoryFixture) { f.store.threadFound = false }, 404, true},
		{"invalid message cursor", "?cursor=bad", func(*priorHistoryFixture) {}, 400, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newPriorHistoryFixture(t)
			tt.change(&f)
			params := map[string]string{"session_id": uuid.NewString(), "thread_id": uuid.NewString()}
			rr := httptest.NewRecorder()
			req := f.request(t, "/history"+tt.path, params, &f.threadID)
			if tt.messages {
				f.handler.Messages(rr, req)
			} else if strings.Contains(tt.name, "limit") || strings.Contains(tt.name, "list cursor") {
				f.handler.List(rr, req)
			} else {
				f.handler.Get(rr, req)
			}
			require.Equal(t, tt.want, rr.Code, "unavailable or out-of-scope evidence must fail without being presented as complete")
			require.Empty(t, f.store.receipts, "failed reads must not count as successful reconciliation evidence")
		})
	}
}

func TestPriorReviewHistoryMessagesChunkPagination(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		offset, total int
		content       string
		wantNext      bool
	}{
		{"first part", 0, 4010, strings.Repeat("é", 4000), true},
		{"remaining part", 4000, 4010, strings.Repeat("é", 10), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newPriorHistoryFixture(t)
			sourceID, threadID := uuid.New(), uuid.New()
			f.store.messages = []db.CodeReviewHistoryMessage{{ID: 7, Role: models.MessageRoleAssistant, Content: tt.content, ContentRunes: tt.total}}
			path := "/history"
			if tt.offset > 0 {
				raw, err := json.Marshal(priorReviewMessageCursor{ID: 7, Offset: tt.offset})
				require.NoError(t, err, "encode test cursor")
				path += "?cursor=" + base64.RawURLEncoding.EncodeToString(raw)
			}
			rr := httptest.NewRecorder()
			f.handler.Messages(rr, f.request(t, path, map[string]string{"session_id": sourceID.String(), "thread_id": threadID.String()}, &f.threadID))
			require.Equal(t, http.StatusOK, rr.Code, "message excerpts should be available")
			var response models.ListResponse[priorReviewMessageChunk]
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "message response should be valid JSON")
			require.Equal(t, []priorReviewMessageChunk{{CodeReviewHistoryMessage: f.store.messages[0], Offset: tt.offset, Truncated: tt.wantNext}}, response.Data, "chunk offsets and total length should disclose omitted content")
			if tt.wantNext {
				raw, err := base64.RawURLEncoding.DecodeString(response.Meta.NextCursor)
				require.NoError(t, err, "continuation should be an opaque valid cursor")
				var next priorReviewMessageCursor
				require.NoError(t, json.Unmarshal(raw, &next), "decode continuation")
				require.Equal(t, priorReviewMessageCursor{ID: 7, Offset: 4000}, next, "continuation should resume inside the same long message")
			} else {
				require.Empty(t, response.Meta.NextCursor, "the final chunk should terminate pagination")
			}
		})
	}
}

func TestPriorReviewHistoryReceiptCanonicalParameters(t *testing.T) {
	t.Parallel()
	cursor := base64.RawURLEncoding.EncodeToString([]byte(`{"id":7,"offset":4000}`))
	equivalentCursor := base64.RawURLEncoding.EncodeToString([]byte(`{ "offset":4000,"ignored":"value", "id":7 }`))
	tests := []struct {
		name     string
		messages bool
		queries  []string
	}{
		{
			name:    "report ignores all query parameters",
			queries: []string{"", "?ignored=one", "?ignored=two&limit=bad&cursor=bad"},
		},
		{
			name:     "omitted and explicit defaults share a message receipt",
			messages: true,
			queries:  []string{"", "?limit=10", "?cursor=&limit=010&ignored=one", "?ignored=two&limit=10&cursor="},
		},
		{
			name:     "clamped limits share a message receipt",
			messages: true,
			queries:  []string{"?limit=20", "?limit=21&ignored=one", "?ignored=two&limit=1000"},
		},
		{
			name:     "decoded cursors and query order share a message receipt",
			messages: true,
			queries:  []string{"?cursor=" + cursor + "&limit=10", "?limit=10&cursor=" + cursor, "?cursor=" + equivalentCursor},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newPriorHistoryFixture(t)
			sourceID, threadID := uuid.New(), uuid.New()
			f.store.report = db.CodeReviewHistoryReport{CodeReviewHistoryRound: db.CodeReviewHistoryRound{SessionID: sourceID}, FinalReviewBody: "Original report", FinalReviewBodyRunes: 15}
			f.store.messages = []db.CodeReviewHistoryMessage{{ID: 7, Role: models.MessageRoleAssistant, Content: "Original excerpt", ContentRunes: 4016}}
			params := map[string]string{"session_id": sourceID.String(), "thread_id": threadID.String()}
			var firstBody string
			for i, query := range tt.queries {
				rr := httptest.NewRecorder()
				req := f.request(t, "/history"+query, params, &f.threadID)
				if tt.messages {
					f.handler.Messages(rr, req)
				} else {
					f.handler.Get(rr, req)
				}
				require.Equal(t, http.StatusOK, rr.Code, "equivalent effective retrieval parameters should remain valid")
				if i == 0 {
					firstBody = rr.Body.String()
					f.store.report.FinalReviewBody = "Mutated report"
					f.store.messages[0].Content = "Mutated excerpt"
				} else {
					require.Equal(t, firstBody, rr.Body.String(), "equivalent requests must replay the exact first response")
				}
			}
			require.Equal(t, 1, f.store.loads, "irrelevant URL variation must never reread source evidence")
			require.Len(t, f.store.receipts, 1, "equivalent effective parameters must create only one receipt")
			require.Len(t, f.store.metadata, 1, "equivalent requests must perform only one receipt write")
		})
	}
}

func TestPriorReviewHistoryReceiptSeparatesEffectiveParameters(t *testing.T) {
	t.Parallel()
	f := newPriorHistoryFixture(t)
	sourceID, threadID := uuid.New(), uuid.New()
	params := map[string]string{"session_id": sourceID.String(), "thread_id": threadID.String()}
	for _, query := range []string{"?limit=5", "?limit=10"} {
		rr := httptest.NewRecorder()
		f.handler.Messages(rr, f.request(t, "/history"+query, params, &f.threadID))
		require.Equal(t, http.StatusOK, rr.Code, "distinct valid page sizes should each be retrievable")
	}
	require.Equal(t, 2, f.store.loads, "different effective page selections must not share a receipt")
	require.Len(t, f.store.receipts, 2, "different effective page selections should retain their own evidence")
}

func TestPriorReviewHistoryInvalidParametersBeforeReceipt(t *testing.T) {
	t.Parallel()
	negativeOffset := base64.RawURLEncoding.EncodeToString([]byte(`{"id":7,"offset":-1}`))
	zeroID := base64.RawURLEncoding.EncodeToString([]byte(`{"id":0,"offset":0}`))
	tests := []struct{ name, query string }{
		{"zero limit", "?limit=0"},
		{"negative limit", "?limit=-1"},
		{"nonnumeric limit", "?limit=invalid"},
		{"invalid cursor encoding", "?cursor=invalid"},
		{"negative offset", "?cursor=" + negativeOffset},
		{"zero message identity", "?cursor=" + zeroID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newPriorHistoryFixture(t)
			rr := httptest.NewRecorder()
			f.handler.Messages(rr, f.request(t, "/history"+tt.query, map[string]string{"session_id": uuid.NewString(), "thread_id": uuid.NewString()}, &f.threadID))
			require.Equal(t, http.StatusBadRequest, rr.Code, "invalid accepted parameters must fail before retrieval")
			require.Equal(t, 0, f.store.loads, "invalid parameters must not read historical content")
			require.Empty(t, f.store.receipts, "invalid parameters must not create evidence receipts")
			require.Empty(t, f.store.metadata, "invalid parameters must not persist receipt metadata")
		})
	}
}
