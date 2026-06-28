package preview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/models"
)

const previewWorkerTokenTTL = 30 * time.Second

// previewWorkerHTTPTimeout caps app->worker preview RPCs. It must exceed the
// worst-case launch budget (image pulls + infra startup + service readiness
// probes, each defaulting to 90s) or the API gives up before the worker
// finishes, surfacing as "context canceled" on a readiness probe.
const previewWorkerHTTPTimeout = 10 * time.Minute

const PreviewSoftRestartUnsupportedCode = "PREVIEW_SOFT_RESTART_UNSUPPORTED"

// WorkerRequestError preserves structured worker error responses.
type WorkerRequestError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *WorkerRequestError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("worker preview request failed with %d", e.StatusCode)
	}
	return fmt.Sprintf("worker preview request failed with %d (%s): %s", e.StatusCode, e.Code, e.Message)
}

func (e *WorkerRequestError) Is(target error) bool {
	return target == ErrSoftRestartUnsupported && e.Code == PreviewSoftRestartUnsupportedCode
}

// RemoteStartPreviewRequest is the app->worker request for starting a preview.
type RemoteStartPreviewRequest struct {
	OrgID         uuid.UUID             `json:"org_id"`
	UserID        uuid.UUID             `json:"user_id"`
	SessionID     uuid.UUID             `json:"session_id"`
	Config        *models.PreviewConfig `json:"config,omitempty"`
	BaseCommitSHA string                `json:"base_commit_sha,omitempty"`
	ProfileName   string                `json:"profile_name,omitempty"`
}

// StartPreviewJobPayload is the durable worker job payload for completing a
// previously reserved preview startup.
type StartPreviewJobPayload struct {
	OrgID                      uuid.UUID             `json:"org_id"`
	UserID                     uuid.UUID             `json:"user_id"`
	SessionID                  uuid.UUID             `json:"session_id"`
	PreviewID                  uuid.UUID             `json:"preview_id"`
	Config                     *models.PreviewConfig `json:"config,omitempty"`
	BaseCommitSHA              string                `json:"base_commit_sha,omitempty"`
	ProfileName                string                `json:"profile_name,omitempty"`
	WorkspaceRevision          int64                 `json:"workspace_revision"`
	WorkspaceRevisionUpdatedAt time.Time             `json:"workspace_revision_updated_at"`
}

// StartBranchPreviewJobPayload is the durable worker job payload for starting
// a standalone branch/PR preview from a pinned Git commit.
type StartBranchPreviewJobPayload struct {
	OrgID             uuid.UUID             `json:"org_id"`
	UserID            uuid.UUID             `json:"user_id"`
	PreviewID         uuid.UUID             `json:"preview_id"`
	PreviewTargetID   uuid.UUID             `json:"preview_target_id"`
	RepositoryID      uuid.UUID             `json:"repository_id"`
	Branch            string                `json:"branch"`
	CommitSHA         string                `json:"commit_sha"`
	PreviewConfigName string                `json:"preview_config_name,omitempty"`
	Config            *models.PreviewConfig `json:"config,omitempty"`
	ProfileName       string                `json:"profile_name,omitempty"`
	Initiator         string                `json:"initiator,omitempty"`
	StopAfterReady    bool                  `json:"stop_after_ready,omitempty"`
}

// AutoPreviewDeferredPayload is the payload for a deferred auto-preview
// start job, enqueued when the auto-preview pool is full at webhook time.
// The job re-attempts the start once capacity is available, retrying with
// backoff rather than silently dropping the webhook event.
type AutoPreviewDeferredPayload struct {
	OrgID             uuid.UUID              `json:"org_id"`
	UserID            uuid.UUID              `json:"user_id"`
	RepositoryID      uuid.UUID              `json:"repository_id"`
	PRNumber          int                    `json:"pr_number"`
	HeadRef           string                 `json:"head_ref"`
	HeadSHA           string                 `json:"head_sha"`
	HTMLURL           string                 `json:"html_url"`
	Mode              models.PreviewAutoMode `json:"mode"`
	PreviewConfigName string                 `json:"preview_config_name,omitempty"`
}

type PreviewCachePrewarmSource string

const (
	PreviewCachePrewarmSourceSession PreviewCachePrewarmSource = "session"
	PreviewCachePrewarmSourceBranch  PreviewCachePrewarmSource = "branch"
)

type PreviewCachePrewarmJobPayload struct {
	JobID             uuid.UUID                 `json:"job_id,omitempty"`
	OrgID             uuid.UUID                 `json:"org_id"`
	RepositoryID      uuid.UUID                 `json:"repository_id"`
	UserID            uuid.UUID                 `json:"user_id,omitempty"`
	Source            PreviewCachePrewarmSource `json:"source"`
	SessionID         uuid.UUID                 `json:"session_id,omitempty"`
	PreviewTargetID   uuid.UUID                 `json:"preview_target_id,omitempty"`
	Branch            string                    `json:"branch,omitempty"`
	CommitSHA         string                    `json:"commit_sha,omitempty"`
	PreviewConfigName string                    `json:"preview_config_name,omitempty"`
	WorkspaceRevision int64                     `json:"workspace_revision,omitempty"`
	ConfigDigest      string                    `json:"config_digest,omitempty"`
	Reason            string                    `json:"reason,omitempty"`
}

type SessionPreviewPrewarmClassifyJobPayload struct {
	JobID             uuid.UUID `json:"job_id,omitempty"`
	OrgID             uuid.UUID `json:"org_id"`
	SessionID         uuid.UUID `json:"session_id"`
	RepositoryID      uuid.UUID `json:"repository_id"`
	WorkspaceRevision int64     `json:"workspace_revision"`
	ConfigDigest      string    `json:"config_digest,omitempty"`
	Phase             string    `json:"phase,omitempty"`
}

type SessionPreviewWarmBuildJobPayload struct {
	JobID             uuid.UUID `json:"job_id,omitempty"`
	OrgID             uuid.UUID `json:"org_id"`
	UserID            uuid.UUID `json:"user_id,omitempty"`
	SessionID         uuid.UUID `json:"session_id"`
	RepositoryID      uuid.UUID `json:"repository_id"`
	WorkspaceRevision int64     `json:"workspace_revision"`
	ConfigDigest      string    `json:"config_digest,omitempty"`
	Reason            string    `json:"reason,omitempty"`
}

// RemoteStopActivePreviewForSessionRequest targets preview teardown by session.
type RemoteStopActivePreviewForSessionRequest struct {
	OrgID     uuid.UUID `json:"org_id"`
	SessionID uuid.UUID `json:"session_id"`
}

type RemoteRecyclePreviewRequest struct {
	Config     *models.PreviewConfig `json:"config,omitempty"`
	ResumeWarm bool                  `json:"resume_warm,omitempty"`
}

type RemoteCancelSessionRequest struct {
	OrgID     uuid.UUID `json:"org_id"`
	SessionID uuid.UUID `json:"session_id"`
}

type RemoteCancelSessionResponse struct {
	Accepted bool `json:"accepted"`
}

// RemoteInspectElementRequest targets DOM inspection by coordinates.
type RemoteInspectElementRequest struct {
	X        int    `json:"x"`
	Y        int    `json:"y"`
	Selector string `json:"selector,omitempty"`
}

// RemoteExecuteInteractionRequest runs browser interactions against a preview.
type RemoteExecuteInteractionRequest struct {
	Steps []models.InteractionStep `json:"steps"`
}

// RemoteComputeVisualDiffRequest computes a visual diff between two snapshots.
type RemoteComputeVisualDiffRequest struct {
	BeforeSnapshotID string `json:"before_snapshot_id"`
	AfterSnapshotID  string `json:"after_snapshot_id"`
}

// RemoteRunAssertionsRequest runs preview assertions.
type RemoteRunAssertionsRequest struct {
	Assertions []Assertion `json:"assertions"`
}

// RemoteStopActivePreviewForSessionResponse reports whether a preview was stopped.
type RemoteStopActivePreviewForSessionResponse struct {
	Stopped bool `json:"stopped"`
}

// WorkerPreviewClient is the signed app->worker preview control-plane client.
type WorkerPreviewClient struct {
	httpClient *http.Client
	keyring    auth.PreviewTokenKeyring
}

// NewWorkerPreviewClient creates a worker preview client.
func NewWorkerPreviewClient(secret string) *WorkerPreviewClient {
	keyring, err := auth.NewPreviewTokenKeyring([]string{secret})
	if err != nil {
		keyring = auth.PreviewTokenKeyring{}
	}
	return NewWorkerPreviewClientWithKeyring(keyring)
}

// NewWorkerPreviewClientWithKeyring creates a worker preview client backed by
// the ordered preview RPC keyring.
func NewWorkerPreviewClientWithKeyring(keyring auth.PreviewTokenKeyring) *WorkerPreviewClient {
	return &WorkerPreviewClient{
		keyring: keyring,
		httpClient: &http.Client{
			Timeout: previewWorkerHTTPTimeout,
		},
	}
}

func (c *WorkerPreviewClient) newRequest(
	ctx context.Context,
	method, url string,
	claims auth.PreviewTokenClaims,
	body any,
) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	token, err := c.keyring.Generate(claims)
	if err != nil {
		return nil, fmt.Errorf("sign preview token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return req, nil
}

func decodeWorkerResponse[T any](resp *http.Response) (*T, error) {
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		var payload models.ErrorResponse
		if err := json.Unmarshal(body, &payload); err == nil && payload.Error.Code != "" {
			return nil, &WorkerRequestError{
				StatusCode: resp.StatusCode,
				Code:       payload.Error.Code,
				Message:    payload.Error.Message,
			}
		}
		return nil, &WorkerRequestError{
			StatusCode: resp.StatusCode,
			Message:    strings.TrimSpace(string(body)),
		}
	}
	var payload models.SingleResponse[T]
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &payload.Data, nil
}

func decodeWorkerListResponse[T any](resp *http.Response) ([]T, error) {
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		var payload models.ErrorResponse
		if err := json.Unmarshal(body, &payload); err == nil && payload.Error.Code != "" {
			return nil, &WorkerRequestError{
				StatusCode: resp.StatusCode,
				Code:       payload.Error.Code,
				Message:    payload.Error.Message,
			}
		}
		return nil, &WorkerRequestError{
			StatusCode: resp.StatusCode,
			Message:    strings.TrimSpace(string(body)),
		}
	}
	var payload models.ListResponse[T]
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return payload.Data, nil
}

// AsWorkerRequestError unwraps a worker client error.
func AsWorkerRequestError(err error) (*WorkerRequestError, bool) {
	var target *WorkerRequestError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

func (c *WorkerPreviewClient) StartPreview(ctx context.Context, worker WorkerNode, reqBody RemoteStartPreviewRequest) (*models.PreviewInstance, error) {
	req, err := c.newRequest(ctx, http.MethodPost, worker.BaseURL+"/internal/preview/start", auth.PreviewTokenClaims{
		OrgID:        reqBody.OrgID,
		TargetNodeID: worker.ID,
		SessionID:    &reqBody.SessionID,
		Action:       "start",
		ExpiresAt:    time.Now().Add(previewWorkerTokenTTL),
	}, reqBody)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("start preview: %w", err)
	}
	return decodeWorkerResponse[models.PreviewInstance](resp)
}

func (c *WorkerPreviewClient) StopPreview(ctx context.Context, worker WorkerNode, orgID, previewID uuid.UUID) error {
	req, err := c.newRequest(ctx, http.MethodPost, fmt.Sprintf("%s/internal/preview/%s/stop", worker.BaseURL, previewID), auth.PreviewTokenClaims{
		OrgID:        orgID,
		TargetNodeID: worker.ID,
		PreviewID:    &previewID,
		Action:       "stop",
		ExpiresAt:    time.Now().Add(previewWorkerTokenTTL),
	}, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("stop preview: %w", err)
	}
	_, err = decodeWorkerResponse[map[string]string](resp)
	return err
}

func (c *WorkerPreviewClient) RecyclePreview(ctx context.Context, worker WorkerNode, orgID, previewID uuid.UUID, cfg *models.PreviewConfig) error {
	return c.recyclePreview(ctx, worker, orgID, previewID, RemoteRecyclePreviewRequest{Config: cfg})
}

func (c *WorkerPreviewClient) ResumeWarmPreview(ctx context.Context, worker WorkerNode, orgID, previewID uuid.UUID) error {
	return c.recyclePreview(ctx, worker, orgID, previewID, RemoteRecyclePreviewRequest{ResumeWarm: true})
}

func (c *WorkerPreviewClient) SoftRestartPreview(ctx context.Context, worker WorkerNode, orgID, previewID uuid.UUID) error {
	req, err := c.newRequest(ctx, http.MethodPost, fmt.Sprintf("%s/internal/preview/%s/soft-restart", worker.BaseURL, previewID), auth.PreviewTokenClaims{
		OrgID:        orgID,
		TargetNodeID: worker.ID,
		PreviewID:    &previewID,
		Action:       "soft_restart",
		ExpiresAt:    time.Now().Add(previewWorkerTokenTTL),
	}, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("soft restart preview: %w", err)
	}
	_, err = decodeWorkerResponse[map[string]string](resp)
	return err
}

func (c *WorkerPreviewClient) recyclePreview(ctx context.Context, worker WorkerNode, orgID, previewID uuid.UUID, body RemoteRecyclePreviewRequest) error {
	req, err := c.newRequest(ctx, http.MethodPost, fmt.Sprintf("%s/internal/preview/%s/recycle", worker.BaseURL, previewID), auth.PreviewTokenClaims{
		OrgID:        orgID,
		TargetNodeID: worker.ID,
		PreviewID:    &previewID,
		Action:       "recycle",
		ExpiresAt:    time.Now().Add(previewWorkerTokenTTL),
	}, body)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("recycle preview: %w", err)
	}
	_, err = decodeWorkerResponse[map[string]string](resp)
	return err
}

func (c *WorkerPreviewClient) CaptureScreenshot(ctx context.Context, worker WorkerNode, orgID, previewID uuid.UUID, opts models.ScreenshotOpts) (*models.ScreenshotResult, error) {
	req, err := c.newRequest(ctx, http.MethodPost, fmt.Sprintf("%s/internal/preview/%s/screenshot", worker.BaseURL, previewID), auth.PreviewTokenClaims{
		OrgID:        orgID,
		TargetNodeID: worker.ID,
		PreviewID:    &previewID,
		Action:       "screenshot",
		ExpiresAt:    time.Now().Add(previewWorkerTokenTTL),
	}, opts)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("capture screenshot: %w", err)
	}
	return decodeWorkerResponse[models.ScreenshotResult](resp)
}

func (c *WorkerPreviewClient) InspectElement(ctx context.Context, worker WorkerNode, orgID, previewID uuid.UUID, x, y int) (*models.ElementInfo, error) {
	return c.inspectElement(ctx, worker, orgID, previewID, RemoteInspectElementRequest{X: x, Y: y})
}

func (c *WorkerPreviewClient) InspectElementBySelector(ctx context.Context, worker WorkerNode, orgID, previewID uuid.UUID, selector string) (*models.ElementInfo, error) {
	return c.inspectElement(ctx, worker, orgID, previewID, RemoteInspectElementRequest{Selector: selector})
}

func (c *WorkerPreviewClient) inspectElement(ctx context.Context, worker WorkerNode, orgID, previewID uuid.UUID, body RemoteInspectElementRequest) (*models.ElementInfo, error) {
	req, err := c.newRequest(ctx, http.MethodPost, fmt.Sprintf("%s/internal/preview/%s/inspect", worker.BaseURL, previewID), auth.PreviewTokenClaims{
		OrgID:        orgID,
		TargetNodeID: worker.ID,
		PreviewID:    &previewID,
		Action:       "inspect",
		ExpiresAt:    time.Now().Add(previewWorkerTokenTTL),
	}, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("inspect element: %w", err)
	}
	return decodeWorkerResponse[models.ElementInfo](resp)
}

func (c *WorkerPreviewClient) ReadConsole(ctx context.Context, worker WorkerNode, orgID, previewID uuid.UUID) ([]ConsoleMessage, error) {
	req, err := c.newRequest(ctx, http.MethodGet, fmt.Sprintf("%s/internal/preview/%s/console", worker.BaseURL, previewID), auth.PreviewTokenClaims{
		OrgID:        orgID,
		TargetNodeID: worker.ID,
		PreviewID:    &previewID,
		Action:       "console",
		ExpiresAt:    time.Now().Add(previewWorkerTokenTTL),
	}, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read console: %w", err)
	}
	return decodeWorkerListResponse[ConsoleMessage](resp)
}

func (c *WorkerPreviewClient) ExecuteInteraction(ctx context.Context, worker WorkerNode, orgID, previewID uuid.UUID, steps []models.InteractionStep) (*models.InteractionResult, error) {
	req, err := c.newRequest(ctx, http.MethodPost, fmt.Sprintf("%s/internal/preview/%s/interact", worker.BaseURL, previewID), auth.PreviewTokenClaims{
		OrgID:        orgID,
		TargetNodeID: worker.ID,
		PreviewID:    &previewID,
		Action:       "interact",
		ExpiresAt:    time.Now().Add(previewWorkerTokenTTL),
	}, RemoteExecuteInteractionRequest{Steps: steps})
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute interaction: %w", err)
	}
	return decodeWorkerResponse[models.InteractionResult](resp)
}

func (c *WorkerPreviewClient) CaptureMultiViewport(ctx context.Context, worker WorkerNode, orgID, previewID uuid.UUID, opts models.MultiViewportOpts) (*models.MultiViewportResult, error) {
	req, err := c.newRequest(ctx, http.MethodPost, fmt.Sprintf("%s/internal/preview/%s/multi-viewport", worker.BaseURL, previewID), auth.PreviewTokenClaims{
		OrgID:        orgID,
		TargetNodeID: worker.ID,
		PreviewID:    &previewID,
		Action:       "multi_viewport",
		ExpiresAt:    time.Now().Add(previewWorkerTokenTTL),
	}, opts)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("capture multi viewport: %w", err)
	}
	return decodeWorkerResponse[models.MultiViewportResult](resp)
}

func (c *WorkerPreviewClient) ComputeVisualDiff(ctx context.Context, worker WorkerNode, orgID, previewID uuid.UUID, beforeSnapshotID, afterSnapshotID string) (*models.VisualDiff, error) {
	req, err := c.newRequest(ctx, http.MethodPost, fmt.Sprintf("%s/internal/preview/%s/visual-diff", worker.BaseURL, previewID), auth.PreviewTokenClaims{
		OrgID:        orgID,
		TargetNodeID: worker.ID,
		PreviewID:    &previewID,
		Action:       "visual_diff",
		ExpiresAt:    time.Now().Add(previewWorkerTokenTTL),
	}, RemoteComputeVisualDiffRequest{BeforeSnapshotID: beforeSnapshotID, AfterSnapshotID: afterSnapshotID})
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("compute visual diff: %w", err)
	}
	return decodeWorkerResponse[models.VisualDiff](resp)
}

func (c *WorkerPreviewClient) RunAssertions(ctx context.Context, worker WorkerNode, orgID, previewID uuid.UUID, assertions []Assertion) (*AssertionResult, error) {
	req, err := c.newRequest(ctx, http.MethodPost, fmt.Sprintf("%s/internal/preview/%s/assert", worker.BaseURL, previewID), auth.PreviewTokenClaims{
		OrgID:        orgID,
		TargetNodeID: worker.ID,
		PreviewID:    &previewID,
		Action:       "assert",
		ExpiresAt:    time.Now().Add(previewWorkerTokenTTL),
	}, RemoteRunAssertionsRequest{Assertions: assertions})
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("run assertions: %w", err)
	}
	return decodeWorkerResponse[AssertionResult](resp)
}

func (c *WorkerPreviewClient) StopActivePreviewForSession(ctx context.Context, worker WorkerNode, orgID, sessionID uuid.UUID) (bool, error) {
	req, err := c.newRequest(ctx, http.MethodPost, worker.BaseURL+"/internal/preview/stop-session", auth.PreviewTokenClaims{
		OrgID:        orgID,
		TargetNodeID: worker.ID,
		SessionID:    &sessionID,
		Action:       "stop_session",
		ExpiresAt:    time.Now().Add(previewWorkerTokenTTL),
	}, RemoteStopActivePreviewForSessionRequest{
		OrgID:     orgID,
		SessionID: sessionID,
	})
	if err != nil {
		return false, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("stop active preview for session: %w", err)
	}
	result, err := decodeWorkerResponse[RemoteStopActivePreviewForSessionResponse](resp)
	if err != nil {
		return false, err
	}
	return result.Stopped, nil
}

func (c *WorkerPreviewClient) CancelSession(ctx context.Context, worker WorkerNode, reqBody RemoteCancelSessionRequest) (*RemoteCancelSessionResponse, error) {
	sessionID := reqBody.SessionID
	req, err := c.newRequest(ctx, http.MethodPost, fmt.Sprintf("%s/internal/sessions/%s/cancel", worker.BaseURL, sessionID), auth.PreviewTokenClaims{
		OrgID:        reqBody.OrgID,
		TargetNodeID: worker.ID,
		SessionID:    &sessionID,
		Action:       "cancel_session",
		ExpiresAt:    time.Now().Add(previewWorkerTokenTTL),
	}, reqBody)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cancel session on worker: %w", err)
	}
	return decodeWorkerResponse[RemoteCancelSessionResponse](resp)
}
