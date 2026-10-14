package worker

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewAgentSummariesDoesNotInferCleanReview(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		raw          string
		parsedCount  int
		ownedCount   int
		orchestrator bool
		expected     []string
	}{
		{
			name:     "prose findings missed by the directive parser",
			raw:      "High severity: the shared cache can serve data from another tenant.",
			expected: []string{"Codex completed its review"},
		},
		{
			name:     "JSON findings missed by the directive parser",
			raw:      `{"findings":[{"priority":1,"summary":"Missing tenant filter"}]}`,
			expected: []string{"Codex completed its review"},
		},
		{
			name:     "malformed directive does not establish a clean result",
			raw:      `::code-comment{title="[P1] Missing tenant filter" body="The lookup crosses tenants." file="internal/db/cache.go"}`,
			expected: []string{"Codex completed its review"},
		},
		{
			name:     "a parsed directive reports a finding",
			raw:      `::code-comment{title="[P1] Missing tenant filter" body="The lookup crosses tenants." file="internal/db/cache.go" start=42 priority=1}`,
			expected: []string{"Codex reported 1 finding"},
		},
		{
			name:         "synthesized blockers are not attributed to the original reviewer",
			raw:          "The lookup crosses tenants.",
			orchestrator: true,
			expected:     []string{"Codex completed its review"},
		},
		{
			name:        "harvested findings remain visible after synthesis replaces attribution",
			parsedCount: 2,
			expected:    []string{"Codex reported 2 findings"},
		},
		{
			name:       "reports an attributed finding",
			ownedCount: 1,
			expected:   []string{"Codex reported 1 finding"},
		},
		{
			name:        "harvested and attributed counts are not added together",
			parsedCount: 2,
			ownedCount:  2,
			expected:    []string{"Codex reported 2 findings"},
		},
		{
			name:     "a clean prose response also uses a neutral completion summary",
			raw:      "No actionable findings.",
			expected: []string{"Codex completed its review"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resultID := uuid.New()
			results := []models.CodeReviewAgentResult{{
				ID:            resultID,
				AgentProvider: "codex",
				Role:          models.CodeReviewAgentRoleReviewer,
				Status:        models.CodeReviewAgentResultStatusCompleted,
				RawOutput:     &tt.raw,
				StructuredResult: marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{
					FindingCount: max(tt.parsedCount, len(parseCodeReviewFindings(tt.raw, []string{"internal/db/cache.go"}))),
				}),
			}}
			findings := make([]models.CodeReviewFinding, 0, tt.ownedCount+1)
			for i := 0; i < tt.ownedCount; i++ {
				findings = append(findings, models.CodeReviewFinding{AgentResultID: &resultID, Severity: models.CodeReviewFindingSeverityHigh})
			}
			if tt.orchestrator {
				orchestratorID := uuid.New()
				results = append(results, models.CodeReviewAgentResult{
					ID: orchestratorID, Role: models.CodeReviewAgentRoleOrchestrator, Status: models.CodeReviewAgentResultStatusCompleted,
				})
				findings = append(findings, models.CodeReviewFinding{AgentResultID: &orchestratorID, Severity: models.CodeReviewFindingSeverityHigh})
			}

			summaries := codeReviewAgentSummaries(results, findings)
			require.Equal(t, tt.expected, summaries, "reviewer summaries should report completion or recorded findings without inferring a clean verdict")
			body := models.BuildCodeReviewFinalReviewBody(models.CodeReviewFinalReviewInput{AgentSummaries: summaries, Findings: findings})
			require.NotContains(t, body, "found no blocking issues", "the published review should not turn a parsing miss into a clean verdict")
		})
	}
}

func TestPersistCodeReviewOrchestratorFindingsUsesCanonicalSynthesis(t *testing.T) {
	t.Parallel()

	const path = "internal/db/cache.go"
	const directive = `::code-comment{title="[P1] Scope cached lookups to the tenant" body="[P1] A missing tenant filter exposes another tenant's cache entries." file="/workspace/internal/db/cache.go" start=42 end=42 priority=1}`
	newFinding := func(summary, body string) models.CodeReviewFinding {
		return models.CodeReviewFinding{
			DedupeKey: codeReviewFindingDedupeKey(path, 42, 42, summary),
			Severity:  models.CodeReviewFindingSeverityHigh, Confidence: models.CodeReviewFindingConfidenceHigh,
			Path: stringPtr(path), StartLine: intPtr(42), EndLine: intPtr(42), Summary: summary, Body: body,
		}
	}
	tests := []struct {
		name            string
		structured      bool
		structuredCount int
		superseded      bool
		raw             string
		findings        func() []models.CodeReviewFinding
	}{
		{
			name:            "reworded inline version does not duplicate a structured finding",
			structured:      true,
			structuredCount: 1,
			superseded:      true,
			raw:             directive,
			findings: func() []models.CodeReviewFinding {
				return []models.CodeReviewFinding{newFinding("Missing tenant filter", "The cache lookup must include the tenant ID.")}
			},
		},
		{
			name:            "distinct structured issues on the same line remain separate",
			structured:      true,
			structuredCount: 2,
			superseded:      true,
			raw:             directive,
			findings: func() []models.CodeReviewFinding {
				return []models.CodeReviewFinding{
					newFinding("Missing tenant filter", "The cache lookup must include the tenant ID."),
					newFinding("Expired cache entries are accepted", "The lookup must check the entry expiration."),
				}
			},
		},
		{
			name:            "two directive-only issues cannot be hidden by one structured issue",
			structured:      true,
			structuredCount: 1,
			raw:             directive + "\n" + `::code-comment{title="[P1] Reject ambiguous cache keys" body="[P1] Unescaped separators make distinct cache keys collide." file="internal/db/cache.go" start=42 priority=1}`,
			findings: func() []models.CodeReviewFinding {
				return []models.CodeReviewFinding{
					newFinding("Missing tenant filter", "The cache lookup must include the tenant ID."),
					newFinding("Scope cached lookups to the tenant", "[P1] A missing tenant filter exposes another tenant's cache entries."),
					newFinding("Reject ambiguous cache keys", "[P1] Unescaped separators make distinct cache keys collide."),
				}
			},
		},
		{
			name:            "an exact match consumes its structured counterpart before reworded matching",
			structured:      true,
			structuredCount: 1,
			raw:             strings.Replace(directive, "Scope cached lookups to the tenant", "Missing tenant filter", 1) + "\n" + `::code-comment{title="[P1] Reject ambiguous cache keys" body="[P1] Unescaped separators make distinct cache keys collide." file="internal/db/cache.go" start=42 priority=1}`,
			findings: func() []models.CodeReviewFinding {
				return []models.CodeReviewFinding{
					newFinding("Missing tenant filter", "The cache lookup must include the tenant ID."),
					newFinding("Reject ambiguous cache keys", "[P1] Unescaped separators make distinct cache keys collide."),
				}
			},
		},
		{
			name:            "an unmatched directive at a different line survives synthesis",
			structured:      true,
			structuredCount: 1,
			raw:             strings.Replace(directive, "start=42 end=42", "start=53 end=53", 1),
			findings: func() []models.CodeReviewFinding {
				unmatched := newFinding("Scope cached lookups to the tenant", "[P1] A missing tenant filter exposes another tenant's cache entries.")
				unmatched.StartLine, unmatched.EndLine = intPtr(53), intPtr(53)
				unmatched.DedupeKey = codeReviewFindingDedupeKey(path, 53, 53, unmatched.Summary)
				return []models.CodeReviewFinding{newFinding("Missing tenant filter", "The cache lookup must include the tenant ID."), unmatched}
			},
		},
		{
			name:            "a higher priority directive is not hidden by an advisory finding at the same location",
			structured:      true,
			structuredCount: 1,
			raw:             directive,
			findings: func() []models.CodeReviewFinding {
				advisory := newFinding("Clarify the cache lookup", "The cache lookup should be easier to read.")
				advisory.Severity = models.CodeReviewFindingSeverityMedium
				return []models.CodeReviewFinding{advisory, newFinding("Scope cached lookups to the tenant", "[P1] A missing tenant filter exposes another tenant's cache entries.")}
			},
		},
		{
			name:       "an empty structured array does not discard directive blockers",
			structured: true,
			raw:        directive,
			findings: func() []models.CodeReviewFinding {
				return []models.CodeReviewFinding{newFinding("Scope cached lookups to the tenant", "[P1] A missing tenant filter exposes another tenant's cache entries.")}
			},
		},
		{
			name: "legacy or malformed synthesis retains directive findings",
			raw:  directive + "\n" + directive,
			findings: func() []models.CodeReviewFinding {
				return []models.CodeReviewFinding{newFinding("Scope cached lookups to the tenant", "[P1] A missing tenant filter exposes another tenant's cache entries.")}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "finding persistence should have an isolated database mock")
			defer mock.Close()
			orgID, sessionID, resultID := uuid.New(), uuid.New(), uuid.New()
			now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
			expected := tt.findings()
			var synthesis *codeReviewOrchestratorSynthesis
			if tt.structured {
				synthesis = &codeReviewOrchestratorSynthesis{Findings: make([]codeReviewOrchestratorFinding, 0, len(expected))}
				for _, finding := range expected[:tt.structuredCount] {
					synthesis.Findings = append(synthesis.Findings, codeReviewOrchestratorFinding{
						Severity: finding.Severity, Confidence: finding.Confidence, Path: finding.Path,
						StartLine: finding.StartLine, EndLine: finding.EndLine, Summary: finding.Summary, Body: finding.Body,
					})
				}
				mock.ExpectQuery("(?s)SELECT .*FROM code_review_findings").
					WithArgs(orgID, sessionID, false).
					WillReturnRows(newCodeReviewFindingRows())
			}
			for _, finding := range expected {
				mock.ExpectQuery("INSERT INTO code_review_findings").
					WithArgs(orgID, sessionID, &resultID, finding.DedupeKey, finding.Severity, finding.Confidence,
						finding.Path, finding.StartLine, finding.EndLine, finding.Summary, finding.Body, false, (*int64)(nil)).
					WillReturnRows(newCodeReviewFindingRows().AddRow(uuid.New(), orgID, sessionID, &resultID,
						finding.DedupeKey, finding.Severity, finding.Confidence, finding.Path, finding.StartLine, finding.EndLine,
						finding.Summary, finding.Body, false, nil, now))
			}
			if tt.superseded {
				mock.ExpectExec("DELETE FROM code_review_findings").
					WithArgs(orgID, sessionID, resultID, []string{codeReviewFindingDedupeKey(path, 42, 42, "Scope cached lookups to the tenant")}).
					WillReturnResult(pgxmock.NewResult("DELETE", 0))
			}

			count, err := persistCodeReviewOrchestratorFindings(context.Background(), &Stores{CodeReviews: db.NewCodeReviewStore(mock)},
				runCodeReviewPayload{OrgID: orgID, SessionID: sessionID}, resultID, synthesis, tt.raw, []string{path})
			require.NoError(t, err, "synthesis should persist each canonical issue exactly once")
			require.Equal(t, len(expected), count, "persisted count should include each issue once, including distinct issues on the same line")
			require.NoError(t, mock.ExpectationsWereMet(), "persistence should write the canonical content with the review's tenant and session")

			structured := []models.CodeReviewFinding{}
			if synthesis != nil {
				structured = codeReviewFindingsFromSynthesis(*synthesis, []string{path})
			}
			actual, _ := reconcileCodeReviewOrchestratorFindings(structured, parseCodeReviewFindings(tt.raw, []string{path}))
			require.Equal(t, expected, actual, "reconciliation should retain the exact canonical or unmatched issue contents")
			body := models.BuildCodeReviewFinalReviewBody(models.CodeReviewFinalReviewInput{Findings: actual})
			for _, finding := range expected {
				require.Equal(t, 1, strings.Count(body, fmt.Sprintf("%s:%d - %s", path, *finding.StartLine, finding.Summary)), "each canonical issue should appear once in the final review body")
			}
		})
	}
}

func TestReconcileCodeReviewOrchestratorFindingsRetainsHighestSeverity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		structured models.CodeReviewFindingSeverity
		directive  models.CodeReviewFindingSeverity
		expected   models.CodeReviewFindingSeverity
		body       string
	}{
		{name: "a blocker cannot be downgraded by an exact-key advisory finding", structured: models.CodeReviewFindingSeverityMedium, directive: models.CodeReviewFindingSeverityHigh, expected: models.CodeReviewFindingSeverityHigh, body: "Directive scenario."},
		{name: "a lower severity directive cannot downgrade the canonical finding", structured: models.CodeReviewFindingSeverityCritical, directive: models.CodeReviewFindingSeverityHigh, expected: models.CodeReviewFindingSeverityCritical, body: "Structured scenario."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			canonical := models.CodeReviewFinding{DedupeKey: "same-issue", Summary: "Missing tenant filter", Severity: tt.structured, Confidence: models.CodeReviewFindingConfidenceHigh, Body: "Structured scenario."}
			directive := canonical
			directive.Severity, directive.Body = tt.directive, "Directive scenario."
			expected := canonical
			expected.Severity, expected.Body = tt.expected, tt.body
			actual, superseded := reconcileCodeReviewOrchestratorFindings([]models.CodeReviewFinding{canonical}, []models.CodeReviewFinding{directive})
			require.Equal(t, []models.CodeReviewFinding{expected}, actual, "exact-key reconciliation should retain the most severe evidence and its concrete scenario")
			require.Equal(t, []string{}, superseded, "an exact key should be updated rather than deleted")
		})
	}
}
