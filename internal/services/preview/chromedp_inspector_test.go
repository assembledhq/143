package preview

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestChromeDPInspector_BindsSessionContextIdentity(t *testing.T) {
	t.Parallel()
	inspector := NewChromeDPInspector(ChromeDPInspectorConfig{}, zerolog.Nop())
	inspector.BindSessionBrowser("preview-old", "session-1")
	inspector.BindSessionBrowser("preview-new", "session-1")
	require.Equal(t, "session:session-1", inspector.contextKeys["preview-old"], "old preview instance should bind to the session context")
	require.Equal(t, inspector.contextKeys["preview-old"], inspector.contextKeys["preview-new"], "replacement preview instance should retain browser identity")
	require.False(t, inspector.HasContext(models.BrowserTarget{PreviewID: "preview-new", SessionID: "session-1", ContextKey: "session:session-1"}), "binding should not eagerly launch a browser")
}

func TestCompatiblePreviewRestoreURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, stored, active, expectedURL string
		expected                          bool
	}{
		{name: "same origin", stored: "https://old.preview.143.dev/app?q=1", active: "https://old.preview.143.dev/", expected: true, expectedURL: "https://old.preview.143.dev/app?q=1"},
		{name: "replacement preview sibling", stored: "https://old.preview.143.dev/app", active: "https://new.preview.143.dev/", expected: true, expectedURL: "https://new.preview.143.dev/app"},
		{name: "different parent domain", stored: "https://old.preview.143.dev/app", active: "https://new.evil.test/", expected: false},
		{name: "different scheme", stored: "http://old.preview.143.dev/app", active: "https://new.preview.143.dev/", expected: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, url := compatiblePreviewRestoreURL(tt.stored, tt.active)
			require.Equal(t, tt.expected, actual, "restore compatibility should match preview-origin policy")
			require.Equal(t, tt.expectedURL, url, "compatible restore should retain the stored path on the active origin")
		})
	}
}

func TestConsoleMessagesAfter(t *testing.T) {
	t.Parallel()
	messages := []ConsoleMessage{{Cursor: 1, Level: "log", Text: "old"}, {Cursor: 2, Level: "error", Text: "new"}, {Cursor: 3, Level: "warn", Text: "newer"}}
	actual := consoleMessagesAfter(messages, 1)
	expected := []models.ConsoleMessage{{Cursor: 2, Level: "error", Text: "new"}}
	require.Equal(t, expected, actual, "observation should include only new error-level console messages after its cursor")
}

func TestValidateInteractionStep(t *testing.T) {
	t.Parallel()
	x, y := 10, 20
	tests := []struct {
		name      string
		step      models.InteractionStep
		expectErr bool
	}{
		{name: "semantic click", step: models.InteractionStep{Action: "click", Role: "button", Name: "Save"}},
		{name: "coordinate click", step: models.InteractionStep{Action: "click", X: &x, Y: &y}},
		{name: "fill selector", step: models.InteractionStep{Action: "fill", Selector: "#email"}},
		{name: "URL wait", step: models.InteractionStep{Action: "wait", URL: "/dashboard"}},
		{name: "viewport", step: models.InteractionStep{Action: "viewport", Value: "390x844"}},
		{name: "missing click target", step: models.InteractionStep{Action: "click"}, expectErr: true},
		{name: "partial coordinates", step: models.InteractionStep{Action: "click", X: &x}, expectErr: true},
		{name: "missing fill target", step: models.InteractionStep{Action: "fill", Value: "text"}, expectErr: true},
		{name: "invalid viewport", step: models.InteractionStep{Action: "viewport", Value: "huge"}, expectErr: true},
		{name: "unknown action", step: models.InteractionStep{Action: "launch"}, expectErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateInteractionStep(tt.step)
			if tt.expectErr {
				require.Error(t, err, "invalid interaction should be rejected")
				return
			}
			require.NoError(t, err, "supported interaction should validate")
		})
	}
}

func TestRedactBrowserText(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, input, expected string }{
		{name: "bearer token", input: "request Authorization: Bearer abc123", expected: "request Authorization: [REDACTED]"},
		{name: "password", input: `password="hunter2"`, expected: `password="[REDACTED]"`},
		{name: "ordinary message", input: "button failed to render", expected: "button failed to render"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, redactBrowserText(tt.input), "browser observation should redact likely secrets")
		})
	}
}

func TestInteractionStepErrorCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		targeted bool
		matches  int
		expected string
	}{
		{name: "timeout", err: context.DeadlineExceeded, expected: "STEP_TIMEOUT"},
		{name: "external navigation", err: errors.New("browser left the authorized preview origin"), expected: "NAVIGATION_NOT_ALLOWED"},
		{name: "ambiguous semantic target", err: errors.New("semantic target matched 2 elements"), targeted: true, matches: 2, expected: "AMBIGUOUS_TARGET"},
		{name: "missing target", err: errors.New("could not find node"), targeted: true, expected: "TARGET_NOT_FOUND"},
		{name: "invalid action", err: errors.New("click requires selector"), expected: "INVALID_STEP"},
		{name: "browser error", err: errors.New("target closed"), expected: "BROWSER_ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, interactionStepErrorCode(tt.err, tt.targeted, tt.matches), "step failure should use the stable diagnostic code")
		})
	}
}

func TestSameOrigin(t *testing.T) {
	t.Parallel()

	const preview = "https://abc.preview.143.dev/"

	cases := []struct {
		name      string
		rawURL    string
		expected  string
		wantMatch bool
	}{
		{"exact origin root", "https://abc.preview.143.dev/", preview, true},
		{"same origin with path", "https://abc.preview.143.dev/dashboard?q=1", preview, true},
		{"same origin no trailing slash", "https://abc.preview.143.dev", preview, true},

		// The bypasses a prefix check would have allowed.
		{"suffix attack subdomain", "https://abc.preview.143.dev.evil.com/", preview, false},
		{"userinfo attack", "https://abc.preview.143.dev@evil.com/", preview, false},

		// Other mismatches.
		{"different host", "https://evil.com/", preview, false},
		{"different scheme", "http://abc.preview.143.dev/", preview, false},
		{"different port", "https://abc.preview.143.dev:8443/", preview, false},
		{"unparseable target", "://not a url", preview, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := sameOrigin(tc.rawURL, tc.expected); got != tc.wantMatch {
				t.Errorf("sameOrigin(%q, %q) = %v, want %v", tc.rawURL, tc.expected, got, tc.wantMatch)
			}
		})
	}
}
