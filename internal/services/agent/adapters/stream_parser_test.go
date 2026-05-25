package adapters

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// TestParseAgentStreamLine_NonJSON verifies that a non-JSON line lands in
// summaryParts as plain output.
func TestParseAgentStreamLine_NonJSON(t *testing.T) {
	t.Parallel()

	logCh := make(chan agent.LogEntry, 10)
	result := &agent.AgentResult{}
	var summary []string
	var last string

	parseAgentStreamLine([]byte(`not json at all`), streamParseConfig{}, result, logCh, &summary, &last)
	close(logCh)

	logs := drain(logCh)
	require.Len(t, logs, 1)
	require.Equal(t, "output", logs[0].Level)
	require.Contains(t, summary, "not json at all")
}

// TestParseAgentStreamLine_UnknownType confirms unknown event types fall
// through to a debug log rather than being silently dropped.
func TestParseAgentStreamLine_UnknownType(t *testing.T) {
	t.Parallel()

	logCh := make(chan agent.LogEntry, 10)
	result := &agent.AgentResult{}
	var summary []string
	var last string

	parseAgentStreamLine(
		[]byte(`{"type":"weird_event","content":"x"}`),
		streamParseConfig{},
		result, logCh, &summary, &last,
	)
	close(logCh)

	logs := drain(logCh)
	require.Len(t, logs, 1)
	require.Equal(t, "debug", logs[0].Level)
}

// TestParseAgentStreamLine_AmpEvents exercises the Amp configuration: no
// message-as-assistant, no done-as-result, no tool_use model capture, but
// session_id capture on result events.
func TestParseAgentStreamLine_AmpEvents(t *testing.T) {
	t.Parallel()

	type check struct {
		level     string
		contains  string
		metaField string
		metaValue interface{}
	}

	tests := []struct {
		name          string
		line          string
		wantLogs      []check
		wantAssistant string
		wantTokens    *agent.TokenUsage
		wantSession   string
	}{
		{
			name:          "assistant falls back to message",
			line:          `{"type":"assistant","message":"hello from amp"}`,
			wantLogs:      []check{{level: "output", contains: "hello from amp"}},
			wantAssistant: "hello from amp",
		},
		{
			name:     "tool_use falls back to name",
			line:     `{"type":"tool_use","name":"bash","input":{"cmd":"ls"}}`,
			wantLogs: []check{{level: "tool_use", contains: "bash", metaField: "tool", metaValue: "bash"}},
		},
		{
			name:     "tool_result falls back to name and result",
			line:     `{"type":"tool_result","name":"bash","result":{"stdout":"ok"}}`,
			wantLogs: []check{{level: "output", contains: "stdout", metaField: "tool", metaValue: "bash"}},
		},
		{
			name:     "thinking goes to debug",
			line:     `{"type":"thinking","content":"considering options"}`,
			wantLogs: []check{{level: "debug", contains: "considering options", metaField: "type", metaValue: "thinking"}},
		},
		{
			name:     "error field wins",
			line:     `{"type":"error","error":"provider unreachable"}`,
			wantLogs: []check{{level: "error", contains: "provider unreachable"}},
		},
		{
			name:     "error falls back to message then content",
			line:     `{"type":"error","content":"fallback text"}`,
			wantLogs: []check{{level: "error", contains: "fallback text"}},
		},
		{
			name:       "result with embedded token usage",
			line:       `{"type":"result","content":"done","result":{"input_tokens":100,"output_tokens":25}}`,
			wantLogs:   []check{{level: "info", contains: "done"}},
			wantTokens: &agent.TokenUsage{InputTokens: 100, OutputTokens: 25},
		},
		{
			name:        "result captures session id",
			line:        `{"type":"result","content":"ok","session_id":"amp-sess-42"}`,
			wantLogs:    []check{{level: "info", contains: "ok"}},
			wantSession: "amp-sess-42",
		},
		{
			name:     "amp does not treat message as assistant",
			line:     `{"type":"message","content":"only if message_as_assistant is on"}`,
			wantLogs: []check{{level: "debug", contains: "message_as_assistant"}},
			// assistant stays empty because amp config doesn't opt into this.
		},
		{
			name:     "amp does not treat done as result",
			line:     `{"type":"done","content":"done with work"}`,
			wantLogs: []check{{level: "debug", contains: "done with work"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			logCh := make(chan agent.LogEntry, 10)
			result := &agent.AgentResult{}
			var summary []string
			var last string

			parseAgentStreamLine(
				[]byte(tc.line),
				ampStreamingConfig.ParseConfig,
				result, logCh, &summary, &last,
			)
			close(logCh)

			logs := drain(logCh)
			require.Len(t, logs, len(tc.wantLogs))
			for i, want := range tc.wantLogs {
				require.Equal(t, want.level, logs[i].Level)
				require.Contains(t, logs[i].Message, want.contains)
				if want.metaField != "" {
					require.Equal(t, want.metaValue, logs[i].Metadata[want.metaField])
				}
			}
			if tc.wantAssistant != "" {
				require.Equal(t, tc.wantAssistant, last)
			}
			if tc.wantTokens != nil {
				require.Equal(t, tc.wantTokens.InputTokens, result.TokenUsage.InputTokens)
				require.Equal(t, tc.wantTokens.OutputTokens, result.TokenUsage.OutputTokens)
			}
			if tc.wantSession != "" {
				require.Equal(t, tc.wantSession, result.AgentSessionID)
			}
		})
	}
}

// TestParseAgentStreamLine_PiEvents exercises the Pi configuration: message
// and done aliases, tool_use model capture, no session_id capture.
func TestParseAgentStreamLine_PiEvents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		line          string
		wantLevel     string
		wantContains  string
		wantMetaField string
		wantMetaValue interface{}
		wantAssistant string
		wantTokens    *agent.TokenUsage
	}{
		{
			name:          "message event uses content",
			line:          `{"type":"message","content":"pi speaks"}`,
			wantLevel:     "output",
			wantContains:  "pi speaks",
			wantAssistant: "pi speaks",
		},
		{
			name:          "assistant falls back to message field",
			line:          `{"type":"assistant","message":"pi fallback"}`,
			wantLevel:     "output",
			wantContains:  "pi fallback",
			wantAssistant: "pi fallback",
		},
		{
			name:          "tool_use name fallback with model metadata",
			line:          `{"type":"tool_use","name":"edit","model":"anthropic/claude-sonnet-4-6","input":{"path":"a.go"}}`,
			wantLevel:     "tool_use",
			wantContains:  "edit",
			wantMetaField: "model",
			wantMetaValue: "anthropic/claude-sonnet-4-6",
		},
		{
			name:          "tool_result falls back to Name and Result",
			line:          `{"type":"tool_result","name":"edit","result":{"status":"ok"}}`,
			wantLevel:     "output",
			wantContains:  "status",
			wantMetaField: "tool",
			wantMetaValue: "edit",
		},
		{
			name:          "thinking goes to debug",
			line:          `{"type":"thinking","content":"pondering"}`,
			wantLevel:     "debug",
			wantContains:  "pondering",
			wantMetaField: "type",
			wantMetaValue: "thinking",
		},
		{
			name:         "error uses error field",
			line:         `{"type":"error","error":"upstream 500"}`,
			wantLevel:    "error",
			wantContains: "upstream 500",
		},
		{
			name:         "error falls back to message",
			line:         `{"type":"error","message":"msg body"}`,
			wantLevel:    "error",
			wantContains: "msg body",
		},
		{
			name:         "error falls back to content",
			line:         `{"type":"error","content":"content body"}`,
			wantLevel:    "error",
			wantContains: "content body",
		},
		{
			name:         "done event with embedded token usage",
			line:         `{"type":"done","content":"","result":{"input_tokens":42,"output_tokens":7}}`,
			wantLevel:    "info",
			wantContains: "",
			wantTokens:   &agent.TokenUsage{InputTokens: 42, OutputTokens: 7},
		},
		{
			name:         "unknown event type goes to debug",
			line:         `{"type":"mystery_event","content":"x"}`,
			wantLevel:    "debug",
			wantContains: "mystery_event",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			logCh := make(chan agent.LogEntry, 10)
			result := &agent.AgentResult{}
			var summary []string
			var last string

			parseAgentStreamLine(
				[]byte(tc.line),
				piStreamingConfig.ParseConfig,
				result, logCh, &summary, &last,
			)
			close(logCh)

			logs := drain(logCh)
			require.Len(t, logs, 1)
			require.Equal(t, tc.wantLevel, logs[0].Level)
			if tc.wantContains != "" {
				require.Contains(t, logs[0].Message, tc.wantContains)
			}
			if tc.wantMetaField != "" {
				require.Equal(t, tc.wantMetaValue, logs[0].Metadata[tc.wantMetaField])
			}
			if tc.wantAssistant != "" {
				require.Equal(t, tc.wantAssistant, last)
			}
			if tc.wantTokens != nil {
				require.Equal(t, tc.wantTokens.InputTokens, result.TokenUsage.InputTokens)
				require.Equal(t, tc.wantTokens.OutputTokens, result.TokenUsage.OutputTokens)
			}
		})
	}
}

// TestParseAgentStreamLine_EmptyErrorFallsBackToUnknown ensures a type="error"
// event with no error/message/content fields still produces a non-empty log
// message so operators can tell something went wrong.
func TestParseAgentStreamLine_EmptyErrorFallsBackToUnknown(t *testing.T) {
	t.Parallel()

	logCh := make(chan agent.LogEntry, 5)
	result := &agent.AgentResult{}
	var summary []string
	var last string

	parseAgentStreamLine(
		[]byte(`{"type":"error"}`),
		streamParseConfig{},
		result, logCh, &summary, &last,
	)
	close(logCh)

	logs := drain(logCh)
	require.Len(t, logs, 1)
	require.Equal(t, "error", logs[0].Level)
	require.Equal(t, "unknown error", logs[0].Message,
		"empty error events must surface a placeholder instead of a blank message")
}

func TestParseAgentStreamLine_NormalizesActionChoiceHumanInput(t *testing.T) {
	t.Parallel()

	logCh := make(chan agent.LogEntry, 5)
	result := &agent.AgentResult{}
	var summary []string
	var last string

	parseAgentStreamLine(
		[]byte(`{"type":"action_choice","request_id":"action-1","title":"Choose next step","content":"The agent needs direction.","actions":[{"id":"fix_tests","label":"Fix tests","description":"Update the failing suite"},{"id":"open_pr","label":"Open PR","kind":"positive"}],"response_schema":{"type":"object","required":["decision"],"properties":{"decision":{"type":"string"}}}}`),
		ampStreamingConfig.ParseConfig,
		result,
		logCh,
		&summary,
		&last,
	)
	close(logCh)

	logs := drain(logCh)
	require.True(t, result.RequiresHumanInput, "generic action_choice events should pause the run for human input")
	require.Len(t, logs, 1, "generic action_choice events should emit one normalized human-input log")
	require.Equal(t, "human_input", logs[0].Level, "generic action_choice events should use the normalized human-input log level")
	require.Equal(t, "The agent needs direction.", logs[0].Message, "generic action_choice content should become the user-visible request body")
	require.Equal(t, string(models.HumanInputRequestKindActionChoice), logs[0].Metadata["request_kind"], "generic action_choice events should normalize the request kind")
	require.NotNil(t, logs[0].HumanInput, "generic action_choice logs should include a durable request payload")
	require.Equal(t, "action-1", logs[0].HumanInput.ProviderRequestID, "generic action_choice payload should retain the provider request id")
	require.Equal(t, models.HumanInputRequestKindActionChoice, logs[0].HumanInput.Kind, "generic action_choice payload should use the action choice kind")
	require.Equal(t, "Fix tests", logs[0].HumanInput.Choices[0].Label, "generic action_choice payload should retain action labels")
	require.JSONEq(t, `{"type":"object","required":["decision"],"properties":{"decision":{"type":"string"}}}`, string(logs[0].HumanInput.ResponseSchema), "generic action_choice payload should retain the response schema")
}

// TestParseAgentStreamLine_OutputOnlyTokenUsageAccepted covers a reported bug:
// when a result payload reports only output tokens (legitimate for continuation
// turns that re-use cached input), the parser used to drop the counter
// entirely because of an `input_tokens > 0` guard.
func TestParseAgentStreamLine_OutputOnlyTokenUsageAccepted(t *testing.T) {
	t.Parallel()

	logCh := make(chan agent.LogEntry, 5)
	result := &agent.AgentResult{}
	var summary []string
	var last string

	parseAgentStreamLine(
		[]byte(`{"type":"result","content":"done","result":{"input_tokens":0,"output_tokens":42}}`),
		streamParseConfig{},
		result, logCh, &summary, &last,
	)
	close(logCh)

	require.Equal(t, 0, result.TokenUsage.InputTokens)
	require.Equal(t, 42, result.TokenUsage.OutputTokens,
		"output-only usage payloads must still populate TokenUsage")
}
