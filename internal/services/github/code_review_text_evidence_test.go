package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestDiscoverCodeReviewTextEvidence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body, comments string
		complete             bool
		wantSources          []CodeReviewTextSource
	}{
		{"human discussion and bot excluded", "Purpose\n", `[{"id":10,"html_url":"https://github.com/assembledhq/assembled/pull/42#issuecomment-10","body":"## Testing\nPassed\n","user":{"login":"alice","type":"User"}},{"id":11,"html_url":"https://github.com/assembledhq/assembled/pull/42#issuecomment-11","body":"bot output","user":{"login":"143-app[bot]","type":"Bot"}}]`, true, []CodeReviewTextSource{{Surface: models.CodeReviewEvidenceSurfaceIssueComment, ProviderObjectID: "10", SourceURL: "https://github.com/assembledhq/assembled/pull/42#issuecomment-10", AuthorLogin: "alice", Body: "## Testing\nPassed\n"}}},
		{"oversized PR body incomplete", strings.Repeat("x", codeReviewTextMaxBytes+1), `[]`, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload string
				switch r.URL.Path {
				case "/repos/assembledhq/assembled/pulls/42":
					payload = fmt.Sprintf(`{"body":%q,"head":{"sha":"head-sha"}}`, tt.body)
				case "/repos/assembledhq/assembled/issues/42/comments":
					payload = tt.comments
				case "/repos/assembledhq/assembled/pulls/42/reviews", "/repos/assembledhq/assembled/pulls/42/comments":
					payload = `[]`
				default:
					http.NotFound(w, r)
					return
				}
				_, err := fmt.Fprint(w, payload)
				require.NoError(t, err, "GitHub text fixture response should write")
			}))
			t.Cleanup(server.Close)
			orgID, repoID := uuid.New(), uuid.New()
			service, mock := newVisualEvidenceTestService(t, server, orgID, repoID)
			got, err := service.DiscoverCodeReviewTextEvidence(context.Background(), orgID, repoID, 42)
			require.NoError(t, err, "text evidence discovery should return bounded provider inventory")
			require.Equal(t, tt.complete, got.Complete, "capture completeness should reflect source budgets")
			if tt.complete {
				require.Equal(t, tt.body, got.Body, "raw PR body should be retained exactly")
				require.Equal(t, tt.wantSources, got.Sources, "only human discussion should be citable")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "repository lookup should be org scoped")
		})
	}
}
