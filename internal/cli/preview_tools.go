package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/services/mcp"
)

// previewToolExecutor implements the platform preview tools as thin
// client-side wrappers over /api/v1. Session-preview actions target
// /sessions/{id}/preview..., while branch preview lifecycle keeps using
// /previews*. The executor never constructs worker URLs or preview RPC tokens.
type previewToolExecutor struct {
	client   *Client
	internal bool
}

func (e *previewToolExecutor) handles(name string) bool {
	switch name {
	case "preview_create", "preview_ensure", "preview_status", "preview_list", "preview_stop",
		"preview_restart", "preview_update", "preview_screenshot", "preview_console",
		"preview_inspect", "preview_interact", "preview_multi_viewport",
		"preview_visual_diff", "preview_assert", "preview_observe", "preview_act",
		"preview_control", "preview_request_handoff":
		return true
	}
	return false
}

func (e *previewToolExecutor) tools() []mcp.Tool {
	return []mcp.Tool{
		{
			Name:        "preview_ensure",
			Description: "Idempotently create or resume the current session preview. Defaults to 143_SESSION_ID.",
			InputSchema: mcp.ToolSchema{Type: "object", Properties: map[string]mcp.SchemaProperty{
				"session_id": {Type: "string", Description: "Session ID; defaults to 143_SESSION_ID"},
				"wait":       {Type: "boolean", Description: "Wait until the preview is ready or fails"},
			}},
		},
		{
			Name:        "preview_create",
			Description: "Create or reuse a preview. Prefer --session-id for agent visual iteration against the live session workspace; use --repository and --branch for pushed branch previews.",
			InputSchema: mcp.ToolSchema{
				Type: "object",
				Properties: map[string]mcp.SchemaProperty{
					"session_id": {Type: "string", Description: "Session ID whose active session preview should be created or reused"},
					"repository": {Type: "string", Description: `Repository name, e.g. "acme/webapp" or just "webapp"`},
					"branch":     {Type: "string", Description: "Branch name (must exist on the remote)"},
					"wait":       {Type: "boolean", Description: "Wait until the preview is ready or fails"},
				},
			},
		},
		{
			Name:        "preview_status",
			Description: "Get preview status. Accepts --session-id for the active session preview or --preview-id for a branch preview.",
			InputSchema: mcp.ToolSchema{
				Type: "object",
				Properties: map[string]mcp.SchemaProperty{
					"session_id": {Type: "string", Description: "Session ID whose active preview should be checked"},
					"preview_id": {Type: "string", Description: "Preview ID returned by preview_create"},
				},
			},
		},
		{
			Name:        "preview_list",
			Description: "List previews. With --session-id, returns that session's active preview; otherwise lists branch preview environments.",
			InputSchema: mcp.ToolSchema{
				Type: "object",
				Properties: map[string]mcp.SchemaProperty{
					"session_id": {Type: "string", Description: "Optional session ID whose active preview should be listed"},
				},
			},
		},
		{
			Name:        "preview_stop",
			Description: "Stop a running preview. Accepts --session-id for the active session preview or --preview-id for a branch preview.",
			InputSchema: mcp.ToolSchema{
				Type: "object",
				Properties: map[string]mcp.SchemaProperty{
					"session_id": {Type: "string", Description: "Session ID whose active preview should be stopped"},
					"preview_id": {Type: "string", Description: "The preview ID to stop"},
				},
			},
		},
		{Name: "preview_restart", Description: "Force a full restart of a session preview.", InputSchema: previewSessionSchema(map[string]mcp.SchemaProperty{
			"wait": {Type: "boolean", Description: "Wait until restart reaches ready or failed"},
		})},
		{Name: "preview_update", Description: "Smart update after code edits; the platform selects browser reload, service restart, full recycle, cold relaunch, or noop.", InputSchema: previewSessionSchema(map[string]mcp.SchemaProperty{
			"path":           {Type: "string", Description: "Path to reload/check after update, default /"},
			"wait":           {Type: "boolean", Description: "Wait until ready or failed when a restart is started"},
			"force_mode":     {Type: "string", Description: "Diagnostic override", Enum: []string{"browser_reload", "soft_service_restart", "full_recycle", "cold_relaunch", "noop_current"}},
			"reload_browser": {Type: "boolean", Description: "Reload browser context when possible; defaults true"},
			"config":         {Type: "string", Description: "Optional preview config JSON object"},
		})},
		{Name: "preview_screenshot", Description: "Capture a preview screenshot.", InputSchema: previewTargetSchema(map[string]mcp.SchemaProperty{
			"path":          {Type: "string", Description: "Path to capture, default /"},
			"viewport_w":    {Type: "number", Description: "Viewport width, default 1280"},
			"viewport_h":    {Type: "number", Description: "Viewport height, default 720"},
			"full_page":     {Type: "boolean", Description: "Capture full page"},
			"delay_ms":      {Type: "number", Description: "Delay before capture in milliseconds"},
			"inline_base64": {Type: "boolean", Description: "Keep png_base64 in output; defaults true until artifact storage is available"},
		})},
		{Name: "preview_console", Description: "Read browser console messages from a preview.", InputSchema: previewTargetSchema(map[string]mcp.SchemaProperty{
			"level": {Type: "string", Description: "Optional console level filter, e.g. error"},
		})},
		{Name: "preview_inspect", Description: "Inspect a preview DOM element by selector or coordinates.", InputSchema: previewTargetSchema(map[string]mcp.SchemaProperty{
			"selector": {Type: "string", Description: "CSS selector to inspect"},
			"x":        {Type: "number", Description: "Viewport x coordinate"},
			"y":        {Type: "number", Description: "Viewport y coordinate"},
		})},
		{Name: "preview_interact", Description: "Execute browser interactions against a preview. Pass --steps as JSON array.", InputSchema: previewTargetSchema(map[string]mcp.SchemaProperty{
			"steps": {Type: "string", Description: `JSON array of interaction steps, e.g. [{"action":"click","selector":"[data-testid=save]"}]`},
		})},
		{Name: "preview_multi_viewport", Description: "Capture responsive screenshots for a preview.", InputSchema: previewTargetSchema(map[string]mcp.SchemaProperty{
			"path":      {Type: "string", Description: "Path to capture, default /"},
			"viewports": {Type: "string", Description: "Optional JSON array of {name,width,height}; defaults mobile/tablet/desktop"},
			"delay_ms":  {Type: "number", Description: "Delay before each capture in milliseconds"},
		})},
		{Name: "preview_visual_diff", Description: "Compare two stored preview snapshots.", InputSchema: previewTargetSchema(map[string]mcp.SchemaProperty{
			"before_snapshot_id": {Type: "string", Description: "Before artifact or snapshot ID"},
			"after_snapshot_id":  {Type: "string", Description: "After artifact or snapshot ID"},
		})},
		{Name: "preview_assert", Description: "Run browser/visual assertions against a preview. Pass --assertions as JSON array.", InputSchema: previewTargetSchema(map[string]mcp.SchemaProperty{
			"assertions": {Type: "string", Description: "JSON array of assertion objects"},
		})},
		{Name: "preview_observe", Description: "Capture a high-signal observation of the current session browser, including screenshot metadata and console errors.", InputSchema: previewTargetSchema(map[string]mcp.SchemaProperty{
			"path":               {Type: "string", Description: "Optional preview-origin path to observe"},
			"selector":           {Type: "string", Description: "Optional selector for a bounded DOM excerpt"},
			"include_dom":        {Type: "boolean", Description: "Include a bounded DOM excerpt"},
			"viewport_w":         {Type: "number", Description: "Viewport width"},
			"viewport_h":         {Type: "number", Description: "Viewport height"},
			"full_page":          {Type: "boolean", Description: "Capture the full page"},
			"max_semantic_bytes": {Type: "number", Description: "Bound semantic and DOM output size"},
			"inline_base64":      {Type: "boolean", Description: "Include screenshot bytes; defaults false"},
			"output":             {Type: "string", Description: "Write screenshot PNG to this workspace path"},
			"console_cursor":     {Type: "number", Description: "Return console messages after this cursor"},
		})},
		{Name: "preview_act", Description: "Execute structured actions in the shared session browser and return the resulting observation.", InputSchema: previewTargetSchema(map[string]mcp.SchemaProperty{
			"steps":              {Type: "string", Description: "JSON array of structured browser actions"},
			"selector":           {Type: "string", Description: "Optional selector for a bounded final DOM excerpt"},
			"include_dom":        {Type: "boolean", Description: "Include a bounded final DOM excerpt"},
			"viewport_w":         {Type: "number", Description: "Final observation viewport width"},
			"viewport_h":         {Type: "number", Description: "Final observation viewport height"},
			"max_semantic_bytes": {Type: "number", Description: "Bound semantic and DOM output size"},
			"inline_base64":      {Type: "boolean", Description: "Include screenshot bytes; defaults false"},
			"output":             {Type: "string", Description: "Write the final screenshot PNG to this workspace path"},
			"console_cursor":     {Type: "number", Description: "Return console messages after this cursor"},
		})},
		{Name: "preview_control", Description: "Read the shared session browser control state.", InputSchema: previewSessionSchema(nil)},
		{Name: "preview_request_handoff", Description: "Pause browser actions and request human control of the shared session browser.", InputSchema: previewSessionSchema(map[string]mcp.SchemaProperty{
			"reason": {Type: "string", Description: "Why human interaction is required"},
		})},
	}
}

func previewSessionSchema(extra map[string]mcp.SchemaProperty) mcp.ToolSchema {
	props := map[string]mcp.SchemaProperty{
		"session_id": {Type: "string", Description: "Session ID whose active preview should be targeted"},
	}
	for k, v := range extra {
		props[k] = v
	}
	return mcp.ToolSchema{Type: "object", Properties: props}
}

func previewTargetSchema(extra map[string]mcp.SchemaProperty) mcp.ToolSchema {
	props := map[string]mcp.SchemaProperty{
		"session_id": {Type: "string", Description: "Session ID whose active preview should be targeted"},
		"preview_id": {Type: "string", Description: "Preview ID to target directly"},
	}
	for k, v := range extra {
		props[k] = v
	}
	return mcp.ToolSchema{Type: "object", Properties: props}
}

func (e *previewToolExecutor) call(ctx context.Context, name string, args json.RawMessage) *mcp.ToolCallResult {
	if name != "preview_create" && name != "preview_list" && name != "preview_ensure" {
		args = previewArgsWithSessionDefault(args)
	}
	if e.internal {
		args = previewArgsWithInternalTarget(args)
	}
	switch name {
	case "preview_create":
		return e.create(ctx, args)
	case "preview_ensure":
		return e.ensure(ctx, args)
	case "preview_status":
		return e.status(ctx, args)
	case "preview_list":
		return e.list(ctx, args)
	case "preview_stop":
		return e.stop(ctx, args)
	case "preview_restart":
		return e.restart(ctx, args)
	case "preview_update":
		return e.update(ctx, args)
	case "preview_screenshot":
		return e.screenshot(ctx, args)
	case "preview_console":
		return e.console(ctx, args)
	case "preview_inspect":
		return e.inspect(ctx, args)
	case "preview_interact":
		return e.interact(ctx, args)
	case "preview_multi_viewport":
		return e.multiViewport(ctx, args)
	case "preview_visual_diff":
		return e.visualDiff(ctx, args)
	case "preview_assert":
		return e.assertions(ctx, args)
	case "preview_observe":
		return e.observe(ctx, args)
	case "preview_act":
		return e.act(ctx, args)
	case "preview_control":
		return e.control(ctx, args)
	case "preview_request_handoff":
		return e.requestHandoff(ctx, args)
	}
	return mcp.ErrorResult(fmt.Sprintf("unknown preview tool %q", name))
}

func (e *previewToolExecutor) control(ctx context.Context, args json.RawMessage) *mcp.ToolCallResult {
	var params map[string]any
	if err := json.Unmarshal(args, &params); err != nil {
		return mcp.ErrorResult("invalid preview control arguments")
	}
	target := previewTargetFromMap(params)
	basePath, err := target.basePath()
	if err != nil {
		return mcp.ErrorResult(err.Error())
	}
	var resp struct {
		Data any `json:"data"`
	}
	if err := e.client.Do(ctx, http.MethodGet, basePath+"/control", nil, &resp); err != nil {
		return mcp.ErrorResult(fmt.Sprintf("preview control failed: %s", err))
	}
	return jsonResult(resp.Data)
}

func (e *previewToolExecutor) requestHandoff(ctx context.Context, args json.RawMessage) *mcp.ToolCallResult {
	var params map[string]any
	if err := json.Unmarshal(args, &params); err != nil {
		return mcp.ErrorResult("invalid preview handoff arguments")
	}
	target := previewTargetFromMap(params)
	reason, _ := params["reason"].(string)
	if strings.TrimSpace(reason) == "" {
		return mcp.ErrorResult("reason is required")
	}
	return e.postPreviewTarget(ctx, target, "control/request-handoff", map[string]string{"reason": reason}, "preview handoff failed")
}

func previewArgsWithInternalTarget(args json.RawMessage) json.RawMessage {
	var params map[string]any
	if len(args) == 0 {
		params = make(map[string]any)
	} else if json.Unmarshal(args, &params) != nil {
		return args
	}
	params["__internal"] = true
	return mustJSON(params)
}

func previewArgsWithSessionDefault(args json.RawMessage) json.RawMessage {
	var params map[string]any
	if len(args) == 0 {
		params = make(map[string]any)
	} else if json.Unmarshal(args, &params) != nil {
		return args
	}
	sessionID, _ := params["session_id"].(string)
	previewID, _ := params["preview_id"].(string)
	if strings.TrimSpace(sessionID) == "" && strings.TrimSpace(previewID) == "" {
		if sessionID := strings.TrimSpace(os.Getenv("143_SESSION_ID")); sessionID != "" {
			params["session_id"] = sessionID
		}
	}
	return mustJSON(params)
}

// previewView is the slice of the server's branch-preview response surfaced
// to agents: enough to act on, nothing to parse around.
type previewView struct {
	PreviewID    string  `json:"preview_id"`
	Repository   string  `json:"repository,omitempty"`
	Branch       string  `json:"branch,omitempty"`
	Status       string  `json:"status"`
	PreviewURL   *string `json:"preview_url"`
	CurrentPhase string  `json:"current_phase,omitempty"`
	Error        string  `json:"error,omitempty"`
}

// branchPreviewWire mirrors the fields of the server's branch-preview
// response this executor consumes.
type branchPreviewWire struct {
	TargetID           string  `json:"target_id"`
	PreviewID          *string `json:"preview_id"`
	RepositoryFullName string  `json:"repository_full_name"`
	Branch             string  `json:"branch"`
	Status             string  `json:"status"`
	PreviewURL         *string `json:"preview_url"`
	CurrentPhase       string  `json:"current_phase"`
	Error              string  `json:"error"`
}

type ensurePreviewWire struct {
	Action     string                  `json:"action"`
	Instance   *sessionPreviewInstance `json:"instance"`
	PreviewURL string                  `json:"preview_url"`
}

type sessionPreviewInstance struct {
	ID           string `json:"id"`
	SessionID    string `json:"session_id"`
	Status       string `json:"status"`
	CurrentPhase string `json:"current_phase"`
	Error        string `json:"error"`
}

type sessionPreviewStatusWire struct {
	Instance              *sessionPreviewInstance `json:"instance"`
	PreviewOrigin         string                  `json:"preview_origin"`
	Freshness             any                     `json:"freshness,omitempty"`
	RecommendedUpdateMode string                  `json:"recommended_update_mode,omitempty"`
	Prewarm               any                     `json:"prewarm,omitempty"`
}

func (e *previewToolExecutor) sessionPreviewPath(sessionID string) string {
	if e.internal {
		return "/api/v1/internal/sessions/" + sessionID + "/preview"
	}
	return "/api/v1/sessions/" + sessionID + "/preview"
}

func (w ensurePreviewWire) view() previewView {
	if w.Instance == nil {
		return previewView{Status: w.Action}
	}
	var url *string
	if w.PreviewURL != "" {
		url = &w.PreviewURL
	}
	return previewView{
		PreviewID:    w.Instance.ID,
		Status:       w.Instance.Status,
		PreviewURL:   url,
		CurrentPhase: w.Instance.CurrentPhase,
		Error:        w.Instance.Error,
	}
}

func (w branchPreviewWire) view() previewView {
	id := w.TargetID
	if w.PreviewID != nil && *w.PreviewID != "" {
		id = *w.PreviewID
	}
	return previewView{
		PreviewID:    id,
		Repository:   w.RepositoryFullName,
		Branch:       w.Branch,
		Status:       w.Status,
		PreviewURL:   w.PreviewURL,
		CurrentPhase: w.CurrentPhase,
		Error:        w.Error,
	}
}

func (e *previewToolExecutor) sessionStatus(ctx context.Context, sessionID string) *mcp.ToolCallResult {
	var resp struct {
		Data sessionPreviewStatusWire `json:"data"`
	}
	if err := e.client.Do(ctx, http.MethodGet, e.sessionPreviewPath(sessionID), nil, &resp); err != nil {
		return mcp.ErrorResult(fmt.Sprintf("preview status failed: %s", err))
	}
	return jsonResult(resp.Data.view(sessionID))
}

func (w sessionPreviewStatusWire) view(sessionID string) map[string]any {
	result := map[string]any{"session_id": sessionID}
	if w.Instance != nil {
		result["preview_id"] = w.Instance.ID
		result["status"] = w.Instance.Status
		if w.Instance.CurrentPhase != "" {
			result["current_phase"] = w.Instance.CurrentPhase
		}
		if w.Instance.Error != "" {
			result["error"] = w.Instance.Error
		}
	}
	if w.PreviewOrigin != "" {
		result["preview_url"] = w.PreviewOrigin
	}
	if w.Freshness != nil {
		result["freshness"] = w.Freshness
	}
	if w.RecommendedUpdateMode != "" {
		result["recommended_update_mode"] = w.RecommendedUpdateMode
	}
	if w.Prewarm != nil {
		result["prewarm"] = w.Prewarm
	}
	return result
}

func (e *previewToolExecutor) waitSessionReady(ctx context.Context, sessionID string) *mcp.ToolCallResult {
	deadline := time.NewTimer(previewWaitTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		result := e.sessionStatus(ctx, sessionID)
		if result.IsError {
			return result
		}
		var status map[string]any
		if err := json.Unmarshal([]byte(firstText(result)), &status); err != nil {
			return result
		}
		state, _ := status["status"].(string)
		switch state {
		case "ready", "partially_ready", "running":
			return result
		case "failed", "stopped", "expired", "unavailable":
			return mcp.ErrorResult(fmt.Sprintf("preview %s: %v", state, status["error"]))
		}
		select {
		case <-ctx.Done():
			return mcp.ErrorResult(ctx.Err().Error())
		case <-deadline.C:
			return mcp.ErrorResult(fmt.Sprintf("timed out waiting for session preview %s", sessionID))
		case <-ticker.C:
		}
	}
}

func previewSessionAndWait(args json.RawMessage) (string, bool, error) {
	var params struct {
		SessionID string `json:"session_id"`
		Wait      bool   `json:"wait"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &params); err != nil {
			return "", false, fmt.Errorf("invalid arguments")
		}
	}
	if params.SessionID == "" {
		params.SessionID = strings.TrimSpace(os.Getenv("143_SESSION_ID"))
	}
	if params.SessionID == "" {
		return "", false, fmt.Errorf("session_id is required")
	}
	return params.SessionID, params.Wait, nil
}

type previewTarget struct {
	SessionID string
	PreviewID string
	Internal  bool
}

// validate enforces that exactly one target identifier is supplied. Silently
// preferring one when both are given is a footgun: an agent that passes a stale
// preview_id alongside the right session_id would act on the wrong surface
// without any signal. Make the ambiguity loud instead.
func (t previewTarget) validate() error {
	if t.SessionID != "" && t.PreviewID != "" {
		return fmt.Errorf("specify session_id or preview_id, not both")
	}
	if t.SessionID == "" && t.PreviewID == "" {
		return fmt.Errorf("session_id or preview_id is required")
	}
	if t.Internal && t.PreviewID != "" {
		return fmt.Errorf("sandbox preview tools may only target their current session")
	}
	return nil
}

func (t previewTarget) basePath() (string, error) {
	if err := t.validate(); err != nil {
		return "", err
	}
	if t.SessionID != "" {
		if t.Internal {
			return "/api/v1/internal/sessions/" + t.SessionID + "/preview", nil
		}
		return "/api/v1/sessions/" + t.SessionID + "/preview", nil
	}
	return "/api/v1/previews/" + t.PreviewID, nil
}

func previewTargetFromMap(params map[string]any) previewTarget {
	sessionID, _ := params["session_id"].(string)
	previewID, _ := params["preview_id"].(string)
	internal, _ := params["__internal"].(bool)
	target := previewTarget{SessionID: strings.TrimSpace(sessionID), PreviewID: strings.TrimSpace(previewID), Internal: internal}
	if target.SessionID == "" && target.PreviewID == "" {
		target.SessionID = strings.TrimSpace(os.Getenv("143_SESSION_ID"))
	}
	return target
}

func previewJSONPayload(args json.RawMessage, field string) (previewTarget, any, error) {
	var params map[string]any
	if err := json.Unmarshal(args, &params); err != nil {
		return previewTarget{}, nil, fmt.Errorf("invalid arguments")
	}
	target := previewTargetFromMap(params)
	if _, err := target.basePath(); err != nil {
		return previewTarget{}, nil, err
	}
	raw, _ := params[field].(string)
	if strings.TrimSpace(raw) == "" {
		return previewTarget{}, nil, fmt.Errorf("%s is required", field)
	}
	var payload any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return previewTarget{}, nil, fmt.Errorf("%s must be JSON: %w", field, err)
	}
	return target, payload, nil
}

func (e *previewToolExecutor) create(ctx context.Context, args json.RawMessage) *mcp.ToolCallResult {
	var params struct {
		SessionID  string `json:"session_id"`
		Repository string `json:"repository"`
		Branch     string `json:"branch"`
		Wait       bool   `json:"wait"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return mcp.ErrorResult("invalid preview create arguments")
	}
	hasBranchTarget := strings.TrimSpace(params.Repository) != "" || strings.TrimSpace(params.Branch) != ""
	if e.internal && params.SessionID == "" {
		params.SessionID = strings.TrimSpace(os.Getenv("143_SESSION_ID"))
	}
	if params.SessionID != "" {
		if hasBranchTarget {
			return mcp.ErrorResult("specify session_id or repository and branch, not both")
		}
		var resp struct {
			Data ensurePreviewWire `json:"data"`
		}
		if err := e.client.Do(ctx, http.MethodPost, e.sessionPreviewPath(params.SessionID)+"/ensure", nil, &resp); err != nil {
			return mcp.ErrorResult(fmt.Sprintf("preview create failed: %s", err))
		}
		if params.Wait {
			return e.waitSessionReady(ctx, params.SessionID)
		}
		return jsonResult(resp.Data.view())
	}
	if e.internal {
		return mcp.ErrorResult("sandbox preview tools require 143_SESSION_ID")
	}
	if params.Repository == "" || params.Branch == "" {
		return mcp.ErrorResult("session_id or repository and branch are required")
	}

	repoID, repoFullName, err := e.resolveRepository(ctx, params.Repository)
	if err != nil {
		return mcp.ErrorResult(err.Error())
	}

	// Branch-existence preflight: the #1 failure mode for local use is a
	// branch that only exists in the local working tree. A distinct error
	// naming the fix lets agents recover without guessing.
	if exists, checkErr := e.branchExists(ctx, repoID, params.Branch); checkErr == nil && !exists {
		return mcp.ErrorResult(fmt.Sprintf(
			"BRANCH_NOT_PUSHED: branch %q does not exist on the remote for %s — run `git push -u origin %s` first, then retry",
			params.Branch, repoFullName, params.Branch))
	}

	var resp struct {
		Data branchPreviewWire `json:"data"`
	}
	err = e.client.Do(ctx, http.MethodPost, "/api/v1/previews",
		map[string]string{"repository_id": repoID, "branch": params.Branch}, &resp)
	if err != nil {
		return mcp.ErrorResult(fmt.Sprintf("preview create failed: %s", err))
	}
	if params.Wait {
		return e.waitBranchReady(ctx, resp.Data.view().PreviewID)
	}
	return jsonResult(resp.Data.view())
}

func (e *previewToolExecutor) ensure(ctx context.Context, args json.RawMessage) *mcp.ToolCallResult {
	sessionID, wait, err := previewSessionAndWait(args)
	if err != nil {
		return mcp.ErrorResult(err.Error())
	}
	return e.create(ctx, mustJSON(map[string]any{"session_id": sessionID, "wait": wait}))
}

func (e *previewToolExecutor) waitBranchReady(ctx context.Context, previewID string) *mcp.ToolCallResult {
	if strings.TrimSpace(previewID) == "" {
		return mcp.ErrorResult("preview create did not return a preview_id to wait on")
	}
	deadline := time.NewTimer(previewWaitTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		result := e.status(ctx, mustJSON(map[string]string{"preview_id": previewID}))
		if result.IsError {
			return result
		}
		var status previewView
		if err := json.Unmarshal([]byte(firstText(result)), &status); err != nil {
			return result
		}
		switch status.Status {
		case "running", "ready", "partially_ready":
			return result
		case "failed", "stopped", "expired", "unavailable":
			if status.Error != "" {
				return mcp.ErrorResult(fmt.Sprintf("preview %s: %s", status.Status, status.Error))
			}
			return mcp.ErrorResult(fmt.Sprintf("preview %s", status.Status))
		}
		select {
		case <-ctx.Done():
			return mcp.ErrorResult(ctx.Err().Error())
		case <-deadline.C:
			return mcp.ErrorResult(fmt.Sprintf("timed out waiting for preview %s", previewID))
		case <-ticker.C:
		}
	}
}

func (e *previewToolExecutor) status(ctx context.Context, args json.RawMessage) *mcp.ToolCallResult {
	var params struct {
		SessionID string `json:"session_id"`
		PreviewID string `json:"preview_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return mcp.ErrorResult("invalid preview status arguments")
	}
	if err := (previewTarget{SessionID: params.SessionID, PreviewID: params.PreviewID, Internal: e.internal}).validate(); err != nil {
		return mcp.ErrorResult(err.Error())
	}
	if params.SessionID != "" {
		return e.sessionStatus(ctx, params.SessionID)
	}
	var resp struct {
		Data branchPreviewWire `json:"data"`
	}
	if err := e.client.Do(ctx, http.MethodGet, "/api/v1/previews/"+params.PreviewID, nil, &resp); err != nil {
		return mcp.ErrorResult(fmt.Sprintf("preview status failed: %s", err))
	}
	return jsonResult(resp.Data.view())
}

func (e *previewToolExecutor) list(ctx context.Context, args json.RawMessage) *mcp.ToolCallResult {
	var params struct {
		SessionID string `json:"session_id"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &params); err != nil {
			return mcp.ErrorResult("invalid preview list arguments")
		}
	}
	if e.internal && strings.TrimSpace(params.SessionID) == "" {
		params.SessionID = strings.TrimSpace(os.Getenv("143_SESSION_ID"))
	}
	if strings.TrimSpace(params.SessionID) != "" {
		result := e.sessionStatus(ctx, params.SessionID)
		if result.IsError {
			return result
		}
		var status map[string]any
		if err := json.Unmarshal([]byte(firstText(result)), &status); err != nil {
			return result
		}
		return jsonResult([]map[string]any{status})
	}
	if e.internal {
		return mcp.ErrorResult("sandbox preview tools require 143_SESSION_ID")
	}
	var resp struct {
		Data []branchPreviewWire `json:"data"`
	}
	if err := e.client.Do(ctx, http.MethodGet, "/api/v1/previews", nil, &resp); err != nil {
		return mcp.ErrorResult(fmt.Sprintf("preview list failed: %s", err))
	}
	views := make([]previewView, 0, len(resp.Data))
	for _, p := range resp.Data {
		views = append(views, p.view())
	}
	return jsonResult(views)
}

func (e *previewToolExecutor) stop(ctx context.Context, args json.RawMessage) *mcp.ToolCallResult {
	var params struct {
		SessionID string `json:"session_id"`
		PreviewID string `json:"preview_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return mcp.ErrorResult("invalid preview stop arguments")
	}
	if err := (previewTarget{SessionID: params.SessionID, PreviewID: params.PreviewID, Internal: e.internal}).validate(); err != nil {
		return mcp.ErrorResult(err.Error())
	}
	if params.SessionID != "" {
		if err := e.client.Do(ctx, http.MethodDelete, e.sessionPreviewPath(params.SessionID), nil, nil); err != nil {
			return mcp.ErrorResult(fmt.Sprintf("preview stop failed: %s", err))
		}
		return jsonResult(map[string]string{"session_id": params.SessionID, "status": "stopped"})
	}
	if err := e.client.Do(ctx, http.MethodPost, "/api/v1/previews/"+params.PreviewID+"/stop", nil, nil); err != nil {
		return mcp.ErrorResult(fmt.Sprintf("preview stop failed: %s", err))
	}
	return jsonResult(map[string]string{"preview_id": params.PreviewID, "status": "stopping"})
}

func (e *previewToolExecutor) restart(ctx context.Context, args json.RawMessage) *mcp.ToolCallResult {
	sessionID, wait, err := previewSessionAndWait(args)
	if err != nil {
		return mcp.ErrorResult(err.Error())
	}
	var resp struct {
		Data any `json:"data"`
	}
	if err := e.client.Do(ctx, http.MethodPost, e.sessionPreviewPath(sessionID)+"/restart", nil, &resp); err != nil {
		return mcp.ErrorResult(fmt.Sprintf("preview restart failed: %s", err))
	}
	if wait {
		return e.waitSessionReady(ctx, sessionID)
	}
	return jsonResult(resp.Data)
}

func (e *previewToolExecutor) update(ctx context.Context, args json.RawMessage) *mcp.ToolCallResult {
	var params struct {
		SessionID     string `json:"session_id"`
		Path          string `json:"path"`
		Wait          bool   `json:"wait"`
		ForceMode     string `json:"force_mode"`
		ReloadBrowser *bool  `json:"reload_browser"`
		Config        string `json:"config"`
	}
	if err := json.Unmarshal(args, &params); err != nil || params.SessionID == "" {
		return mcp.ErrorResult("session_id is required")
	}
	body := map[string]any{"path": params.Path, "wait": params.Wait, "force_mode": params.ForceMode}
	if params.ReloadBrowser != nil {
		body["reload_browser"] = *params.ReloadBrowser
	}
	if strings.TrimSpace(params.Config) != "" {
		var cfg map[string]any
		if err := json.Unmarshal([]byte(params.Config), &cfg); err != nil {
			return mcp.ErrorResult(fmt.Sprintf("config must be a JSON object: %s", err))
		}
		body["config"] = cfg
	}
	var resp struct {
		Data any `json:"data"`
	}
	if err := e.client.Do(ctx, http.MethodPost, e.sessionPreviewPath(params.SessionID)+"/update", body, &resp); err != nil {
		return mcp.ErrorResult(fmt.Sprintf("preview update failed: %s", err))
	}
	return jsonResult(resp.Data)
}

func (e *previewToolExecutor) screenshot(ctx context.Context, args json.RawMessage) *mcp.ToolCallResult {
	var params struct {
		SessionID    string `json:"session_id"`
		PreviewID    string `json:"preview_id"`
		Path         string `json:"path"`
		ViewportW    int    `json:"viewport_w"`
		ViewportH    int    `json:"viewport_h"`
		FullPage     bool   `json:"full_page"`
		DelayMS      int    `json:"delay_ms"`
		InlineBase64 *bool  `json:"inline_base64"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return mcp.ErrorResult("invalid preview screenshot arguments")
	}
	target := previewTarget{SessionID: params.SessionID, PreviewID: params.PreviewID, Internal: e.internal}
	basePath, err := target.basePath()
	if err != nil {
		return mcp.ErrorResult(err.Error())
	}
	body := map[string]any{"path": params.Path, "viewport_w": params.ViewportW, "viewport_h": params.ViewportH, "full_page": params.FullPage, "delay_ms": params.DelayMS}
	if params.InlineBase64 != nil {
		body["inline_base64"] = *params.InlineBase64
	}
	if target.SessionID != "" {
		var resp struct {
			Data map[string]any `json:"data"`
		}
		if err := e.client.Do(ctx, http.MethodPost, basePath+"/observe", body, &resp); err != nil {
			return mcp.ErrorResult(fmt.Sprintf("preview screenshot failed: %s", err))
		}
		screenshot, _ := resp.Data["screenshot"].(map[string]any)
		if screenshot == nil {
			return mcp.ErrorResult("preview screenshot returned no image")
		}
		if params.InlineBase64 != nil && !*params.InlineBase64 {
			delete(screenshot, "png_base64")
		}
		return jsonResult(screenshot)
	}
	var resp struct {
		Data map[string]any `json:"data"`
	}
	if err := e.client.Do(ctx, http.MethodPost, basePath+"/screenshot", body, &resp); err != nil {
		return mcp.ErrorResult(fmt.Sprintf("preview screenshot failed: %s", err))
	}
	if params.InlineBase64 != nil && !*params.InlineBase64 {
		delete(resp.Data, "png_base64")
	}
	return jsonResult(resp.Data)
}

func (e *previewToolExecutor) console(ctx context.Context, args json.RawMessage) *mcp.ToolCallResult {
	var params struct {
		SessionID string `json:"session_id"`
		PreviewID string `json:"preview_id"`
		Level     string `json:"level"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return mcp.ErrorResult("invalid preview console arguments")
	}
	basePath, err := (previewTarget{SessionID: params.SessionID, PreviewID: params.PreviewID, Internal: e.internal}).basePath()
	if err != nil {
		return mcp.ErrorResult(err.Error())
	}
	path := basePath + "/console"
	if params.Level != "" {
		// The server filters by level (exact, case-insensitive); no client-side
		// re-filtering needed.
		path += "?level=" + url.QueryEscape(params.Level)
	}
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := e.client.Do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return mcp.ErrorResult(fmt.Sprintf("preview console failed: %s", err))
	}
	return jsonResult(resp.Data)
}

func (e *previewToolExecutor) inspect(ctx context.Context, args json.RawMessage) *mcp.ToolCallResult {
	var params struct {
		SessionID string `json:"session_id"`
		PreviewID string `json:"preview_id"`
		Selector  string `json:"selector"`
		X         *int   `json:"x"`
		Y         *int   `json:"y"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return mcp.ErrorResult("invalid preview inspect arguments")
	}
	basePath, err := (previewTarget{SessionID: params.SessionID, PreviewID: params.PreviewID, Internal: e.internal}).basePath()
	if err != nil {
		return mcp.ErrorResult(err.Error())
	}
	// Coordinates use pointers so (0,0) — the top-left pixel, which the server
	// accepts (0..10000) — is distinguishable from omitted coordinates.
	if params.Selector == "" && (params.X == nil || params.Y == nil) {
		return mcp.ErrorResult("selector or x/y coordinates are required")
	}
	x, y := 0, 0
	if params.X != nil {
		x = *params.X
	}
	if params.Y != nil {
		y = *params.Y
	}
	body := map[string]any{"selector": params.Selector, "x": x, "y": y}
	var resp struct {
		Data any `json:"data"`
	}
	if err := e.client.Do(ctx, http.MethodPost, basePath+"/inspect", body, &resp); err != nil {
		return mcp.ErrorResult(fmt.Sprintf("preview inspect failed: %s", err))
	}
	return jsonResult(resp.Data)
}

func (e *previewToolExecutor) interact(ctx context.Context, args json.RawMessage) *mcp.ToolCallResult {
	target, steps, err := previewJSONPayload(args, "steps")
	if err != nil {
		return mcp.ErrorResult(err.Error())
	}
	return e.postPreviewTarget(ctx, target, "interact", map[string]any{"steps": steps}, "preview interact failed")
}

func (e *previewToolExecutor) observe(ctx context.Context, args json.RawMessage) *mcp.ToolCallResult {
	var params map[string]any
	if err := json.Unmarshal(args, &params); err != nil {
		return mcp.ErrorResult("invalid preview observe arguments")
	}
	target := previewTargetFromMap(params)
	output, _ := params["output"].(string)
	delete(params, "session_id")
	delete(params, "preview_id")
	delete(params, "output")
	if strings.TrimSpace(output) != "" {
		params["inline_base64"] = true
	}
	result := e.postPreviewTarget(ctx, target, "observe", params, "preview observe failed")
	return writePreviewObservationImage(result, output, false)
}

func (e *previewToolExecutor) act(ctx context.Context, args json.RawMessage) *mcp.ToolCallResult {
	var params map[string]any
	if err := json.Unmarshal(args, &params); err != nil {
		return mcp.ErrorResult("invalid preview act arguments")
	}
	target := previewTargetFromMap(params)
	output, _ := params["output"].(string)
	rawSteps, _ := params["steps"].(string)
	if strings.TrimSpace(rawSteps) == "" {
		return mcp.ErrorResult("steps is required")
	}
	var steps any
	if err := json.Unmarshal([]byte(rawSteps), &steps); err != nil {
		return mcp.ErrorResult(fmt.Sprintf("steps must be JSON: %s", err))
	}
	params["steps"] = steps
	delete(params, "session_id")
	delete(params, "preview_id")
	delete(params, "output")
	if strings.TrimSpace(output) != "" {
		params["inline_base64"] = true
	}
	result := e.postPreviewTarget(ctx, target, "act", params, "preview act failed")
	return writePreviewObservationImage(result, output, true)
}

func writePreviewObservationImage(result *mcp.ToolCallResult, output string, act bool) *mcp.ToolCallResult {
	if result == nil || result.IsError || strings.TrimSpace(output) == "" {
		return result
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(firstText(result)), &payload); err != nil {
		return mcp.ErrorResult(fmt.Sprintf("decode preview image response: %s", err))
	}
	observation := payload
	if act {
		observation, _ = payload["observation"].(map[string]any)
	}
	screenshot, _ := observation["screenshot"].(map[string]any)
	encoded, _ := screenshot["png_base64"].(string)
	if encoded == "" {
		return mcp.ErrorResult("preview response did not include screenshot bytes")
	}
	png, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return mcp.ErrorResult(fmt.Sprintf("decode preview screenshot: %s", err))
	}
	if err := os.WriteFile(output, png, 0o600); err != nil {
		return mcp.ErrorResult(fmt.Sprintf("write preview screenshot: %s", err))
	}
	delete(screenshot, "png_base64")
	observation["workspace_path"] = output
	return jsonResult(payload)
}

func (e *previewToolExecutor) multiViewport(ctx context.Context, args json.RawMessage) *mcp.ToolCallResult {
	var params struct {
		SessionID string `json:"session_id"`
		PreviewID string `json:"preview_id"`
		Path      string `json:"path"`
		Viewports string `json:"viewports"`
		DelayMS   int    `json:"delay_ms"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return mcp.ErrorResult("invalid preview multi_viewport arguments")
	}
	target := previewTarget{SessionID: params.SessionID, PreviewID: params.PreviewID, Internal: e.internal}
	if _, err := target.basePath(); err != nil {
		return mcp.ErrorResult(err.Error())
	}
	body := map[string]any{"path": params.Path, "delay_ms": params.DelayMS}
	if strings.TrimSpace(params.Viewports) != "" {
		var viewports []map[string]any
		if err := json.Unmarshal([]byte(params.Viewports), &viewports); err != nil {
			return mcp.ErrorResult(fmt.Sprintf("viewports must be a JSON array: %s", err))
		}
		body["viewports"] = viewports
	}
	return e.postPreviewTarget(ctx, target, "multi-viewport", body, "preview multi_viewport failed")
}

func (e *previewToolExecutor) visualDiff(ctx context.Context, args json.RawMessage) *mcp.ToolCallResult {
	var params struct {
		SessionID        string `json:"session_id"`
		PreviewID        string `json:"preview_id"`
		BeforeSnapshotID string `json:"before_snapshot_id"`
		AfterSnapshotID  string `json:"after_snapshot_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil || params.BeforeSnapshotID == "" || params.AfterSnapshotID == "" {
		return mcp.ErrorResult("session_id or preview_id, before_snapshot_id, and after_snapshot_id are required")
	}
	target := previewTarget{SessionID: params.SessionID, PreviewID: params.PreviewID, Internal: e.internal}
	if _, err := target.basePath(); err != nil {
		return mcp.ErrorResult(err.Error())
	}
	body := map[string]string{"before_snapshot_id": params.BeforeSnapshotID, "after_snapshot_id": params.AfterSnapshotID}
	return e.postPreviewTarget(ctx, target, "visual-diff", body, "preview visual_diff failed")
}

func (e *previewToolExecutor) assertions(ctx context.Context, args json.RawMessage) *mcp.ToolCallResult {
	target, assertions, err := previewJSONPayload(args, "assertions")
	if err != nil {
		return mcp.ErrorResult(err.Error())
	}
	return e.postPreviewTarget(ctx, target, "assert", map[string]any{"assertions": assertions}, "preview assert failed")
}

func (e *previewToolExecutor) postPreviewTarget(ctx context.Context, target previewTarget, action string, body any, errPrefix string) *mcp.ToolCallResult {
	basePath, err := target.basePath()
	if err != nil {
		return mcp.ErrorResult(err.Error())
	}
	var resp struct {
		Data any `json:"data"`
	}
	if err := e.client.Do(ctx, http.MethodPost, basePath+"/"+action, body, &resp); err != nil {
		return mcp.ErrorResult(fmt.Sprintf("%s: %s", errPrefix, err))
	}
	return jsonResult(resp.Data)
}

// resolveRepository matches a human/agent-supplied name against the org's
// repositories: exact full_name ("acme/webapp") first, then unique
// short-name ("webapp"). Ambiguity and misses return errors that name the
// candidates so agents can self-correct.
func (e *previewToolExecutor) resolveRepository(ctx context.Context, name string) (id, fullName string, err error) {
	var resp struct {
		Data []struct {
			ID       string `json:"id"`
			FullName string `json:"full_name"`
		} `json:"data"`
	}
	if err := e.client.Do(ctx, http.MethodGet, "/api/v1/repositories", nil, &resp); err != nil {
		return "", "", fmt.Errorf("list repositories: %w", err)
	}

	name = strings.TrimSpace(name)
	var matches []struct{ id, fullName string }
	for _, repo := range resp.Data {
		if strings.EqualFold(repo.FullName, name) {
			return repo.ID, repo.FullName, nil
		}
		if parts := strings.SplitN(repo.FullName, "/", 2); len(parts) == 2 && strings.EqualFold(parts[1], name) {
			matches = append(matches, struct{ id, fullName string }{repo.ID, repo.FullName})
		}
	}
	switch len(matches) {
	case 1:
		return matches[0].id, matches[0].fullName, nil
	case 0:
		available := make([]string, 0, len(resp.Data))
		for _, repo := range resp.Data {
			available = append(available, repo.FullName)
		}
		return "", "", fmt.Errorf("repository %q not found — connected repositories: %s", name, strings.Join(available, ", "))
	default:
		candidates := make([]string, 0, len(matches))
		for _, m := range matches {
			candidates = append(candidates, m.fullName)
		}
		return "", "", fmt.Errorf("repository %q is ambiguous — use the full name: %s", name, strings.Join(candidates, ", "))
	}
}

// branchExists checks the remote for the branch. Errors are swallowed by
// the caller (the create call itself will surface a real failure) — this
// is purely the fast-path for the BRANCH_NOT_PUSHED hint.
func (e *previewToolExecutor) branchExists(ctx context.Context, repoID, branch string) (bool, error) {
	var resp struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := e.client.Do(ctx, http.MethodGet, "/api/v1/repositories/"+repoID+"/branches", nil, &resp); err != nil {
		return false, err
	}
	for _, b := range resp.Data {
		if b.Name == branch {
			return true, nil
		}
	}
	return false, nil
}

func jsonResult(v any) *mcp.ToolCallResult {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.ErrorResult(fmt.Sprintf("encode result: %s", err))
	}
	return mcp.TextResult(string(data))
}
