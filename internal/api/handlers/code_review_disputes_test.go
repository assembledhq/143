package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestValidCodeReviewDisputeAdjudicationUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   models.CodeReviewDisputeAdjudicationStatus
		expected bool
	}{
		{name: "upheld", status: models.CodeReviewDisputeAdjudicationUpheld, expected: true},
		{name: "rejected", status: models.CodeReviewDisputeAdjudicationRejected, expected: true},
		{name: "needs context", status: models.CodeReviewDisputeAdjudicationNeedsContext, expected: true},
		{name: "pending is list-only", status: models.CodeReviewDisputeAdjudicationPending, expected: false},
		{name: "expired is lifecycle-only", status: models.CodeReviewDisputeAdjudicationExpired, expected: false},
		{name: "unknown", status: models.CodeReviewDisputeAdjudicationStatus("unknown"), expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, validCodeReviewDisputeAdjudicationUpdate(tt.status), "only terminal admin decisions should be accepted by the PATCH contract")
		})
	}
}

// stubCodeReviewDisputeService records what the handler asked for and replays a
// scripted outcome, so the endpoints' authorization, parsing, and error
// classification can be exercised without a database.
type stubCodeReviewDisputeService struct {
	dispute      models.CodeReviewDispute
	page         models.CodeReviewDisputePage
	err          error
	fileInput    codereviewsvc.FileCodeReviewDisputeInput
	queueFilters models.CodeReviewDisputeListFilters
	update       models.CodeReviewDisputeAdjudicationUpdate
	actorUserID  uuid.UUID
	calls        int
}

func (s *stubCodeReviewDisputeService) FileInApp(_ context.Context, input codereviewsvc.FileCodeReviewDisputeInput) (models.CodeReviewDispute, error) {
	s.calls++
	s.fileInput = input
	return s.dispute, s.err
}

func (s *stubCodeReviewDisputeService) ListBySession(_ context.Context, _, _ uuid.UUID, _ *uuid.UUID, _ int) (models.CodeReviewDisputePage, error) {
	s.calls++
	return s.page, s.err
}

func (s *stubCodeReviewDisputeService) ListQueue(_ context.Context, _ uuid.UUID, filters models.CodeReviewDisputeListFilters) (models.CodeReviewDisputePage, error) {
	s.calls++
	s.queueFilters = filters
	return s.page, s.err
}

func (s *stubCodeReviewDisputeService) Escalate(_ context.Context, _, _, userID uuid.UUID, _ string) (models.CodeReviewDispute, error) {
	s.calls++
	s.actorUserID = userID
	return s.dispute, s.err
}

func (s *stubCodeReviewDisputeService) Adjudicate(_ context.Context, _, _, userID uuid.UUID, update models.CodeReviewDisputeAdjudicationUpdate) (models.CodeReviewDispute, error) {
	s.calls++
	s.actorUserID = userID
	s.update = update
	return s.dispute, s.err
}

func disputeRequest(t *testing.T, method, target, body string, orgID, userID uuid.UUID, routeID string) *http.Request {
	t.Helper()

	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "admin"})
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", routeID)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, routeCtx)
	return req.WithContext(ctx)
}

func decodeDisputeErrorCode(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body), "error body should be valid JSON")
	return body.Error.Code
}

func TestCodeReviewHandler_DisputeEndpointsRequireTheService(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	id := uuid.New().String()
	handler := &CodeReviewHandler{}
	tests := []struct {
		name   string
		invoke func(http.ResponseWriter, *http.Request)
		method string
		target string
	}{
		{name: "create", invoke: handler.CreateDispute, method: http.MethodPost, target: "/api/v1/code-reviews/" + id + "/disputes"},
		{name: "list session", invoke: handler.ListSessionDisputes, method: http.MethodGet, target: "/api/v1/code-reviews/" + id + "/disputes"},
		{name: "escalate", invoke: handler.EscalateDispute, method: http.MethodPost, target: "/api/v1/code-review-disputes/" + id + "/escalate"},
		{name: "queue", invoke: handler.ListDisputeQueue, method: http.MethodGet, target: "/api/v1/code-review-disputes"},
		{name: "adjudicate", invoke: handler.UpdateDispute, method: http.MethodPatch, target: "/api/v1/code-review-disputes/" + id},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rr := httptest.NewRecorder()
			tt.invoke(rr, disputeRequest(t, tt.method, tt.target, "{}", orgID, userID, id))

			require.Equal(t, http.StatusServiceUnavailable, rr.Code, "an unconfigured dispute service should report unavailability, not crash")
			require.Equal(t, "CODE_REVIEW_DISPUTES_UNAVAILABLE", decodeDisputeErrorCode(t, rr))
		})
	}
}

func TestCodeReviewHandler_CreateDisputeValidatesBodyBeforeCallingTheService(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New().String()
	tests := []struct {
		name         string
		body         string
		expectedCode int
		expectedErr  string
	}{
		{name: "malformed JSON", body: "{", expectedCode: http.StatusBadRequest, expectedErr: "INVALID_BODY"},
		{name: "empty body", body: `{"body":"   "}`, expectedCode: http.StatusUnprocessableEntity, expectedErr: "INVALID_DISPUTE_BODY"},
		{name: "over the rune ceiling", body: `{"body":"` + strings.Repeat("a", models.CodeReviewDisputeBodyMaxRunes+1) + `"}`, expectedCode: http.StatusUnprocessableEntity, expectedErr: "INVALID_DISPUTE_BODY"},
		{name: "unknown reason code", body: `{"body":"reconsider","contested_reason_codes":["not_a_code"]}`, expectedCode: http.StatusUnprocessableEntity, expectedErr: "INVALID_REASON_CODE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			service := &stubCodeReviewDisputeService{}
			handler := &CodeReviewHandler{disputes: service}
			rr := httptest.NewRecorder()
			handler.CreateDispute(rr, disputeRequest(t, http.MethodPost, "/api/v1/code-reviews/"+sessionID+"/disputes", tt.body, orgID, userID, sessionID))

			require.Equal(t, tt.expectedCode, rr.Code, "input validation should answer before the service is reached")
			require.Equal(t, tt.expectedErr, decodeDisputeErrorCode(t, rr))
			require.Zero(t, service.calls, "a rejected request must not spend an LLM triage")
		})
	}
}

func TestCodeReviewHandler_CreateDisputeMapsServiceErrors(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	tests := []struct {
		name         string
		err          error
		expectedCode int
		expectedErr  string
	}{
		{name: "missing review", err: pgx.ErrNoRows, expectedCode: http.StatusNotFound, expectedErr: "CODE_REVIEW_NOT_FOUND"},
		{name: "no decision yet", err: codereviewsvc.ErrCodeReviewDisputeNotReady, expectedCode: http.StatusConflict, expectedErr: "CODE_REVIEW_NOT_DISPUTABLE"},
		{name: "invalid body", err: codereviewsvc.ErrCodeReviewDisputeInvalidBody, expectedCode: http.StatusUnprocessableEntity, expectedErr: "INVALID_DISPUTE_BODY"},
		{name: "unexpected failure", err: errors.New("boom"), expectedCode: http.StatusInternalServerError, expectedErr: "CODE_REVIEW_DISPUTE_CREATE_FAILED"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			service := &stubCodeReviewDisputeService{err: tt.err}
			handler := &CodeReviewHandler{disputes: service}
			rr := httptest.NewRecorder()
			handler.CreateDispute(rr, disputeRequest(t, http.MethodPost,
				"/api/v1/code-reviews/"+sessionID.String()+"/disputes", `{"body":"this should not have been blocked"}`,
				orgID, userID, sessionID.String()))

			require.Equal(t, tt.expectedCode, rr.Code)
			require.Equal(t, tt.expectedErr, decodeDisputeErrorCode(t, rr))
		})
	}
}

func TestCodeReviewHandler_CreateDisputeForwardsFilerIdentity(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	service := &stubCodeReviewDisputeService{dispute: models.CodeReviewDispute{ID: uuid.New(), SessionID: sessionID}}
	handler := &CodeReviewHandler{disputes: service}
	rr := httptest.NewRecorder()

	handler.CreateDispute(rr, disputeRequest(t, http.MethodPost,
		"/api/v1/code-reviews/"+sessionID.String()+"/disputes",
		`{"body":"  this should not have been blocked  ","contested_reason_codes":["files_limit_exceeded"]}`,
		orgID, userID, sessionID.String()))

	require.Equal(t, http.StatusCreated, rr.Code, "body: %s", rr.Body.String())
	require.Equal(t, orgID, service.fileInput.OrgID)
	require.Equal(t, sessionID, service.fileInput.SessionID)
	require.Equal(t, &userID, service.fileInput.FiledByUserID, "the filer must be attributable for the authorization snapshot")
	require.Equal(t, models.CodeReviewDisputeSourceAppUI, service.fileInput.Source)
	require.Equal(t, "this should not have been blocked", service.fileInput.Body, "the stored body should be trimmed")
	require.Equal(t, []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonFilesLimitExceeded}, service.fileInput.ContestedReasonCodes)
}

func TestCodeReviewHandler_UpdateDisputeSeparatesCallerErrorsFromServerErrors(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	disputeID := uuid.New()
	tests := []struct {
		name         string
		err          error
		expectedCode int
		expectedErr  string
	}{
		{
			name: "concurrent edit", err: db.ErrCodeReviewDisputeVersionConflict,
			expectedCode: http.StatusConflict, expectedErr: "CODE_REVIEW_DISPUTE_VERSION_CONFLICT",
		},
		{
			name: "rejected update", err: codereviewsvc.ErrCodeReviewDisputeInvalidUpdate,
			expectedCode: http.StatusUnprocessableEntity, expectedErr: "CODE_REVIEW_DISPUTE_UPDATE_FAILED",
		},
		{
			// Reporting a database or enqueue failure as 4xx hides it from error
			// budgets and alerting.
			name: "enqueue failure", err: errors.New("enqueue code review dispute reply: connection refused"),
			expectedCode: http.StatusInternalServerError, expectedErr: "CODE_REVIEW_DISPUTE_UPDATE_FAILED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			service := &stubCodeReviewDisputeService{err: tt.err}
			handler := &CodeReviewHandler{disputes: service}
			rr := httptest.NewRecorder()
			handler.UpdateDispute(rr, disputeRequest(t, http.MethodPatch,
				"/api/v1/code-review-disputes/"+disputeID.String(),
				`{"expected_version":3,"adjudication_status":"upheld"}`, orgID, userID, disputeID.String()))

			require.Equal(t, tt.expectedCode, rr.Code, "body: %s", rr.Body.String())
			require.Equal(t, tt.expectedErr, decodeDisputeErrorCode(t, rr))
		})
	}
}

func TestCodeReviewHandler_UpdateDisputeParsesTheTrustOverrideTriState(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	disputeID := uuid.New()
	truth := true
	tests := []struct {
		name            string
		body            string
		expectedCode    int
		expectedPresent bool
		expectedValue   *bool
	}{
		{name: "promotion", body: `{"expected_version":2,"trust_override":true}`, expectedCode: http.StatusOK, expectedPresent: true, expectedValue: &truth},
		{name: "clearing the override", body: `{"expected_version":2,"trust_override":null}`, expectedCode: http.StatusOK, expectedPresent: true},
		{name: "omitted override", body: `{"expected_version":2,"adjudication_status":"rejected"}`, expectedCode: http.StatusOK},
		{name: "non-boolean override", body: `{"expected_version":2,"trust_override":"yes"}`, expectedCode: http.StatusUnprocessableEntity},
		{name: "no version", body: `{"trust_override":true}`, expectedCode: http.StatusUnprocessableEntity},
		{name: "nothing to update", body: `{"expected_version":2}`, expectedCode: http.StatusUnprocessableEntity},
		{name: "pending is not an admin verdict", body: `{"expected_version":2,"adjudication_status":"pending"}`, expectedCode: http.StatusUnprocessableEntity},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			service := &stubCodeReviewDisputeService{dispute: models.CodeReviewDispute{ID: disputeID}}
			handler := &CodeReviewHandler{disputes: service}
			rr := httptest.NewRecorder()
			handler.UpdateDispute(rr, disputeRequest(t, http.MethodPatch,
				"/api/v1/code-review-disputes/"+disputeID.String(), tt.body, orgID, userID, disputeID.String()))

			require.Equal(t, tt.expectedCode, rr.Code, "body: %s", rr.Body.String())
			if tt.expectedCode != http.StatusOK {
				require.Zero(t, service.calls, "a rejected update must not reach the service")
				return
			}
			require.Equal(t, userID, service.actorUserID, "the adjudicator must be attributable")
			require.Equal(t, tt.expectedPresent, service.update.TrustOverridePresent,
				"an omitted override and an explicit null are different instructions")
			require.Equal(t, tt.expectedValue, service.update.TrustOverride)
		})
	}
}

func TestCodeReviewHandler_ListDisputeQueueRejectsUnknownFilters(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	repositoryID := uuid.New()
	tests := []struct {
		name         string
		query        string
		expectedCode int
		expectedErr  string
	}{
		{name: "bad adjudication status", query: "?adjudication_status=maybe", expectedCode: http.StatusBadRequest, expectedErr: "INVALID_ADJUDICATION_STATUS"},
		{name: "bad repository", query: "?repository_id=not-a-uuid", expectedCode: http.StatusBadRequest, expectedErr: "INVALID_REPOSITORY_ID"},
		{name: "bad direction", query: "?direction=sideways", expectedCode: http.StatusBadRequest, expectedErr: "INVALID_DIRECTION"},
		{name: "bad cursor", query: "?cursor=not-a-uuid", expectedCode: http.StatusBadRequest, expectedErr: "INVALID_CURSOR"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			service := &stubCodeReviewDisputeService{}
			handler := &CodeReviewHandler{disputes: service}
			rr := httptest.NewRecorder()
			handler.ListDisputeQueue(rr, disputeRequest(t, http.MethodGet, "/api/v1/code-review-disputes"+tt.query, "", orgID, userID, ""))

			require.Equal(t, tt.expectedCode, rr.Code, "body: %s", rr.Body.String())
			require.Equal(t, tt.expectedErr, decodeDisputeErrorCode(t, rr))
			require.Zero(t, service.calls, "an unparseable filter must not reach the store")
		})
	}

	t.Run("accepted filters reach the service", func(t *testing.T) {
		t.Parallel()

		cursor := uuid.New()
		service := &stubCodeReviewDisputeService{}
		handler := &CodeReviewHandler{disputes: service}
		rr := httptest.NewRecorder()
		handler.ListDisputeQueue(rr, disputeRequest(t, http.MethodGet,
			"/api/v1/code-review-disputes?adjudication_status=pending&repository_id="+repositoryID.String()+
				"&direction=should_have_approved&cursor="+cursor.String(), "", orgID, userID, ""))

		require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
		require.Equal(t, models.CodeReviewDisputeAdjudicationPending, *service.queueFilters.AdjudicationStatus)
		require.Equal(t, repositoryID, *service.queueFilters.RepositoryID)
		require.Equal(t, models.CodeReviewDisputeDirectionShouldHaveApproved, *service.queueFilters.Direction)
		require.Equal(t, cursor, *service.queueFilters.Cursor)
	})
}

func TestCodeReviewHandler_EscalateDisputeReportsIneligibilityAsConflict(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	disputeID := uuid.New()
	tests := []struct {
		name         string
		err          error
		expectedCode int
		expectedErr  string
	}{
		{name: "not escalatable", err: codereviewsvc.ErrCodeReviewDisputeNotEscalatable, expectedCode: http.StatusConflict, expectedErr: "DISPUTE_NOT_ESCALATABLE"},
		{name: "already adjudicated", err: pgx.ErrNoRows, expectedCode: http.StatusConflict, expectedErr: "DISPUTE_NOT_ESCALATABLE"},
		{name: "unexpected failure", err: errors.New("boom"), expectedCode: http.StatusInternalServerError, expectedErr: "CODE_REVIEW_DISPUTE_ESCALATE_FAILED"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			service := &stubCodeReviewDisputeService{err: tt.err}
			handler := &CodeReviewHandler{disputes: service}
			rr := httptest.NewRecorder()
			handler.EscalateDispute(rr, disputeRequest(t, http.MethodPost,
				"/api/v1/code-review-disputes/"+disputeID.String()+"/escalate", `{"note":"third report this week"}`,
				orgID, userID, disputeID.String()))

			require.Equal(t, tt.expectedCode, rr.Code, "body: %s", rr.Body.String())
			require.Equal(t, tt.expectedErr, decodeDisputeErrorCode(t, rr))
		})
	}

	t.Run("an empty body is a valid escalation", func(t *testing.T) {
		t.Parallel()

		service := &stubCodeReviewDisputeService{dispute: models.CodeReviewDispute{ID: disputeID}}
		handler := &CodeReviewHandler{disputes: service}
		rr := httptest.NewRecorder()
		handler.EscalateDispute(rr, disputeRequest(t, http.MethodPost,
			"/api/v1/code-review-disputes/"+disputeID.String()+"/escalate", "", orgID, userID, disputeID.String()))

		require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
		require.Equal(t, userID, service.actorUserID, "escalation demand is counted per user, so the actor must be recorded")
	})
}
