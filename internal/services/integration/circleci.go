package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrCircleCIUnauthorized is returned when CircleCI responds with 401/403.
// Tool dispatch wraps this to tell the agent "stop retrying; ask the user to
// reconnect", mirroring the ErrLinearUnauthorized pattern.
var ErrCircleCIUnauthorized = errors.New("circleci unauthorized")

// circleCIMaxPages caps how many pages of test results we follow on a single
// job — 10 pages * 250 per page = 2500 tests, enough to cover most real CI
// jobs without runaway pagination if CircleCI returns broken next tokens.
const circleCIMaxPages = 10

// circleCIMaxJobDives caps how many distinct jobs GetRecentFailures will
// fetch test results from. Each dive is one extra HTTP call, so this bounds
// the worst-case fan-out from a single CLI invocation.
const circleCIMaxJobDives = 20

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
//     when it failed. Paginated via `next_page_token`.
//
// Authentication: CircleCI personal API tokens, sent in the Circle-Token
// header. The same token works for both endpoints. Token scope: read access
// to the project. 401/403 responses are converted to ErrCircleCIUnauthorized
// so the CLI surface can prompt the user to reconnect.
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
//
// Note: the upstream endpoint does not support a server-side `limit`
// parameter, so filter.Limit is enforced client-side by truncating the
// returned slice.
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

// GetTestResults fetches individual test results for a single CircleCI job,
// following `next_page_token` until exhausted (capped at circleCIMaxPages).
// The response includes per-test pass/fail status and (for failed tests) the
// failure message — the agent uses this to read what the flaky test failure
// actually looked like before deciding how to fix it.
//
// CircleCI returns an empty page for jobs that didn't upload JUnit XML via
// `store_test_results`, or for jobs whose results exceed 250MB. In those
// cases this method returns an empty slice with no error; the caller should
// treat that as "no structured test data available for this job".
func (c *CircleCITestInsights) GetTestResults(ctx context.Context, ref JobRef) ([]TestResult, error) {
	if c.projectSlug == "" {
		return nil, errors.New("circleci: project_slug not configured")
	}
	if ref.JobNumber <= 0 {
		return nil, errors.New("circleci: job_number must be > 0")
	}

	var all []TestResult
	pageToken := ""
	for page := 0; page < circleCIMaxPages; page++ {
		params := url.Values{}
		if pageToken != "" {
			params.Set("page-token", pageToken)
		}
		endpoint := fmt.Sprintf("%s/api/v2/project/%s/%d/tests",
			c.baseURL, c.projectSlug, ref.JobNumber)
		if encoded := params.Encode(); encoded != "" {
			endpoint += "?" + encoded
		}

		var resp ccJobTestsResponse
		if err := c.doGet(ctx, endpoint, &resp); err != nil {
			if len(all) > 0 {
				// Return partial results rather than nothing — the agent can
				// still reason about what we did fetch.
				return all, fmt.Errorf("get test results (after page %d): %w", page, err)
			}
			return nil, fmt.Errorf("get test results: %w", err)
		}

		for _, t := range resp.Items {
			all = append(all, TestResult{
				TestName:  t.Name,
				Classname: t.Classname,
				File:      t.File,
				Result:    t.Result,
				RunTime:   t.RunTime,
				Message:   t.Message,
				JobNumber: ref.JobNumber,
			})
		}

		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	return all, nil
}

// GetRecentFailures finds recent failed occurrences of a single flaky test
// across recent jobs in the project. The agent uses this to compare multiple
// failure messages and identify a root cause rather than reacting to a single
// failure.
//
// Implementation: the flaky-tests endpoint already groups occurrences by test,
// so we re-fetch it and pull the matching test's recent failure messages from
// its associated job. CircleCI's API doesn't expose a single "failures across
// jobs for one test" endpoint, so we cap the dive at circleCIMaxJobDives
// distinct jobs and stop when we've collected `limit` failures.
//
// If every dive into a job fails (e.g. revoked token, rate-limited) we return
// the underlying error rather than silently returning [] — the agent needs to
// distinguish "no flakes recently" from "auth broken".
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
	dives := 0
	var lastDiveErr error
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
		if dives >= circleCIMaxJobDives {
			break
		}
		seenJobs[t.JobNumber] = true
		dives++

		jobResults, err := c.GetTestResults(ctx, JobRef{JobNumber: t.JobNumber})
		if err != nil {
			// Remember the most recent error so we can surface it if no
			// successful dive turns up anything. Authorization failures get
			// special handling: bail immediately so the agent gets the
			// "reconnect" signal instead of empty results.
			lastDiveErr = err
			if errors.Is(err, ErrCircleCIUnauthorized) {
				return nil, err
			}
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

	if len(failures) == 0 && lastDiveErr != nil {
		// Every job dive failed AND we found no failures from any
		// successful dive. The empty slice is misleading — surface the
		// underlying error so the agent doesn't conclude "no flakes".
		return nil, fmt.Errorf("get recent failures: all job dives failed: %w", lastDiveErr)
	}
	return failures, nil
}

// isFailureResult reports whether a CircleCI test result string represents
// a failure. CircleCI documents the result enum as success / failure /
// skipped / error.
func isFailureResult(r string) bool {
	switch strings.ToLower(strings.TrimSpace(r)) {
	case "failure", "error":
		return true
	default:
		return false
	}
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

// doGet performs an authenticated GET request and decodes the JSON response.
// 401/403 errors are wrapped in ErrCircleCIUnauthorized so callers can
// distinguish "reconnect" from generic API failures. For other non-2xx
// statuses, the response body is preserved (capped at 4KB) in the error
// message so the agent sees CircleCI's actual diagnostic, not just a code.
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

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		body := readBodySnippet(resp.Body)
		if body != "" {
			return fmt.Errorf("%w (%d): %s", ErrCircleCIUnauthorized, resp.StatusCode, body)
		}
		return fmt.Errorf("%w: HTTP %d", ErrCircleCIUnauthorized, resp.StatusCode)
	}

	if resp.StatusCode != http.StatusOK {
		body := readBodySnippet(resp.Body)
		if body != "" {
			return fmt.Errorf("circleci API returned %d: %s", resp.StatusCode, body)
		}
		return fmt.Errorf("circleci API returned %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

// readBodySnippet reads up to 4KB of the response body and trims it. Used to
// preserve CircleCI's error JSON in failure messages without unbounded reads.
func readBodySnippet(r io.Reader) string {
	const max = 4 << 10
	data, err := io.ReadAll(io.LimitReader(r, max))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// parseCircleCITime parses an ISO-8601 timestamp. CircleCI returns RFC3339
// with sub-second precision; RFC3339Nano already accepts plain RFC3339, so
// one parser handles both cases. Zero time is returned on failure so the
// caller can omit the field.
func parseCircleCITime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
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
		TestNameHyphen     string `json:"test-name"`
		TestNameSnake      string `json:"test_name"`
		Classname          string `json:"classname"`
		File               string `json:"file"`
		JobNameHyphen      string `json:"job-name"`
		JobNameSnake       string `json:"job_name"`
		JobNumberHyphen    int    `json:"job-number"`
		JobNumberSnake     int    `json:"job_number"`
		WorkflowNameHyphen string `json:"workflow-name"`
		WorkflowNameSnake  string `json:"workflow_name"`
		WorkflowCreatedAtH string `json:"workflow-created-at"`
		WorkflowCreatedAtS string `json:"workflow_created_at"`
		PipelineNumberH    int    `json:"pipeline-number"`
		PipelineNumberS    int    `json:"pipeline_number"`
		TimesFlakedHyphen  int    `json:"times-flaked"`
		TimesFlakedSnake   int    `json:"times_flaked"`
		Source             string `json:"source"`
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
	Items         []ccJobTest `json:"items"`
	NextPageToken string      `json:"next_page_token"`
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
