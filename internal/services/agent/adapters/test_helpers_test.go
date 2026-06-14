package adapters

import (
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/testutil"
)

func newTestIssue(source models.IssueSource, hasDescription bool) *models.Issue {
	issue := &models.Issue{
		Source: source,
		Title:  "Test issue",
	}
	if hasDescription {
		desc := "Test description"
		issue.Description = &desc
	}
	return issue
}

func newMockProvider() *testutil.MockSandboxProvider {
	return testutil.NewMockSandboxProvider()
}
