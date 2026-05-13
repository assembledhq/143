package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CircleCITestInsights implements CITestInsights for CircleCI. It uses the
// CircleCI v2 Insights API to surface flaky tests and the v2 project test
// metadata endpoint to fetch the actual failure messages for a specific job.
//
// API endpoints used:
//
//   - GET /api/v2/insights/{project-slug}/flaky-tests
//     returns the provider's flaky-test detection results (CircleCI flags a
//     test as flaky when the same test on the same commit/git ref flips
//     between pass and fail).
//   - GET /api/v2/project/{project-slug}/{job-number}/tests
//     returns the per-test results (with failure message and run time) for
//     a specific job — used to read what a flaky test actually looked like
//     when it failed.
//
// Authentication: CircleCI personal API tokens, sent in the Circle-Token
// header. The same token works for both endpoints. Token scope: read access
// to the project.
type CircleCITestInsights struct {
	httpClient  *http.Client
	baseURL     string
	authToken   string
	projectSlug string // e.g. "gh/assembledhq/143" or "github/assembledhq/143"
}

// CircleCIConfig holds the connection details for a CircleCI provider.
type CircleCIConfig struct {
	BaseURL     string // defaults to "https://circleci.com"
	AuthToken   string // CircleCI personal API token
	ProjectSlug string // VCS-prefixed slug, e.g. "gh/org/repo"
}

// NewCircleCITestInsights creates a CircleCI CITestInsights provider.
func NewCircleCITestInsights(cfg CircleCIConfig) *CircleCITestInsights {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://circleci.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	return &CircleCITestInsights{
		httpClient:  &http.Client{Timeout: 30 * time.Second},
		baseURL:     baseURL,
		authToken:   cfg.AuthToken,
		projectSlug: cfg.ProjectSlug,
	}
}

func (c *CircleCITestInsights) Name() string { return "circleci" }

// ListFlakyTests fetches CircleCI's flaky-test detector output for the
// configured project. CircleCI flags a test as flaky when it has flipped
// pass/fail on the same commit within the analysed window (default 14 days).
func (c *CircleCITestInsights) ListFlakyTests(ctx context.Context, filter FlakyTestFilter) ([]FlakyTest, error) {
	if c.projectSlug == "" {
		return nil, errors.New("circleci: project_slug not configured")
	}

	params := url.Values{}
	if filter.Branch != "" {
		params.Set("branch", filter.Branch)
	}
	if filter.WorkflowName != "" {
		params.Set("workflow-name", filter.WorkflowName)
	}

	endpoint := fmt.Sprintf("%s/api/v2/insights/%s/flaky-tests",
		c.baseURL, c.projectSlug)
	if encoded := params.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}

	var resp ccFlakyTestsResponse
	if err := c.doGet(ctx, endpoint, &resp); err != nil {
		return nil, fmt.Errorf("list flaky tests: %w", err)
	}

	limit := filter.Limit
	if limit <= 0 || limit > len(resp.FlakyTests) {
		limit = len(resp.FlakyTests)
	}

	results := make([]FlakyTest, 0, limit)
	for i, t := range resp.FlakyTests {
		if i >= limit {
			break
		}
		ft := FlakyTest{
			TestName:      t.TestName,
			Classname:     t.Classname,
			File:          t.File,
			JobName:       t.JobName,
			WorkflowName:  t.WorkflowName,
			TimesFlaked:   t.TimesFlaked,
			LastFailureAt: parseCircleCITime(t.WorkflowCreatedAt),
		}
		if t.JobNumber > 0 {
			ft.LastJob = &JobRef{
				JobNumber: t.JobNumber,
				WebURL:    c.jobWebURL(t.JobNumber),
			}
		}
		results = append(results, ft)
	}
	return results, nil
}

// GetTestResults fetches individual test results for a single CircleCI job.
// The response includes per-test pass/fail status and (for failed tests) the
// failure message — the agent uses this to read what the flaky test failure
// actually looked like before deciding how to fix it.
func (c *CircleCITestInsights) GetTestResults(ctx context.Context, ref JobRef) ([]TestResult, error) {
	if c.projectSlug == "" {
		return nil, errors.New("circleci: project_slug not configured")
	}
	if ref.JobNumber <= 0 {
		return nil, errors.New("circleci: job_number must be > 0")
	}

	endpoint := fmt.Sprintf("%s/api/v2/project/%s/%d/tests",
		c.baseURL, c.projectSlug, ref.JobNumber)

	var resp ccJobTestsResponse
	if err := c.doGet(ctx, endpoint, &resp); err != nil {
		return nil, fmt.Errorf("get test results: %w", err)
	}

	results := make([]TestResult, 0, len(resp.Items))
	for _, t := range resp.Items {
		results = append(results, TestResult{
			TestName:  t.Name,
			Classname: t.Classname,
			File:      t.File,
			Result:    t.Result,
			RunTime:   t.RunTime,
			Message:   t.Message,
			JobNumber: ref.JobNumber,
		})
	}
	return results, nil
}

// GetRecentFailures finds recent failed occurrences of a single flaky test
// across recent jobs in the project. The agent uses this to compare multiple
// failure messages and identify a root cause rather than reacting to a single
// failure.
//
// Implementation: the flaky-tests endpoint already groups occurrences by test,
// so we re-fetch it and pull the matching test's recent failure messages from
// its associated job. CircleCI's API doesn't expose a single "failures across
// jobs for one test" endpoint, so we cap the dive at `limit` jobs.
func (c *CircleCITestInsights) GetRecentFailures(ctx context.Context, classname, testName string, limit int) ([]TestResult, error) {
	if testName == "" {
		return nil, errors.New("circleci: test_name is required")
	}
	if limit <= 0 {
		limit = 5
	}

	endpoint := fmt.Sprintf("%s/api/v2/insights/%s/flaky-tests",
		c.baseURL, c.projectSlug)

	var resp ccFlakyTestsResponse
	if err := c.doGet(ctx, endpoint, &resp); err != nil {
		return nil, fmt.Errorf("get recent failures: %w", err)
	}

	var failures []TestResult
	seenJobs := make(map[int]bool)
	for _, t := range resp.FlakyTests {
		if t.TestName != testName {
			continue
		}
		if classname != "" && t.Classname != classname {
			continue
		}
		if t.JobNumber <= 0 || seenJobs[t.JobNumber] {
			continue
		}
		seenJobs[t.JobNumber] = true

		jobResults, err := c.GetTestResults(ctx, JobRef{JobNumber: t.JobNumber})
		if err != nil {
			// One job's failure shouldn't kill the whole call — keep going.
			continue
		}
		for _, r := range jobResults {
			if r.TestName != testName {
				continue
			}
			if classname != "" && r.Classname != classname {
				continue
			}
			if !isFailureResult(r.Result) {
				continue
			}
			r.RunAt = parseCircleCITime(t.WorkflowCreatedAt)
			failures = append(failures, r)
			if len(failures) >= limit {
				return failures, nil
			}
		}
	}
	return failures, nil
}

func isFailureResult(r string) bool {
	r = strings.ToLower(strings.TrimSpace(r))
	return r == "failure" || r == "error" || r == "failed"
}

// jobWebURL builds a UI link for a CircleCI job. CircleCI URLs require the
// VCS host + org + repo, which our project slug supplies in either
// "gh/org/repo" or "github/org/repo" form. We normalise to "gh" for the URL.
func (c *CircleCITestInsights) jobWebURL(jobNumber int) string {
	parts := strings.SplitN(c.projectSlug, "/", 3)
	if len(parts) < 3 {
		return ""
	}
	vcs := parts[0]
	switch vcs {
	case "github":
		vcs = "gh"
	case "bitbucket":
		vcs = "bb"
	}
	return fmt.Sprintf("https://app.circleci.com/pipelines/%s/%s/%s/jobs/%d",
		vcs, parts[1], parts[2], jobNumber)
}

func (c *CircleCITestInsights) doGet(ctx context.Context, urlStr string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Circle-Token", c.authToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("circleci API returned %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

// parseCircleCITime parses an ISO-8601 timestamp. CircleCI returns RFC3339
// with sub-second precision; on parse failure we return the zero time so the
// caller can omit the field.
func parseCircleCITime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

// --- response shapes ---

// ccFlakyTestsResponse handles both `flaky-tests` and `flaky_tests` because
// CircleCI's docs and live API have been inconsistent about which form they
// emit. UnmarshalJSON picks whichever is present.
type ccFlakyTestsResponse struct {
	FlakyTests []ccFlakyTest
}

func (r *ccFlakyTestsResponse) UnmarshalJSON(data []byte) error {
	var raw struct {
		Hyphen []ccFlakyTest `json:"flaky-tests"`
		Snake  []ccFlakyTest `json:"flaky_tests"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw.Hyphen) > 0 {
		r.FlakyTests = raw.Hyphen
	} else {
		r.FlakyTests = raw.Snake
	}
	return nil
}

// ccFlakyTest defines both hyphenated and snake_case tags so a custom
// UnmarshalJSON can fall back gracefully if CircleCI emits the other form.
type ccFlakyTest struct {
	TestName          string
	Classname         string
	File              string
	JobName           string
	JobNumber         int
	WorkflowName      string
	WorkflowCreatedAt string
	PipelineNumber    int
	TimesFlaked       int
	Source            string
}

func (t *ccFlakyTest) UnmarshalJSON(data []byte) error {
	var raw struct {
		TestNameHyphen      string `json:"test-name"`
		TestNameSnake       string `json:"test_name"`
		Classname           string `json:"classname"`
		File                string `json:"file"`
		JobNameHyphen       string `json:"job-name"`
		JobNameSnake        string `json:"job_name"`
		JobNumberHyphen     int    `json:"job-number"`
		JobNumberSnake      int    `json:"job_number"`
		WorkflowNameHyphen  string `json:"workflow-name"`
		WorkflowNameSnake   string `json:"workflow_name"`
		WorkflowCreatedAtH  string `json:"workflow-created-at"`
		WorkflowCreatedAtS  string `json:"workflow_created_at"`
		PipelineNumberH     int    `json:"pipeline-number"`
		PipelineNumberS     int    `json:"pipeline_number"`
		TimesFlakedHyphen   int    `json:"times-flaked"`
		TimesFlakedSnake    int    `json:"times_flaked"`
		Source              string `json:"source"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	t.TestName = firstNonEmpty(raw.TestNameHyphen, raw.TestNameSnake)
	t.Classname = raw.Classname
	t.File = raw.File
	t.JobName = firstNonEmpty(raw.JobNameHyphen, raw.JobNameSnake)
	t.JobNumber = firstNonZero(raw.JobNumberHyphen, raw.JobNumberSnake)
	t.WorkflowName = firstNonEmpty(raw.WorkflowNameHyphen, raw.WorkflowNameSnake)
	t.WorkflowCreatedAt = firstNonEmpty(raw.WorkflowCreatedAtH, raw.WorkflowCreatedAtS)
	t.PipelineNumber = firstNonZero(raw.PipelineNumberH, raw.PipelineNumberS)
	t.TimesFlaked = firstNonZero(raw.TimesFlakedHyphen, raw.TimesFlakedSnake)
	t.Source = raw.Source
	return nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func firstNonZero(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

type ccJobTestsResponse struct {
	Items []ccJobTest `json:"items"`
}

type ccJobTest struct {
	Name      string  `json:"name"`
	Classname string  `json:"classname"`
	File      string  `json:"file"`
	Result    string  `json:"result"`
	RunTime   float64 `json:"run_time"`
	Message   string  `json:"message"`
	Source    string  `json:"source"`
}
