package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScreenshotResultSupportsBothCaptureKeys(t *testing.T) {
	t.Parallel()

	result := ScreenshotResult{Capture: &PreviewCapture{ID: "capture-1", URL: "https://example.test/capture.png"}}
	encoded, err := json.Marshal(result)
	require.NoError(t, err, "screenshot result should marshal")
	require.JSONEq(t, `{
		"page_title":"",
		"url":"",
		"viewport":{"width":0,"height":0,"name":""},
		"captured_at":"0001-01-01T00:00:00Z",
		"capture":{"id":"capture-1","kind":"","content_type":"","url":"https://example.test/capture.png","bytes":0,"created_at":"0001-01-01T00:00:00Z"},
		"artifact":{"id":"capture-1","kind":"","content_type":"","url":"https://example.test/capture.png","bytes":0,"created_at":"0001-01-01T00:00:00Z"}
	}`, string(encoded), "screenshot response should expose both supported keys")

	var decoded ScreenshotResult
	require.NoError(t, json.Unmarshal([]byte(`{"artifact":{"id":"legacy-1","url":"https://example.test/legacy.png"}}`), &decoded), "legacy screenshot response should unmarshal")
	require.Equal(t, "legacy-1", decoded.Capture.ID, "legacy screenshot reference should populate Capture")
}

func TestPreviewVerificationStepSupportsBothCaptureKeys(t *testing.T) {
	t.Parallel()

	step := PreviewVerificationStep{Index: 1, Capture: &PreviewCapture{ID: "capture-1"}}
	encoded, err := json.Marshal(step)
	require.NoError(t, err, "verification step should marshal")
	require.Contains(t, string(encoded), `"capture":{"id":"capture-1"`, "verification step should expose the current key")
	require.Contains(t, string(encoded), `"artifact":{"id":"capture-1"`, "verification step should expose the compatibility key")

	var decoded PreviewVerificationStep
	require.NoError(t, json.Unmarshal([]byte(`{"index":1,"artifact":{"id":"legacy-1"}}`), &decoded), "legacy verification step should unmarshal")
	require.Equal(t, "legacy-1", decoded.Capture.ID, "legacy verification reference should populate Capture")
}

func TestPreviewVerificationRunSupportsBothCaptureCollections(t *testing.T) {
	t.Parallel()

	run := PreviewVerificationRun{Captures: json.RawMessage(`[{"id":"capture-1"}]`)}
	encoded, err := json.Marshal(run)
	require.NoError(t, err, "verification run should marshal")
	require.Contains(t, string(encoded), `"captures":[{"id":"capture-1"}]`, "verification run should expose the current collection")
	require.Contains(t, string(encoded), `"artifacts":[{"id":"capture-1"}]`, "verification run should expose the compatibility collection")

	var decoded PreviewVerificationRun
	require.NoError(t, json.Unmarshal([]byte(`{"artifacts":[{"id":"legacy-1"}]}`), &decoded), "legacy verification run should unmarshal")
	require.JSONEq(t, `[{"id":"legacy-1"}]`, string(decoded.Captures), "legacy collection should populate Captures")
}

func TestCodeReviewResponsesExposeCompatibilityFields(t *testing.T) {
	t.Parallel()

	key := "prompts/session/reviewer"
	evidence := CodeReviewEvidence{PromptRecords: []CodeReviewPromptRecord{{RecordKey: key}}}
	encoded, err := json.Marshal(evidence)
	require.NoError(t, err, "code review evidence should marshal")
	require.Contains(t, string(encoded), `"prompt_records"`, "evidence should expose the current collection key")
	require.Contains(t, string(encoded), `"prompt_artifacts"`, "evidence should expose the compatibility collection key")
	require.Contains(t, string(encoded), `"record_key":"`+key+`"`, "prompt record should expose the current identity key")
	require.Contains(t, string(encoded), `"artifact_key":"`+key+`"`, "prompt record should expose the compatibility identity key")

	item := CodeReviewListItem{
		CodeReviewSessionMetadata: CodeReviewSessionMetadata{PromptRecordKey: &key},
		RiskReasonDetails:         json.RawMessage(`[{"code":"blocking_findings"}]`),
	}
	encoded, err = json.Marshal(item)
	require.NoError(t, err, "code review list item should marshal")
	require.Contains(t, string(encoded), `"prompt_record_key":"`+key+`"`, "list item should expose the current prompt reference")
	require.Contains(t, string(encoded), `"prompt_artifact_key":"`+key+`"`, "list item should expose the compatibility prompt reference")
	require.Contains(t, string(encoded), `"risk_reason_details":[{"code":"blocking_findings"}]`, "list item should expose structured non-approval reasons")
}
