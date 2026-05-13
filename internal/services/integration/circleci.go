package integration

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// ErrCircleCIUnauthorized is the sentinel for 401/403 from CircleCI. Tool
// dispatch matches on this to prompt the user to reconnect rather than
// retry a doomed call (parallels ErrLinearUnauthorized).
var ErrCircleCIUnauthorized = errors.New("circleci unauthorized")

// CircleCI test result enum, as documented by CircleCI's /tests endpoint.
const (
	ccResultSuccess = "success"
	ccResultFailure = "failure"
	ccResultSkipped = "skipped"
	ccResultError   = "error"
)

// Default caps. These bound the worst-case fan-out when CircleCI returns
// runaway pagination or a popular flaky test with many job hits; exposed as
// fields so tests can drive the cap paths.
const (
	defaultCircleCIMaxPages    = 10 // 10 * 250 per page = 2500 tests per job
	defaultCircleCIMaxJobDives = 20
	defaultCircleCIDiveConc    = 4 // concurrent /tests requests per GetRecentFailures call
)

// CircleCITestInsights implements CITestInsights using CircleCI's v2 API:
// the Insights flaky-tests endpoint to find candidates, and the per-job
// /tests endpoint (paginated via next_page_token) to read failure messages.
// Auth is a personal API token in the Circle-Token header.
type CircleCITestInsights struct {
	httpClient  *http.Client
	baseURL     string
	authToken   string
	projectSlug string // e.g. "gh/assembledhq/143" or "github/assembledhq/143"

	maxPages    int
	maxJobDives int
	diveConc    int
}

// CircleCIConfig holds the connection details for a CircleCI provider.
type CircleCIConfig struct {
	BaseURL     string // defaults to "https://circleci.com"
	AuthToken   string // CircleCI personal API token
	ProjectSlug string // VCS-prefixed slug, e.g. "gh/org/repo"
}

// NewCircleCITestInsights creates a CircleCI CITestInsights provider.
func NewCircleCITestInsights(cfg CircleCIConfig) *CircleCITestInsights {
	baseURL := strings.TrimRight(cmp.Or(cfg.BaseURL, "https://circleci.com"), "/")
	return &CircleCITestInsights{
		httpClient:  &http.Client{Timeout: 30 * time.Second},
		baseURL:     baseURL,
		authToken:   cfg.AuthToken,
		projectSlug: cfg.ProjectSlug,
		maxPages:    defaultCircleCIMaxPages,
		maxJobDives: defaultCircleCIMaxJobDives,
		diveConc:    defaultCircleCIDiveConc,
	}
}

func (c *CircleCITestInsights) Name() string { return "circleci" }

// ListFlakyTests returns CircleCI's flaky-test detector output. Note: the
// upstream endpoint has no server-side limit param, so filter.Limit is
// applied client-side.
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

	endpoint := fmt.Sprintf("%s/api/v2/insights/%s/flaky-tests", c.baseURL, c.projectSlug)
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

// GetTestResults fetches per-test results for a single CircleCI job,
// following next_page_token until exhausted (capped at maxPages).
//
// CircleCI returns an empty page for jobs that didn't upload JUnit XML via
// `store_test_results`, or whose results exceed 250MB — callers see an
// empty slice with no error in those cases.
func (c *CircleCITestInsights) GetTestResults(ctx context.Context, ref JobRef) ([]TestResult, error) {
	if c.projectSlug == "" {
		return nil, errors.New("circleci: project_slug not configured")
	}
	if ref.JobNumber <= 0 {
		return nil, errors.New("circleci: job_number must be > 0")
	}

	var all []TestResult
	pageToken := ""
	for page := 0; page < c.maxPages; page++ {
		params := url.Values{}
		if pageToken != "" {
			params.Set("page-token", pageToken)
		}
		endpoint := fmt.Sprintf("%s/api/v2/project/%s/%d/tests", c.baseURL, c.projectSlug, ref.JobNumber)
		if encoded := params.Encode(); encoded != "" {
			endpoint += "?" + encoded
		}

		var resp ccJobTestsResponse
		if err := c.doGet(ctx, endpoint, &resp); err != nil {
			if len(all) > 0 {
				// Return partial results so the agent has *some* data to
				// reason about rather than just an opaque failure.
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
// across recent jobs. CircleCI's API doesn't expose "failures across jobs
// for one test" directly — we fetch the flaky-tests list, pick the jobs
// where this test appears, and dive into each job's /tests endpoint for
// the failure message. Dives run with bounded concurrency.
//
// If every dive fails we return the underlying error rather than [] — the
// agent must be able to distinguish "no flakes" from "auth broken".
func (c *CircleCITestInsights) GetRecentFailures(ctx context.Context, classname, testName string, limit int) ([]TestResult, error) {
	if testName == "" {
		return nil, errors.New("circleci: test_name is required")
	}
	if limit <= 0 {
		limit = 5
	}

	endpoint := fmt.Sprintf("%s/api/v2/insights/%s/flaky-tests", c.baseURL, c.projectSlug)
	var resp ccFlakyTestsResponse
	if err := c.doGet(ctx, endpoint, &resp); err != nil {
		return nil, fmt.Errorf("get recent failures: %w", err)
	}

	jobs := c.flakyJobsFor(resp.FlakyTests, classname, testName)
	failures, err := c.collectFailuresFromJobs(ctx, jobs, classname, testName, limit)
	if err != nil {
		return nil, err
	}
	return failures, nil
}

// flakyJobsFor returns the deduped (job-number, workflow-time) pairs for a
// given test, in flaky-tests response order, capped at maxJobDives.
func (c *CircleCITestInsights) flakyJobsFor(flakies []ccFlakyTest, classname, testName string) []flakyJobRef {
	var jobs []flakyJobRef
	seen := make(map[int]bool)
	for _, t := range flakies {
		if t.TestName != testName {
			continue
		}
		if classname != "" && t.Classname != classname {
			continue
		}
		if t.JobNumber <= 0 || seen[t.JobNumber] {
			continue
		}
		seen[t.JobNumber] = true
		jobs = append(jobs, flakyJobRef{
			JobNumber: t.JobNumber,
			RunAt:     parseCircleCITime(t.WorkflowCreatedAt),
		})
		if len(jobs) >= c.maxJobDives {
			break
		}
	}
	return jobs
}

type flakyJobRef struct {
	JobNumber int
	RunAt     time.Time
}

// collectFailuresFromJobs dives into each job's /tests endpoint concurrently
// (bounded by diveConc), gathers failures matching the test, and returns at
// most `limit` results. Unauthorized errors short-circuit the whole call.
// If every dive errors and we collected nothing, the dive error is surfaced.
func (c *CircleCITestInsights) collectFailuresFromJobs(ctx context.Context, jobs []flakyJobRef, classname, testName string, limit int) ([]TestResult, error) {
	if len(jobs) == 0 {
		return nil, nil
	}

	type jobOutcome struct {
		idx     int
		matches []TestResult
		err     error
	}
	outcomes := make([]jobOutcome, len(jobs))

	gctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var unauthOnce sync.Once
	var unauthErr error

	g, gctx := errgroup.WithContext(gctx)
	g.SetLimit(c.diveConc)

	for i, job := range jobs {
		g.Go(func() error {
			results, err := c.GetTestResults(gctx, JobRef{JobNumber: job.JobNumber})
			if err != nil {
				if errors.Is(err, ErrCircleCIUnauthorized) {
					unauthOnce.Do(func() {
						unauthErr = err
						cancel()
					})
				}
				outcomes[i] = jobOutcome{idx: i, err: err}
				return nil
			}
			matches := make([]TestResult, 0)
			for _, r := range results {
				if r.TestName != testName {
					continue
				}
				if classname != "" && r.Classname != classname {
					continue
				}
				if !isFailureResult(r.Result) {
					continue
				}
				r.RunAt = job.RunAt
				matches = append(matches, r)
			}
			outcomes[i] = jobOutcome{idx: i, matches: matches}
			return nil
		})
	}
	_ = g.Wait()

	if unauthErr != nil {
		return nil, unauthErr
	}

	// Preserve flaky-tests response order so "most recent" stays first.
	var failures []TestResult
	var lastErr error
	for _, o := range outcomes {
		if o.err != nil {
			lastErr = o.err
			continue
		}
		for _, m := range o.matches {
			failures = append(failures, m)
			if len(failures) >= limit {
				return failures, nil
			}
		}
	}
	if len(failures) == 0 && lastErr != nil {
		return nil, fmt.Errorf("get recent failures: all job dives failed: %w", lastErr)
	}
	return failures, nil
}

// isFailureResult reports whether a CircleCI test result represents a failure.
func isFailureResult(r string) bool {
	switch strings.ToLower(strings.TrimSpace(r)) {
	case ccResultFailure, ccResultError:
		return true
	default:
		return false
	}
}

// vcsShortPrefix maps CircleCI's accepted long-form VCS names to the short
// form expected by app.circleci.com pipeline URLs.
var vcsShortPrefix = map[string]string{
	"github":    "gh",
	"bitbucket": "bb",
}

func (c *CircleCITestInsights) jobWebURL(jobNumber int) string {
	parts := strings.SplitN(c.projectSlug, "/", 3)
	if len(parts) < 3 {
		return ""
	}
	vcs := cmp.Or(vcsShortPrefix[parts[0]], parts[0])
	return fmt.Sprintf("https://app.circleci.com/pipelines/%s/%s/%s/jobs/%d", vcs, parts[1], parts[2], jobNumber)
}

// doGet performs an authenticated GET and decodes JSON. 401/403 wrap
// ErrCircleCIUnauthorized; other non-2xx errors preserve up to 4KB of body
// so the agent sees CircleCI's diagnostic, not just a status code.
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

func readBodySnippet(r io.Reader) string {
	const max = 4 << 10
	data, err := io.ReadAll(io.LimitReader(r, max))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

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

// ccFlakyTestsResponse handles both "flaky-tests" and "flaky_tests" because
// CircleCI's docs and live API have shipped both forms over time.
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
	t.TestName = cmp.Or(raw.TestNameHyphen, raw.TestNameSnake)
	t.Classname = raw.Classname
	t.File = raw.File
	t.JobName = cmp.Or(raw.JobNameHyphen, raw.JobNameSnake)
	t.JobNumber = cmp.Or(raw.JobNumberHyphen, raw.JobNumberSnake)
	t.WorkflowName = cmp.Or(raw.WorkflowNameHyphen, raw.WorkflowNameSnake)
	t.WorkflowCreatedAt = cmp.Or(raw.WorkflowCreatedAtH, raw.WorkflowCreatedAtS)
	t.PipelineNumber = cmp.Or(raw.PipelineNumberH, raw.PipelineNumberS)
	t.TimesFlaked = cmp.Or(raw.TimesFlakedHyphen, raw.TimesFlakedSnake)
	t.Source = raw.Source
	return nil
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
