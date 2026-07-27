package prioritization

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	llmpkg "github.com/assembledhq/143/internal/llm"
	"github.com/assembledhq/143/internal/models"
)

type fakeIssueStore struct {
	getByIDFn func(ctx context.Context, orgID, issueID uuid.UUID) (models.Issue, error)
}

func (f *fakeIssueStore) GetByID(ctx context.Context, orgID, issueID uuid.UUID) (models.Issue, error) {
	if f.getByIDFn != nil {
		return f.getByIDFn(ctx, orgID, issueID)
	}
	return models.Issue{}, nil
}

type fakePriorityStore struct {
	upsertFn func(ctx context.Context, score *models.PriorityScore) error
	last     *models.PriorityScore
}

func (f *fakePriorityStore) Upsert(ctx context.Context, score *models.PriorityScore) error {
	f.last = score
	if f.upsertFn != nil {
		return f.upsertFn(ctx, score)
	}
	return nil
}

type fakeComplexityStore struct {
	upsertFn func(ctx context.Context, est *models.ComplexityEstimate) error
	last     *models.ComplexityEstimate
}

func (f *fakeComplexityStore) Upsert(ctx context.Context, est *models.ComplexityEstimate) error {
	f.last = est
	if f.upsertFn != nil {
		return f.upsertFn(ctx, est)
	}
	return nil
}

type fakeSessionStore struct {
	countRunningByOrgFn func(ctx context.Context, orgID uuid.UUID) (int, error)
	createFn            func(ctx context.Context, run *models.Session) error
	createdRuns         []*models.Session
}

func (f *fakeSessionStore) CountRunningByOrg(ctx context.Context, orgID uuid.UUID) (int, error) {
	if f.countRunningByOrgFn != nil {
		return f.countRunningByOrgFn(ctx, orgID)
	}
	return 0, nil
}

func (f *fakeSessionStore) Create(ctx context.Context, run *models.Session) error {
	f.createdRuns = append(f.createdRuns, run)
	if f.createFn != nil {
		return f.createFn(ctx, run)
	}
	return nil
}

type fakeOrgStore struct {
	getByIDFn func(ctx context.Context, id uuid.UUID) (models.Organization, error)
}

func (f *fakeOrgStore) GetByID(ctx context.Context, id uuid.UUID) (models.Organization, error) {
	if f.getByIDFn != nil {
		return f.getByIDFn(ctx, id)
	}
	return models.Organization{}, nil
}

type fakeJobStore struct {
	enqueueFn func(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
	enqueueN  int
}

func (f *fakeJobStore) Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	f.enqueueN++
	if f.enqueueFn != nil {
		return f.enqueueFn(ctx, orgID, queue, jobType, payload, priority, dedupeKey)
	}
	return uuid.New(), nil
}

type fakeLLMClient struct {
	completeFn func(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

func (f *fakeLLMClient) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	if f.completeFn != nil {
		return f.completeFn(ctx, systemPrompt, userPrompt)
	}
	return "", nil
}

func TestComputeScore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		issue             models.Issue
		orgSettings       json.RawMessage
		llmResponse       string
		llmErr            error
		expectedAlignment float64
	}{
		{
			name: "uses default weights for open issue",
			issue: models.Issue{
				Severity:              "high",
				OccurrenceCount:       64,
				AffectedCustomerCount: 32,
				LastSeenAt:            time.Now(),
				Status:                "open",
			},
			orgSettings:       json.RawMessage(`{"min_priority_threshold":30}`),
			expectedAlignment: 0,
		},
		{
			name: "uses product context for llm alignment",
			issue: models.Issue{
				Severity:              "critical",
				OccurrenceCount:       100,
				AffectedCustomerCount: 60,
				LastSeenAt:            time.Now(),
				Status:                "open",
				Title:                 "checkout error",
			},
			orgSettings:       json.RawMessage(`{"product_context":{"direction":"self-serve onboarding"}}`),
			llmResponse:       `{"alignment":-0.8,"reasoning":"off direction"}`,
			expectedAlignment: -0.8,
		},
		{
			name: "falls back to zero alignment when llm errors",
			issue: models.Issue{
				Severity:              "high",
				OccurrenceCount:       64,
				AffectedCustomerCount: 32,
				LastSeenAt:            time.Now(),
				Status:                "triaged",
				Title:                 "latency spike",
			},
			orgSettings:       json.RawMessage(`{"product_context":{"direction":"api reliability"}}`),
			llmErr:            errors.New("provider unavailable"),
			expectedAlignment: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()
			issueID := uuid.New()

			issues := &fakeIssueStore{
				getByIDFn: func(ctx context.Context, gotOrgID, gotIssueID uuid.UUID) (models.Issue, error) {
					require.Equal(t, orgID, gotOrgID, "ComputeScore should request issue with the same org id")
					require.Equal(t, issueID, gotIssueID, "ComputeScore should request issue with the same issue id")
					issue := tt.issue
					issue.ID = issueID
					issue.OrgID = orgID
					return issue, nil
				},
			}
			priorities := &fakePriorityStore{}
			orgs := &fakeOrgStore{
				getByIDFn: func(ctx context.Context, id uuid.UUID) (models.Organization, error) {
					require.Equal(t, orgID, id, "ComputeScore should request organization settings for the same org id")
					return models.Organization{ID: id, Settings: tt.orgSettings}, nil
				},
			}

			var llm llmpkg.Client
			if tt.llmResponse != "" || tt.llmErr != nil {
				llm = &fakeLLMClient{
					completeFn: func(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
						if tt.llmErr != nil {
							return "", tt.llmErr
						}
						return tt.llmResponse, nil
					},
				}
			}

			svc := NewService(issues, priorities, &fakeComplexityStore{}, orgs, llm, zerolog.Nop())

			result, err := svc.ComputeScore(context.Background(), orgID, issueID)
			require.NoError(t, err, "ComputeScore should succeed for valid issue and org data")
			require.NotNil(t, result, "ComputeScore should return a populated priority score")
			require.NotNil(t, priorities.last, "ComputeScore should upsert a priority score")
			require.InDelta(t, tt.expectedAlignment, result.DirectionAlignment, 0.0001, "ComputeScore should preserve expected direction alignment value")
			require.Equal(t, orgID, result.OrgID, "ComputeScore should set the org id on the resulting score")
			require.Equal(t, issueID, result.IssueID, "ComputeScore should set the issue id on the resulting score")
		})
	}
}

func TestEstimateComplexity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		llmResponse     string
		llmErr          error
		passIssue       bool
		expectedTier    int
		expectedLabel   string
		expectedModel   *string
		expectFetchCall bool
	}{
		{
			name:            "uses heuristic when llm is nil",
			passIssue:       true,
			expectedTier:    2,
			expectedLabel:   "simple",
			expectedModel:   nil,
			expectFetchCall: false,
		},
		{
			name:            "uses llm response and clamps tier into range",
			llmResponse:     `{"tier":8,"label":"massive","confidence":0.91,"reasoning":"many files"}`,
			passIssue:       true,
			expectedTier:    5,
			expectedLabel:   "massive",
			expectedModel:   ptr("llm"),
			expectFetchCall: false,
		},
		{
			name:            "fetches issue when nil and falls back to heuristic on llm error",
			llmErr:          errors.New("temporary llm failure"),
			passIssue:       false,
			expectedTier:    3,
			expectedLabel:   "moderate",
			expectedModel:   nil,
			expectFetchCall: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()
			issueID := uuid.New()
			fetchCalled := false
			fetchedIssue := models.Issue{ID: issueID, OrgID: orgID, Severity: "critical", Title: "panic in worker", LastSeenAt: time.Now()}
			providedIssue := models.Issue{ID: issueID, OrgID: orgID, Severity: "high", Title: "json mismatch", LastSeenAt: time.Now()}

			issues := &fakeIssueStore{
				getByIDFn: func(ctx context.Context, gotOrgID, gotIssueID uuid.UUID) (models.Issue, error) {
					fetchCalled = true
					require.Equal(t, orgID, gotOrgID, "EstimateComplexity should fetch using the requested org id")
					require.Equal(t, issueID, gotIssueID, "EstimateComplexity should fetch using the requested issue id")
					return fetchedIssue, nil
				},
			}

			complexity := &fakeComplexityStore{}

			var llm llmpkg.Client
			if tt.llmResponse != "" || tt.llmErr != nil {
				llm = &fakeLLMClient{completeFn: func(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
					if tt.llmErr != nil {
						return "", tt.llmErr
					}
					return tt.llmResponse, nil
				}}
			}

			svc := NewService(issues, &fakePriorityStore{}, complexity, &fakeOrgStore{}, llm, zerolog.Nop())

			var inputIssue *models.Issue
			if tt.passIssue {
				inputIssue = &providedIssue
			}

			result, err := svc.EstimateComplexity(context.Background(), orgID, issueID, inputIssue)
			require.NoError(t, err, "EstimateComplexity should succeed for valid inputs")
			require.Equal(t, tt.expectedTier, result.Tier, "EstimateComplexity should return the expected complexity tier")
			require.Equal(t, tt.expectedLabel, result.Label, "EstimateComplexity should return the expected complexity label")
			require.Equal(t, tt.expectedModel, result.ModelUsed, "EstimateComplexity should set model_used when the llm path is used")
			require.Equal(t, tt.expectFetchCall, fetchCalled, "EstimateComplexity should fetch issue data only when issue input is nil")
			require.NotNil(t, complexity.last, "EstimateComplexity should upsert the computed complexity estimate")
		})
	}
}

func TestScoringAndHelpers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		validate func(t *testing.T)
	}{
		{
			name: "severity mapping uses expected buckets",
			validate: func(t *testing.T) {
				require.Equal(t, 100.0, computeSeverity(models.IssueSeverity("critical")), "computeSeverity should map critical to max score")
				require.Equal(t, 75.0, computeSeverity(models.IssueSeverity("HIGH")), "computeSeverity should be case-insensitive for high severity")
				require.Equal(t, 50.0, computeSeverity(models.IssueSeverity("medium")), "computeSeverity should map medium to 50")
				require.Equal(t, 25.0, computeSeverity(models.IssueSeverity("unknown")), "computeSeverity should default unknown values to low score")
			},
		},
		{
			name: "recency decays over time and clamps future to now",
			validate: func(t *testing.T) {
				nowScore := computeRecency(time.Now())
				oldScore := computeRecency(time.Now().Add(-14 * 24 * time.Hour))
				futureScore := computeRecency(time.Now().Add(2 * time.Hour))
				require.Greater(t, nowScore, oldScore, "computeRecency should give higher scores for more recent issues")
				require.InDelta(t, 100.0, futureScore, 0.001, "computeRecency should clamp future timestamps to the max score")
			},
		},
		{
			name: "customer impact is capped at 100",
			validate: func(t *testing.T) {
				require.InDelta(t, 0.0, computeCustomerImpact(0, 0), 0.0001, "computeCustomerImpact should be zero for no impact")
				require.Equal(t, 100.0, computeCustomerImpact(1_000_000, 1_000_000), "computeCustomerImpact should cap large values at 100")
			},
		},
		{
			name: "helper defaults and gates return expected values",
			validate: func(t *testing.T) {
				require.Equal(t, 0.25, defaultOrValue(0, 0.25), "defaultOrValue should return default when value is zero")
				require.Equal(t, 0.5, defaultOrValue(0.5, 0.25), "defaultOrValue should return value when non-zero")
			},
		},
		{
			name: "org settings parsing uses defaults for invalid json",
			validate: func(t *testing.T) {
				settings := parseOrgSettings(zerolog.Nop(), json.RawMessage(`{"priority_weights":{"severity":0.5}}`))
				require.Equal(t, 0.5, settings.PriorityWeights.Severity, "parseOrgSettings should parse scoring configuration")

				badSettings := parseOrgSettings(zerolog.Nop(), json.RawMessage(`{"priority_weights":`))
				require.Equal(t, models.PriorityWeights{}, badSettings.PriorityWeights, "parseOrgSettings should return zero values for invalid json")
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.validate(t)
		})
	}
}

func TestLLMResponseParsing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		alignmentResponse string
		complexityResp    string
		expectedAlign     float64
		expectedTier      int
		expectedErr       bool
	}{
		{
			name:              "clamps alignment and tier to valid bounds",
			alignmentResponse: `{"alignment":2,"reasoning":"great"}`,
			complexityResp:    `{"tier":0,"label":"tiny","confidence":0.7,"reasoning":"small"}`,
			expectedAlign:     1,
			expectedTier:      1,
			expectedErr:       false,
		},
		{
			name:              "returns errors on invalid json",
			alignmentResponse: `not-json`,
			complexityResp:    `not-json`,
			expectedErr:       true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			callN := 0
			llm := &fakeLLMClient{completeFn: func(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
				callN++
				if callN == 1 {
					return tt.alignmentResponse, nil
				}
				return tt.complexityResp, nil
			}}

			svc := NewService(&fakeIssueStore{}, &fakePriorityStore{}, &fakeComplexityStore{}, &fakeOrgStore{}, llm, zerolog.Nop())
			issue := &models.Issue{Title: "nil pointer", Severity: "high"}

			align, alignErr := svc.computeDirectionAlignment(context.Background(), issue, "reduce churn")
			tier, _, _, _, complexityErr := svc.estimateComplexityViaLLM(context.Background(), &models.Issue{Title: "x", Severity: "high", OccurrenceCount: 1, AffectedCustomerCount: 1})

			if tt.expectedErr {
				require.Error(t, alignErr, "computeDirectionAlignment should return an error for invalid JSON responses")
				require.Error(t, complexityErr, "estimateComplexityViaLLM should return an error for invalid JSON responses")
				return
			}

			require.NoError(t, alignErr, "computeDirectionAlignment should parse a valid JSON response")
			require.NoError(t, complexityErr, "estimateComplexityViaLLM should parse a valid JSON response")
			require.InDelta(t, tt.expectedAlign, align, 0.0001, "computeDirectionAlignment should clamp values into [-1,1]")
			require.Equal(t, tt.expectedTier, tier, "estimateComplexityViaLLM should clamp tier into [1,5]")
		})
	}
}

func ptr[T any](v T) *T {
	return &v
}

func TestComputeScoreReturnsErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		setup  func(issues *fakeIssueStore, priorities *fakePriorityStore, orgs *fakeOrgStore)
		assert func(t *testing.T, err error)
	}{
		{
			name: "returns issue fetch error",
			setup: func(issues *fakeIssueStore, priorities *fakePriorityStore, orgs *fakeOrgStore) {
				issues.getByIDFn = func(ctx context.Context, orgID, issueID uuid.UUID) (models.Issue, error) {
					return models.Issue{}, errors.New("issue missing")
				}
			},
			assert: func(t *testing.T, err error) {
				require.Error(t, err, "ComputeScore should return an error when issue lookup fails")
				require.Contains(t, err.Error(), "fetch issue", "ComputeScore should wrap issue lookup errors")
			},
		},
		{
			name: "returns org fetch error",
			setup: func(issues *fakeIssueStore, priorities *fakePriorityStore, orgs *fakeOrgStore) {
				issues.getByIDFn = func(ctx context.Context, orgID, issueID uuid.UUID) (models.Issue, error) {
					return models.Issue{ID: issueID, OrgID: orgID, Status: "open", Severity: "high", LastSeenAt: time.Now()}, nil
				}
				orgs.getByIDFn = func(ctx context.Context, id uuid.UUID) (models.Organization, error) {
					return models.Organization{}, errors.New("org missing")
				}
			},
			assert: func(t *testing.T, err error) {
				require.Error(t, err, "ComputeScore should return an error when org lookup fails")
				require.Contains(t, err.Error(), "fetch org", "ComputeScore should wrap org lookup errors")
			},
		},
		{
			name: "returns upsert error",
			setup: func(issues *fakeIssueStore, priorities *fakePriorityStore, orgs *fakeOrgStore) {
				issues.getByIDFn = func(ctx context.Context, orgID, issueID uuid.UUID) (models.Issue, error) {
					return models.Issue{ID: issueID, OrgID: orgID, Status: "open", Severity: "high", LastSeenAt: time.Now(), OccurrenceCount: 20, AffectedCustomerCount: 20}, nil
				}
				orgs.getByIDFn = func(ctx context.Context, id uuid.UUID) (models.Organization, error) {
					return models.Organization{ID: id}, nil
				}
				priorities.upsertFn = func(ctx context.Context, score *models.PriorityScore) error { return errors.New("write failed") }
			},
			assert: func(t *testing.T, err error) {
				require.Error(t, err, "ComputeScore should return an error when score upsert fails")
				require.Contains(t, err.Error(), "upsert priority score", "ComputeScore should wrap upsert errors")
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			issues := &fakeIssueStore{}
			priorities := &fakePriorityStore{}
			orgs := &fakeOrgStore{}
			tt.setup(issues, priorities, orgs)

			svc := NewService(issues, priorities, &fakeComplexityStore{}, orgs, nil, zerolog.Nop())
			_, err := svc.ComputeScore(context.Background(), uuid.New(), uuid.New())
			tt.assert(t, err)
		})
	}
}

func TestEstimateComplexityReturnsErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		setup  func(issues *fakeIssueStore, complexity *fakeComplexityStore)
		assert func(t *testing.T, err error)
	}{
		{
			name: "returns issue fetch error when issue is nil",
			setup: func(issues *fakeIssueStore, complexity *fakeComplexityStore) {
				issues.getByIDFn = func(ctx context.Context, orgID, issueID uuid.UUID) (models.Issue, error) {
					return models.Issue{}, errors.New("not found")
				}
			},
			assert: func(t *testing.T, err error) {
				require.Error(t, err, "EstimateComplexity should return an error when issue fetch fails")
				require.Contains(t, err.Error(), "fetch issue", "EstimateComplexity should wrap issue fetch errors")
			},
		},
		{
			name: "returns upsert error",
			setup: func(issues *fakeIssueStore, complexity *fakeComplexityStore) {
				complexity.upsertFn = func(ctx context.Context, est *models.ComplexityEstimate) error { return errors.New("db down") }
			},
			assert: func(t *testing.T, err error) {
				require.Error(t, err, "EstimateComplexity should return an error when upsert fails")
				require.Contains(t, err.Error(), "upsert complexity estimate", "EstimateComplexity should wrap complexity upsert errors")
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			issues := &fakeIssueStore{}
			complexity := &fakeComplexityStore{}
			tt.setup(issues, complexity)

			svc := NewService(issues, &fakePriorityStore{}, complexity, &fakeOrgStore{}, nil, zerolog.Nop())
			issue := &models.Issue{ID: uuid.New(), Severity: "medium", LastSeenAt: time.Now()}
			if tt.name == "returns issue fetch error when issue is nil" {
				issue = nil
			}

			_, err := svc.EstimateComplexity(context.Background(), uuid.New(), uuid.New(), issue)
			tt.assert(t, err)
		})
	}
}

func TestComputeRecencyHalfLife(t *testing.T) {
	t.Parallel()

	score := computeRecency(time.Now().Add(-time.Duration(recencyHalfLifeHours) * time.Hour))
	require.InDelta(t, 100/math.E, score, 2.0, "computeRecency should follow the configured exponential decay curve")
	require.False(t, math.IsNaN(score), "computeRecency should never produce NaN values")
}
