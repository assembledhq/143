package preview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestWorkerPreviewClient_SendsSignedRequestsAndDecodesResponses(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	previewID := uuid.New()
	worker := WorkerNode{ID: "worker-1", BaseURL: "", Mode: "worker"}
	secret := "worker-secret"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		claims, err := auth.ValidatePreviewToken(secret, token)
		require.NoError(t, err, "worker requests should carry a valid preview token")
		require.Equal(t, orgID, claims.OrgID, "worker requests should preserve the org scope")
		require.Equal(t, worker.ID, claims.TargetNodeID, "worker requests should preserve the target node")

		switch r.URL.Path {
		case "/internal/preview/start":
			require.Equal(t, http.MethodPost, r.Method, "StartPreview should POST to the worker")
			require.Equal(t, "start", claims.Action, "StartPreview should sign the start action")
			var body RemoteStartPreviewRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body), "StartPreview should encode its request body")
			require.Equal(t, sessionID, body.SessionID, "StartPreview should preserve the session id")
			w.WriteHeader(http.StatusCreated)
			require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[*models.PreviewInstance]{
				Data: &models.PreviewInstance{ID: previewID, SessionID: sessionID, OrgID: orgID, UserID: userID, WorkerNodeID: worker.ID},
			}), "StartPreview should return the created preview")
		case "/internal/preview/" + previewID.String() + "/stop":
			require.Equal(t, "stop", claims.Action, "StopPreview should sign the stop action")
			require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[map[string]string]{Data: map[string]string{"status": "stopped"}}), "StopPreview should decode a status response")
		case "/internal/preview/" + previewID.String() + "/recycle":
			require.Equal(t, "recycle", claims.Action, "RecyclePreview should sign the recycle action")
			require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[map[string]string]{Data: map[string]string{"status": "restarting"}}), "RecyclePreview should decode a status response")
		case "/internal/preview/" + previewID.String() + "/screenshot":
			require.Equal(t, "screenshot", claims.Action, "CaptureScreenshot should sign the screenshot action")
			require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[*models.ScreenshotResult]{
				Data: &models.ScreenshotResult{PageTitle: "Preview Title", URL: "http://preview.local"},
			}), "CaptureScreenshot should decode a screenshot response")
		case "/internal/preview/" + previewID.String() + "/inspect":
			require.Equal(t, "inspect", claims.Action, "InspectElement should sign the inspect action")
			require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[*models.ElementInfo]{
				Data: &models.ElementInfo{TagName: "div", DOMPath: "#app"},
			}), "InspectElement should decode an element response")
		case "/internal/preview/" + previewID.String() + "/console":
			require.Equal(t, http.MethodGet, r.Method, "ReadConsole should use GET")
			require.Equal(t, "console", claims.Action, "ReadConsole should sign the console action")
			require.NoError(t, json.NewEncoder(w).Encode(models.ListResponse[ConsoleMessage]{
				Data: []ConsoleMessage{{Level: "info", Text: "ready"}},
			}), "ReadConsole should decode a list response")
		case "/internal/preview/" + previewID.String() + "/interact":
			require.Equal(t, "interact", claims.Action, "ExecuteInteraction should sign the interact action")
			require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[*models.InteractionResult]{
				Data: &models.InteractionResult{
					FinalURL: "http://preview.local/final",
					Steps:    []models.StepResult{{StepIndex: 0, Action: "click", Success: true}},
				},
			}), "ExecuteInteraction should decode an interaction response")
		case "/internal/preview/" + previewID.String() + "/multi-viewport":
			require.Equal(t, "multi_viewport", claims.Action, "CaptureMultiViewport should sign the multi_viewport action")
			require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[*models.MultiViewportResult]{
				Data: &models.MultiViewportResult{},
			}), "CaptureMultiViewport should decode a multi-viewport response")
		case "/internal/preview/" + previewID.String() + "/visual-diff":
			require.Equal(t, "visual_diff", claims.Action, "ComputeVisualDiff should sign the visual_diff action")
			require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[*models.VisualDiff]{
				Data: &models.VisualDiff{PixelDiffPercent: 1.5, Summary: "changed"},
			}), "ComputeVisualDiff should decode a diff response")
		case "/internal/preview/" + previewID.String() + "/assert":
			require.Equal(t, "assert", claims.Action, "RunAssertions should sign the assert action")
			require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[*AssertionResult]{
				Data: &AssertionResult{
					Passed: 1,
					Failed: 0,
					Total:  1,
					Results: []AssertionCheck{{
						Passed:  true,
						Message: "ok",
					}},
				},
			}), "RunAssertions should decode an assertions response")
		case "/internal/preview/stop-session":
			require.Equal(t, "stop_session", claims.Action, "StopActivePreviewForSession should sign the stop_session action")
			require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[*RemoteStopActivePreviewForSessionResponse]{
				Data: &RemoteStopActivePreviewForSessionResponse{Stopped: true},
			}), "StopActivePreviewForSession should decode the stop result")
		case "/internal/sessions/" + sessionID.String() + "/cancel":
			require.Equal(t, "cancel_session", claims.Action, "CancelSession should sign the cancel_session action")
			var body RemoteCancelSessionRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body), "CancelSession should encode its request body")
			require.Equal(t, sessionID, body.SessionID, "CancelSession should preserve the session id")
			require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[*RemoteCancelSessionResponse]{
				Data: &RemoteCancelSessionResponse{Accepted: true},
			}), "CancelSession should decode the cancel result")
		default:
			t.Fatalf("unexpected worker path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	worker.BaseURL = server.URL

	client := NewWorkerPreviewClient(secret)
	client.httpClient.Timeout = 5 * time.Second

	started, err := client.StartPreview(context.Background(), worker, RemoteStartPreviewRequest{OrgID: orgID, UserID: userID, SessionID: sessionID})
	require.NoError(t, err, "StartPreview should succeed")
	require.Equal(t, previewID, started.ID, "StartPreview should decode the preview instance")

	err = client.StopPreview(context.Background(), worker, orgID, previewID)
	require.NoError(t, err, "StopPreview should succeed")

	err = client.RecyclePreview(context.Background(), worker, orgID, previewID, nil)
	require.NoError(t, err, "RecyclePreview should succeed")

	screenshot, err := client.CaptureScreenshot(context.Background(), worker, orgID, previewID, models.ScreenshotOpts{})
	require.NoError(t, err, "CaptureScreenshot should succeed")
	require.Equal(t, "Preview Title", screenshot.PageTitle, "CaptureScreenshot should decode the screenshot result")

	element, err := client.InspectElement(context.Background(), worker, orgID, previewID, 10, 20)
	require.NoError(t, err, "InspectElement should succeed")
	require.Equal(t, "#app", element.DOMPath, "InspectElement should decode the element response")

	consoleMessages, err := client.ReadConsole(context.Background(), worker, orgID, previewID)
	require.NoError(t, err, "ReadConsole should succeed")
	require.Len(t, consoleMessages, 1, "ReadConsole should decode console output")

	interaction, err := client.ExecuteInteraction(context.Background(), worker, orgID, previewID, []models.InteractionStep{{Action: "click"}})
	require.NoError(t, err, "ExecuteInteraction should succeed")
	require.Equal(t, "http://preview.local/final", interaction.FinalURL, "ExecuteInteraction should decode the interaction response")

	multiViewport, err := client.CaptureMultiViewport(context.Background(), worker, orgID, previewID, models.MultiViewportOpts{})
	require.NoError(t, err, "CaptureMultiViewport should succeed")
	require.NotNil(t, multiViewport, "CaptureMultiViewport should decode the response")

	visualDiff, err := client.ComputeVisualDiff(context.Background(), worker, orgID, previewID, "before", "after")
	require.NoError(t, err, "ComputeVisualDiff should succeed")
	require.Equal(t, 1.5, visualDiff.PixelDiffPercent, "ComputeVisualDiff should decode the diff response")

	assertions, err := client.RunAssertions(context.Background(), worker, orgID, previewID, []Assertion{{Type: "text"}})
	require.NoError(t, err, "RunAssertions should succeed")
	require.Equal(t, 1, assertions.Passed, "RunAssertions should decode the assertions response")

	stopped, err := client.StopActivePreviewForSession(context.Background(), worker, orgID, sessionID)
	require.NoError(t, err, "StopActivePreviewForSession should succeed")
	require.True(t, stopped, "StopActivePreviewForSession should decode the stopped result")

	cancelled, err := client.CancelSession(context.Background(), worker, RemoteCancelSessionRequest{OrgID: orgID, SessionID: sessionID})
	require.NoError(t, err, "CancelSession should succeed")
	require.True(t, cancelled.Accepted, "CancelSession should decode the accepted result")
}

func TestWorkerPreviewClient_UsesPreviewTokenKeyringSigner(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	previewID := uuid.New()
	worker := WorkerNode{ID: "worker-1", BaseURL: "", Mode: "worker"}
	keyring, err := auth.NewPreviewTokenKeyring([]string{"new-secret", "old-secret"})
	require.NoError(t, err, "test keyring should be created")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		_, oldErr := auth.ValidatePreviewToken("old-secret", token)
		require.Error(t, oldErr, "worker client should sign with the first configured preview RPC secret")
		claims, validateErr := auth.ValidatePreviewToken("new-secret", token)
		require.NoError(t, validateErr, "worker client request should validate with the keyring signer")
		require.Equal(t, "stop", claims.Action, "StopPreview should sign the stop action")
		require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[map[string]string]{Data: map[string]string{"status": "stopped"}}), "StopPreview should decode a status response")
	}))
	defer server.Close()
	worker.BaseURL = server.URL

	client := NewWorkerPreviewClientWithKeyring(keyring)
	client.httpClient.Timeout = 5 * time.Second

	err = client.StopPreview(context.Background(), worker, orgID, previewID)
	require.NoError(t, err, "StopPreview should succeed with a keyring-backed client")
}

func TestWorkerPreviewClient_PropagatesStructuredWorkerErrors(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	worker := WorkerNode{ID: "worker-1", Mode: "worker"}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		require.NoError(t, json.NewEncoder(w).Encode(models.ErrorResponse{
			Error: models.ErrorDetail{Code: PreviewCapacityCode, Message: "all preview slots are in use"},
		}), "worker error responses should encode structured errors")
	}))
	defer server.Close()
	worker.BaseURL = server.URL

	client := NewWorkerPreviewClient("worker-secret")
	_, err := client.StartPreview(context.Background(), worker, RemoteStartPreviewRequest{
		OrgID: orgID, UserID: uuid.New(), SessionID: sessionID,
	})
	require.Error(t, err, "StartPreview should surface worker errors")

	reqErr, ok := AsWorkerRequestError(err)
	require.True(t, ok, "StartPreview should expose structured worker errors")
	require.Equal(t, http.StatusServiceUnavailable, reqErr.StatusCode, "worker error status should be preserved")
	require.Equal(t, PreviewCapacityCode, reqErr.Code, "worker error code should be preserved")
	require.Contains(t, reqErr.Error(), PreviewCapacityCode, "worker error string should include the code")
}

func TestWorkerPreviewClient_DecodeFailures(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	previewID := uuid.New()
	worker := WorkerNode{ID: "worker-1", BaseURL: "http://invalid-host", Mode: "worker"}

	client := NewWorkerPreviewClient("worker-secret")
	client.httpClient = &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.URL.Path, "/console") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       ioNopCloserString("not-json"),
					Header:     make(http.Header),
				}, nil
			}
			return nil, errors.New("network down")
		}),
	}

	_, err := client.ReadConsole(context.Background(), worker, orgID, previewID)
	require.Error(t, err, "ReadConsole should surface decode failures")
	require.Contains(t, err.Error(), "decode response", "ReadConsole should wrap decode failures")

	err = client.StopPreview(context.Background(), worker, orgID, previewID)
	require.Error(t, err, "StopPreview should surface transport failures")
	require.Contains(t, err.Error(), "stop preview", "StopPreview should wrap transport failures")
}

func TestWorkerRequestError_ErrorAndUnwrap(t *testing.T) {
	t.Parallel()

	err := &WorkerRequestError{StatusCode: http.StatusBadGateway}
	require.Equal(t, "worker preview request failed with 502", err.Error(), "Error should omit code details when no code is present")

	err = &WorkerRequestError{StatusCode: http.StatusBadGateway, Code: "PREVIEW_FAILED", Message: "worker unavailable"}
	require.Equal(t, "worker preview request failed with 502 (PREVIEW_FAILED): worker unavailable", err.Error(), "Error should include structured worker error details")

	unwrapped, ok := AsWorkerRequestError(err)
	require.True(t, ok, "AsWorkerRequestError should unwrap worker request errors")
	require.Equal(t, err, unwrapped, "AsWorkerRequestError should return the original worker request error")

	_, ok = AsWorkerRequestError(errors.New("plain error"))
	require.False(t, ok, "AsWorkerRequestError should ignore non-worker errors")
}

func TestWorkerPreviewClient_NewRequest_Validation(t *testing.T) {
	t.Parallel()

	client := NewWorkerPreviewClient("worker-secret")
	orgID := uuid.New()

	_, err := client.newRequest(context.Background(), http.MethodGet, "http://[::1", auth.PreviewTokenClaims{
		OrgID:        orgID,
		TargetNodeID: "worker-1",
		Action:       "console",
		ExpiresAt:    time.Now().Add(time.Minute),
	}, nil)
	require.Error(t, err, "newRequest should surface request construction errors")
	require.Contains(t, err.Error(), "build request", "newRequest should wrap request construction errors")

	_, err = client.newRequest(context.Background(), http.MethodPost, "http://worker/internal/preview/start", auth.PreviewTokenClaims{
		OrgID:        orgID,
		TargetNodeID: "worker-1",
		Action:       "start",
		ExpiresAt:    time.Now().Add(time.Minute),
	}, map[string]any{"bad": make(chan int)})
	require.Error(t, err, "newRequest should surface JSON encoding failures")
	require.Contains(t, err.Error(), "marshal request body", "newRequest should wrap JSON encoding failures")
}

func TestDecodeWorkerResponses_ErrorVariants(t *testing.T) {
	t.Parallel()

	t.Run("single response structured worker error", func(t *testing.T) {
		t.Parallel()

		resp := &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       ioNopCloserString(fmt.Sprintf(`{"error":{"code":%q,"message":"full"}}`, PreviewCapacityCode)),
		}

		_, err := decodeWorkerResponse[models.PreviewInstance](resp)
		require.Error(t, err, "decodeWorkerResponse should surface structured worker errors")

		reqErr, ok := AsWorkerRequestError(err)
		require.True(t, ok, "decodeWorkerResponse should return a worker request error")
		require.Equal(t, PreviewCapacityCode, reqErr.Code, "decodeWorkerResponse should preserve the worker error code")
	})

	t.Run("single response plain worker error", func(t *testing.T) {
		t.Parallel()

		resp := &http.Response{
			StatusCode: http.StatusBadGateway,
			Body:       ioNopCloserString("worker unavailable"),
		}

		_, err := decodeWorkerResponse[models.PreviewInstance](resp)
		require.Error(t, err, "decodeWorkerResponse should surface plain-text worker errors")

		reqErr, ok := AsWorkerRequestError(err)
		require.True(t, ok, "decodeWorkerResponse should return a worker request error")
		require.Equal(t, "worker unavailable", reqErr.Message, "decodeWorkerResponse should preserve the plain-text worker error message")
	})

	t.Run("single response decode failure", func(t *testing.T) {
		t.Parallel()

		resp := &http.Response{
			StatusCode: http.StatusOK,
			Body:       ioNopCloserString("not-json"),
		}

		_, err := decodeWorkerResponse[models.PreviewInstance](resp)
		require.Error(t, err, "decodeWorkerResponse should surface JSON decode failures")
		require.Contains(t, err.Error(), "decode response", "decodeWorkerResponse should wrap JSON decode failures")
	})

	t.Run("list response structured worker error", func(t *testing.T) {
		t.Parallel()

		resp := &http.Response{
			StatusCode: http.StatusForbidden,
			Body:       ioNopCloserString(`{"error":{"code":"WRONG_PREVIEW_WORKER","message":"wrong worker"}}`),
		}

		_, err := decodeWorkerListResponse[ConsoleMessage](resp)
		require.Error(t, err, "decodeWorkerListResponse should surface structured worker errors")

		reqErr, ok := AsWorkerRequestError(err)
		require.True(t, ok, "decodeWorkerListResponse should return a worker request error")
		require.Equal(t, "WRONG_PREVIEW_WORKER", reqErr.Code, "decodeWorkerListResponse should preserve the worker error code")
	})

	t.Run("list response decode failure", func(t *testing.T) {
		t.Parallel()

		resp := &http.Response{
			StatusCode: http.StatusOK,
			Body:       ioNopCloserString("not-json"),
		}

		_, err := decodeWorkerListResponse[ConsoleMessage](resp)
		require.Error(t, err, "decodeWorkerListResponse should surface JSON decode failures")
		require.Contains(t, err.Error(), "decode response", "decodeWorkerListResponse should wrap JSON decode failures")
	})
}

func TestWorkerPreviewClient_MethodTransportFailures(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	previewID := uuid.New()
	worker := WorkerNode{ID: "worker-1", BaseURL: "http://worker.internal", Mode: "worker"}

	tests := []struct {
		name        string
		call        func(*WorkerPreviewClient) error
		wantMessage string
	}{
		{
			name: "start preview",
			call: func(client *WorkerPreviewClient) error {
				_, err := client.StartPreview(context.Background(), worker, RemoteStartPreviewRequest{OrgID: orgID, UserID: uuid.New(), SessionID: sessionID})
				return err
			},
			wantMessage: "start preview",
		},
		{
			name: "stop preview",
			call: func(client *WorkerPreviewClient) error {
				return client.StopPreview(context.Background(), worker, orgID, previewID)
			},
			wantMessage: "stop preview",
		},
		{
			name: "recycle preview",
			call: func(client *WorkerPreviewClient) error {
				return client.RecyclePreview(context.Background(), worker, orgID, previewID, nil)
			},
			wantMessage: "recycle preview",
		},
		{
			name: "capture screenshot",
			call: func(client *WorkerPreviewClient) error {
				_, err := client.CaptureScreenshot(context.Background(), worker, orgID, previewID, models.ScreenshotOpts{})
				return err
			},
			wantMessage: "capture screenshot",
		},
		{
			name: "inspect element",
			call: func(client *WorkerPreviewClient) error {
				_, err := client.InspectElement(context.Background(), worker, orgID, previewID, 1, 2)
				return err
			},
			wantMessage: "inspect element",
		},
		{
			name: "read console",
			call: func(client *WorkerPreviewClient) error {
				_, err := client.ReadConsole(context.Background(), worker, orgID, previewID)
				return err
			},
			wantMessage: "read console",
		},
		{
			name: "execute interaction",
			call: func(client *WorkerPreviewClient) error {
				_, err := client.ExecuteInteraction(context.Background(), worker, orgID, previewID, []models.InteractionStep{{Action: "click"}})
				return err
			},
			wantMessage: "execute interaction",
		},
		{
			name: "capture multi viewport",
			call: func(client *WorkerPreviewClient) error {
				_, err := client.CaptureMultiViewport(context.Background(), worker, orgID, previewID, models.MultiViewportOpts{})
				return err
			},
			wantMessage: "capture multi viewport",
		},
		{
			name: "compute visual diff",
			call: func(client *WorkerPreviewClient) error {
				_, err := client.ComputeVisualDiff(context.Background(), worker, orgID, previewID, "before", "after")
				return err
			},
			wantMessage: "compute visual diff",
		},
		{
			name: "run assertions",
			call: func(client *WorkerPreviewClient) error {
				_, err := client.RunAssertions(context.Background(), worker, orgID, previewID, []Assertion{{Type: "text"}})
				return err
			},
			wantMessage: "run assertions",
		},
		{
			name: "stop active preview for session",
			call: func(client *WorkerPreviewClient) error {
				_, err := client.StopActivePreviewForSession(context.Background(), worker, orgID, sessionID)
				return err
			},
			wantMessage: "stop active preview for session",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := NewWorkerPreviewClient("worker-secret")
			client.httpClient = &http.Client{
				Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
					return nil, errors.New("network down")
				}),
			}

			err := tt.call(client)
			require.Error(t, err, "worker client methods should surface transport failures")
			require.Contains(t, err.Error(), tt.wantMessage, "worker client methods should wrap transport failures with method-specific context")
		})
	}
}

func TestWorkerPreviewClient_StopActivePreviewForSession_DecodeFailure(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	worker := WorkerNode{ID: "worker-1", BaseURL: "http://worker.internal", Mode: "worker"}

	client := NewWorkerPreviewClient("worker-secret")
	client.httpClient = &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "/internal/preview/stop-session", req.URL.Path, "StopActivePreviewForSession should target the stop-session endpoint")
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       ioNopCloserString(`{"data":`),
				Header:     make(http.Header),
			}, nil
		}),
	}

	_, err := client.StopActivePreviewForSession(context.Background(), worker, orgID, sessionID)
	require.Error(t, err, "StopActivePreviewForSession should surface JSON decode failures")
	require.Contains(t, err.Error(), "decode response", "StopActivePreviewForSession should wrap JSON decode failures")
}

func TestWorkerPreviewClient_InvalidWorkerBaseURLFailsBuild(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	previewID := uuid.New()
	worker := WorkerNode{ID: "worker-1", BaseURL: "http://[::1", Mode: "worker"}

	client := NewWorkerPreviewClient("worker-secret")

	_, err := client.CaptureScreenshot(context.Background(), worker, orgID, previewID, models.ScreenshotOpts{})
	require.Error(t, err, "worker client methods should surface request build failures")
	require.Contains(t, err.Error(), "build request", "worker client methods should wrap request build failures")
}

func TestWorkerPreviewClient_ErrorContainsMethodSpecificContext(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("wrap: %w", &WorkerRequestError{StatusCode: http.StatusConflict, Code: "NO_SANDBOX", Message: "missing"})
	reqErr, ok := AsWorkerRequestError(err)
	require.True(t, ok, "AsWorkerRequestError should unwrap worker errors through wrapped error chains")
	require.Equal(t, "NO_SANDBOX", reqErr.Code, "AsWorkerRequestError should preserve the wrapped code")
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type stringReadCloser struct {
	*strings.Reader
}

func (r stringReadCloser) Close() error { return nil }

func ioNopCloserString(v string) stringReadCloser {
	return stringReadCloser{Reader: strings.NewReader(v)}
}
