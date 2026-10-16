package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/github/identity"
)

type teamReviewReceiptStore struct {
	state      models.TeamReviewRequestState
	confirmErr error
}

func (s *teamReviewReceiptStore) Begin(_ context.Context, orgID, publicationID uuid.UUID, team string) (models.TeamReviewRequestReceipt, bool, error) {
	claimed := s.state == "" || s.state == models.TeamReviewRequestRetryable
	if claimed {
		s.state = models.TeamReviewRequestSending
	}
	return models.TeamReviewRequestReceipt{OrgID: orgID, PublicationID: publicationID, Team: team, State: s.state}, claimed, nil
}

func (s *teamReviewReceiptStore) MarkRequested(ctx context.Context, _ uuid.UUID, _ uuid.UUID, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.confirmErr != nil {
		return s.confirmErr
	}
	s.state = models.TeamReviewRequestRequested
	return nil
}

func (s *teamReviewReceiptStore) MarkRetryable(ctx context.Context, _ uuid.UUID, _ uuid.UUID, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.state = models.TeamReviewRequestRetryable
	return nil
}

func TestPublicationTeamReviewReceiptsAndUncertainWrites(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                        string
		initial                     models.TeamReviewRequestState
		pendingBefore, pendingAfter bool
		postStatus                  int
		readStatus                  int
		postErr, confirmErr         error
		wantErr, uncertain          bool
		wantState                   models.TeamReviewRequestState
		wantPosts, wantReads        int
	}{
		{name: "request missing team", postStatus: 201, wantState: models.TeamReviewRequestRequested, wantPosts: 1, wantReads: 1},
		{name: "adopt currently pending request", pendingBefore: true, wantState: models.TeamReviewRequestRequested, wantReads: 1},
		{name: "confirmed receipt survives team removal", initial: models.TeamReviewRequestRequested, wantState: models.TeamReviewRequestRequested},
		{name: "reconcile uncertain pending request", initial: models.TeamReviewRequestSending, pendingBefore: true, wantState: models.TeamReviewRequestRequested, wantReads: 1},
		{name: "never resend uncertain absent request", initial: models.TeamReviewRequestSending, wantErr: true, uncertain: true, wantState: models.TeamReviewRequestSending, wantReads: 1},
		{name: "explicit rejection remains retryable", postStatus: 422, wantErr: true, wantState: models.TeamReviewRequestRetryable, wantPosts: 1, wantReads: 1},
		{name: "server failure with pending evidence", postStatus: 500, pendingAfter: true, wantState: models.TeamReviewRequestRequested, wantPosts: 1, wantReads: 2},
		{name: "server failure without delivery proof", postStatus: 500, wantErr: true, uncertain: true, wantState: models.TeamReviewRequestSending, wantPosts: 1, wantReads: 2},
		{name: "lost response with pending evidence", postErr: errors.New("lost response"), pendingAfter: true, wantState: models.TeamReviewRequestRequested, wantPosts: 1, wantReads: 2},
		{name: "lost response without evidence", postErr: errors.New("lost response"), wantErr: true, uncertain: true, wantState: models.TeamReviewRequestSending, wantPosts: 1, wantReads: 2},
		{name: "receipt persistence failure retains uncertainty", postStatus: 201, confirmErr: errors.New("database write failed"), wantErr: true, wantState: models.TeamReviewRequestSending, wantPosts: 1, wantReads: 1},
		{name: "read failure before mutation permits safe retry", readStatus: 503, wantErr: true, wantState: models.TeamReviewRequestRetryable, wantReads: 1},
		{name: "read failure cannot reset an uncertain mutation", initial: models.TeamReviewRequestSending, readStatus: 503, wantErr: true, wantState: models.TeamReviewRequestSending, wantReads: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := &teamReviewReceiptStore{state: tt.initial, confirmErr: tt.confirmErr}
			reads, posts := 0, 0
			service := &PRService{baseURL: "https://api.github.test", logger: zerolog.Nop(), teamReviewRequests: store, httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				require.Equal(t, "/repos/acme/repo/pulls/42/requested_reviewers", req.URL.Path, "review requests must target the finalized pull request")
				require.Equal(t, "token publisher-token", req.Header.Get("Authorization"), "request and reconciliation must share original publisher identity")
				status, body := http.StatusOK, `{"teams":[]}`
				if req.Method == http.MethodGet {
					reads++
					if tt.readStatus != 0 {
						status = tt.readStatus
					}
					if (posts == 0 && tt.pendingBefore) || (posts > 0 && tt.pendingAfter) {
						body = `{"teams":[{"slug":"team"}]}`
					}
				} else {
					posts++
					var payload map[string]any
					require.NoError(t, json.NewDecoder(req.Body).Decode(&payload), "review request should encode valid JSON")
					require.Equal(t, map[string]any{"team_reviewers": []any{"team"}}, payload, "GitHub receives only requested team slugs")
					if tt.postErr != nil {
						return nil, tt.postErr
					}
					status = tt.postStatus
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})}}
			err := service.requestPublicationTeamReview(context.Background(), "publisher-token", "/repos/acme/repo/pulls/42/requested_reviewers", uuid.New(), uuid.New(), "acme/team")
			if tt.wantErr {
				require.Error(t, err, "failed or ambiguous mutation must not report completion")
			} else {
				require.NoError(t, err, "confirmed review requests should complete")
			}
			require.Equal(t, tt.uncertain, errors.Is(err, ErrTeamReviewRequestUncertain), "uncertain outcomes must remain distinguishable from safe retries")
			require.Equal(t, tt.wantState, store.state, "receipt must retain the exact delivery knowledge")
			require.Equal(t, tt.wantPosts, posts, "replays must never blindly duplicate a prior mutation")
			require.Equal(t, tt.wantReads, reads, "confirmed receipts should avoid querying the ephemeral pending list")
		})
	}
}

func TestPublicationTeamReviewCancellationBeforePostReleasesReservation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &teamReviewReceiptStore{}
	requests := 0
	service := &PRService{baseURL: "https://api.github.test", logger: zerolog.Nop(), teamReviewRequests: store, httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		require.Equal(t, http.MethodGet, req.Method, "cancellation during preflight must never reach the mutation")
		cancel()
		return nil, context.Canceled
	})}}
	err := service.requestPublicationTeamReview(ctx, "publisher-token", "/repos/acme/repo/pulls/42/requested_reviewers", uuid.New(), uuid.New(), "acme/team")
	require.ErrorIs(t, err, context.Canceled, "caller cancellation must remain visible")
	require.Equal(t, models.TeamReviewRequestRetryable, store.state, "known pre-POST cancellation must release the reservation using an independent bounded context")
	require.Equal(t, 1, requests, "canceled preflight must not POST or attempt uncertain-write reconciliation")
}

type perTeamReviewReceiptStore map[string]*teamReviewReceiptStore

func (s perTeamReviewReceiptStore) Begin(ctx context.Context, orgID, publicationID uuid.UUID, team string) (models.TeamReviewRequestReceipt, bool, error) {
	if s[team] == nil {
		s[team] = &teamReviewReceiptStore{}
	}
	return s[team].Begin(ctx, orgID, publicationID, team)
}
func (s perTeamReviewReceiptStore) MarkRequested(ctx context.Context, orgID, publicationID uuid.UUID, team string) error {
	return s[team].MarkRequested(ctx, orgID, publicationID, team)
}
func (s perTeamReviewReceiptStore) MarkRetryable(ctx context.Context, orgID, publicationID uuid.UUID, team string) error {
	return s[team].MarkRetryable(ctx, orgID, publicationID, team)
}

func TestPublicationTeamReviewsContinueAfterOneTeamFails(t *testing.T) {
	t.Parallel()
	mock := newMockPool(t)
	orgID, sessionID, changesetID, repoID, publicationID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := time.Now()
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id =").WithArgs(orgID, "acme/repo").WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(repoID, orgID, uuid.New(), int64(1001), "acme/repo", "main", false, nil, nil, "https://github.com/acme/repo.git", int64(123), "active", nil, nil, []byte(`{}`), now, now))
	var posted []string
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, status := `{"teams":[]}`, http.StatusOK
		if req.Method == http.MethodPost {
			var payload struct {
				Teams []string `json:"team_reviewers"`
			}
			require.NoError(t, json.NewDecoder(req.Body).Decode(&payload), "review mutation should encode requested teams")
			posted = append(posted, payload.Teams...)
			if payload.Teams[0] == "missing" {
				status = http.StatusUnprocessableEntity
			}
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	resolver := identity.NewResolver(reconciliationTokenSource{}, zerolog.Nop())
	resolver.SetHTTPClient(client)
	receipts := perTeamReviewReceiptStore{}
	service := &PRService{repos: db.NewRepositoryStore(mock), cachedResolver: resolver, baseURL: "https://api.github.test", httpClient: client, logger: zerolog.Nop(), teamReviewRequests: receipts}
	prNumber := 42
	run := &models.Session{ID: sessionID, OrgID: orgID}
	pr := &models.PullRequest{OrgID: orgID, SessionID: &sessionID, ChangesetID: &changesetID, GitHubRepo: "acme/repo", GitHubPRNumber: prNumber, AuthoredBy: models.GitIdentitySourceApp}
	publication := &models.SessionPublication{ID: publicationID, OrgID: orgID, SessionID: sessionID, ChangesetID: changesetID, RepositoryID: repoID, GitHubPRNumber: &prNumber, State: models.SessionPublicationStateCompleted, ReviewGateState: models.SessionPublicationReviewGatePassed}
	err := service.RequestPublicationTeamReviews(context.Background(), run, pr, publication, []string{"missing", "valid"})
	require.Error(t, err, "one rejected team must still report incomplete follow-through")
	require.Equal(t, []string{"missing", "valid"}, posted, "an invalid first team must not prevent the independent second request")
	require.Equal(t, models.TeamReviewRequestRetryable, receipts["acme/missing"].state, "definitive rejection must remain distinct from an uncertain POST")
	require.Equal(t, models.TeamReviewRequestRequested, receipts["acme/valid"].state, "successful later team must retain its confirmed receipt")
	require.NoError(t, mock.ExpectationsWereMet(), "all teams must use one scoped publisher lookup")
}

func TestPublicationTeamReviewsUseOriginalPublisher(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		author        models.GitIdentitySource
		expectedToken string
	}{
		{name: "original app publication", author: models.GitIdentitySourceApp, expectedToken: "installation-123"},
		{name: "original user publication", author: models.GitIdentitySourceUser, expectedToken: "original-user-token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newMockPool(t)
			orgID, sessionID, changesetID, repoID, publicationID, userID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			now := time.Now()
			mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id =").WithArgs(orgID, "acme/repo").WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(repoID, orgID, uuid.New(), int64(1001), "acme/repo", "main", false, nil, nil, "https://github.com/acme/repo.git", int64(123), "active", nil, nil, []byte(`{}`), now, now))
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				require.Equal(t, "token "+tt.expectedToken, req.Header.Get("Authorization"), "review follow-through must retain the original publication identity")
				body := `{"teams":[]}`
				if req.URL.Path != "/repos/acme/repo" && req.URL.Path != "/repos/acme/repo/pulls/42/requested_reviewers" {
					return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			resolver := identity.NewResolver(reconciliationTokenSource{}, zerolog.Nop())
			resolver.SetHTTPClient(client)
			resolver.SetAppUserAuth(&stubPRAppUserAuth{getValidCredentialFunc: func(_ context.Context, gotOrgID, gotUserID uuid.UUID) (*models.GitHubAppUserConfig, error) {
				if tt.author == models.GitIdentitySourceApp {
					t.Fatal("original app identity must not switch to user authentication")
				}
				require.Equal(t, orgID, gotOrgID, "publisher lookup must remain tenant scoped")
				require.Equal(t, userID, gotUserID, "publisher lookup must use frozen initiator rather than mutable session user")
				return &models.GitHubAppUserConfig{AccessToken: "original-user-token"}, nil
			}})
			service := &PRService{repos: db.NewRepositoryStore(mock), cachedResolver: resolver, baseURL: "https://api.github.test", httpClient: client, logger: zerolog.Nop(), teamReviewRequests: &teamReviewReceiptStore{}}
			mutableUser := uuid.New()
			run := &models.Session{ID: sessionID, OrgID: orgID, TriggeredByUserID: &mutableUser}
			prNumber := 42
			pr := &models.PullRequest{OrgID: orgID, SessionID: &sessionID, ChangesetID: &changesetID, GitHubRepo: "acme/repo", GitHubPRNumber: prNumber, AuthoredBy: tt.author}
			publication := &models.SessionPublication{ID: publicationID, OrgID: orgID, SessionID: sessionID, ChangesetID: changesetID, RepositoryID: repoID, GitHubPRNumber: &prNumber, State: models.SessionPublicationStateCompleted, ReviewGateState: models.SessionPublicationReviewGatePassed, InitiatedByUserID: &userID}
			require.NoError(t, service.RequestPublicationTeamReviews(context.Background(), run, pr, publication, []string{"team"}), "finalized publication should request reviewers with its recorded identity")
			require.NoError(t, service.RequestPublicationTeamReviews(context.Background(), run, pr, publication, []string{"team"}), "confirmed receipt should replay without more identity or repository reads")
			require.NoError(t, mock.ExpectationsWereMet(), "repository identity lookup must remain tenant scoped and occur only before the first request")
		})
	}
}
