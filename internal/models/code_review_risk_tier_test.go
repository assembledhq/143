package models

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	riskTierTestHead  = "abcdef1111111111111111111111111111111111"
	riskTierTestOther = "fedcba2222222222222222222222222222222222"
	riskTierTestHash  = "3333333333333333333333333333333333333333333333333333333333333333"
	riskTierTestApp   = "example-risk-tier"
	riskTierTestAppID = int64(101)
)

var riskTierTestPublishedAt = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func riskTierTestPayloadFields(head string) map[string]any {
	return map[string]any{
		"schema_version": 1,
		"generation": map[string]any{
			"pr": 42, "head_sha": head, "base_sha": riskTierTestOther, "merge_base_sha": riskTierTestOther,
			"trunk_sha": riskTierTestOther, "index_id": riskTierTestHash, "policy_hash": riskTierTestHash, "mode": "enforce",
		},
		"tier": "low", "gate": "success", "override": false, "ui_render_change": "false",
		"published_at": "2026-10-18T12:00:00Z",
	}
}

func riskTierTestBlock(t *testing.T, fields map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(fields)
	require.NoError(t, err, "test payload should encode")
	return CodeReviewRiskTierPayloadFence + "\n" + string(encoded) + "\n```"
}

func riskTierTestOutput(t *testing.T, fields map[string]any) string {
	t.Helper()
	return "## Risk tier\n\nThe tier for this head is below.\n\n" + riskTierTestBlock(t, fields) + "\n"
}

func riskTierTestCheckRun(t *testing.T, id int64, appID int64, payloadHead, tier, render, mode string) CodeReviewCheckRun {
	t.Helper()
	fields := riskTierTestPayloadFields(payloadHead)
	fields["tier"], fields["ui_render_change"] = tier, render
	fields["generation"].(map[string]any)["mode"] = mode
	return CodeReviewCheckRun{ID: id, Name: DefaultCodeReviewRiskTierCheckName, HeadSHA: riskTierTestHead, Status: "completed", Conclusion: "success", AppID: appID, AppSlug: riskTierTestApp, OutputText: riskTierTestOutput(t, fields)}
}

func riskTierTestObservation(id int64, source CodeReviewRiskTierSource, tier CodeReviewRiskTier, render CodeReviewUIRenderChange, mode CodeReviewRiskTierMode) CodeReviewRiskTierObservation {
	indexID := riskTierTestHash
	publishedAt := riskTierTestPublishedAt
	return CodeReviewRiskTierObservation{CheckRunID: id, Source: source, Tier: tier, UIRenderChange: render, HeadSHA: riskTierTestHead, IndexID: &indexID, PolicyHash: riskTierTestHash, Mode: mode, PublishedAt: &publishedAt}
}

func riskTierTestSignal(source CodeReviewRiskTierSource, tier CodeReviewRiskTier, render CodeReviewUIRenderChange, observations ...CodeReviewRiskTierObservation) CodeReviewRiskTierSignal {
	return CodeReviewRiskTierSignal{Enabled: true, HeadSHA: riskTierTestHead, AppID: riskTierTestAppID, AppSlug: riskTierTestApp, CheckName: DefaultCodeReviewRiskTierCheckName, Source: source, Tier: tier, UIRenderChange: render, Observations: observations}
}

func TestExtractCodeReviewRiskTierPayload(t *testing.T) {
	t.Parallel()
	indexID := riskTierTestHash
	valid := CodeReviewRiskTierPayload{
		SchemaVersion: 1,
		Generation: CodeReviewRiskTierGeneration{
			PR: 42, HeadSHA: riskTierTestHead, BaseSHA: riskTierTestOther, MergeBaseSHA: riskTierTestOther, TrunkSHA: riskTierTestOther,
			IndexID: &indexID, PolicyHash: riskTierTestHash, Mode: CodeReviewRiskTierModeEnforce,
		},
		Tier: CodeReviewRiskTierLow, Gate: CodeReviewRiskTierGateSuccess, UIRenderChange: CodeReviewUIRenderChangeFalse,
		PublishedAt: riskTierTestPublishedAt,
	}
	nullIndex := valid
	nullIndex.Generation.IndexID = nil
	tests := []struct {
		name     string
		text     func(t *testing.T) string
		expected *CodeReviewRiskTierPayload
	}{
		{name: "one block preceded by prose", text: func(t *testing.T) string { return riskTierTestOutput(t, riskTierTestPayloadFields(riskTierTestHead)) }, expected: &valid},
		{name: "one block alone", text: func(t *testing.T) string { return riskTierTestBlock(t, riskTierTestPayloadFields(riskTierTestHead)) }, expected: &valid},
		{name: "carriage-return line endings", text: func(t *testing.T) string {
			return strings.ReplaceAll(riskTierTestOutput(t, riskTierTestPayloadFields(riskTierTestHead)), "\n", "\r\n")
		}, expected: &valid},
		{name: "other code blocks are ignored", text: func(t *testing.T) string {
			return "```\nnot the payload\n```\n" + riskTierTestOutput(t, riskTierTestPayloadFields(riskTierTestHead))
		}, expected: &valid},
		{name: "null index id", text: func(t *testing.T) string {
			fields := riskTierTestPayloadFields(riskTierTestHead)
			fields["generation"].(map[string]any)["index_id"] = nil
			return riskTierTestOutput(t, fields)
		}, expected: &nullIndex},
		{name: "two blocks", text: func(t *testing.T) string {
			return riskTierTestOutput(t, riskTierTestPayloadFields(riskTierTestHead)) + riskTierTestOutput(t, riskTierTestPayloadFields(riskTierTestHead))
		}},
		{name: "opening fence embedded mid-line", text: func(t *testing.T) string {
			return "Payload: " + riskTierTestBlock(t, riskTierTestPayloadFields(riskTierTestHead))
		}},
		{name: "opening fence with trailing text", text: func(t *testing.T) string {
			return strings.Replace(riskTierTestBlock(t, riskTierTestPayloadFields(riskTierTestHead)), CodeReviewRiskTierPayloadFence, CodeReviewRiskTierPayloadFence+" v1", 1)
		}},
		{name: "closing fence with trailing text", text: func(t *testing.T) string {
			return riskTierTestBlock(t, riskTierTestPayloadFields(riskTierTestHead)) + " done"
		}},
		{name: "missing fence", text: func(*testing.T) string { return "```json\n{}\n```" }},
		{name: "unclosed fence", text: func(*testing.T) string { return CodeReviewRiskTierPayloadFence + "\n{}" }},
		{name: "invalid JSON", text: func(*testing.T) string { return CodeReviewRiskTierPayloadFence + "\n{not json}\n```" }},
		{name: "JSON array", text: func(*testing.T) string { return CodeReviewRiskTierPayloadFence + "\n[]\n```" }},
		{name: "JSON null", text: func(*testing.T) string { return CodeReviewRiskTierPayloadFence + "\nnull\n```" }},
		{name: "unknown top-level property", text: mutatedRiskTierOutput(func(f map[string]any) { f["score"] = 1 })},
		{name: "property name differs only by case", text: mutatedRiskTierOutput(func(f map[string]any) { f["Tier"] = f["tier"]; delete(f, "tier") })},
		{name: "missing published_at", text: mutatedRiskTierOutput(func(f map[string]any) { delete(f, "published_at") })},
		{name: "schema version 2", text: mutatedRiskTierOutput(func(f map[string]any) { f["schema_version"] = 2 })},
		{name: "schema version as string", text: mutatedRiskTierOutput(func(f map[string]any) { f["schema_version"] = "1" })},
		{name: "unknown tier", text: mutatedRiskTierOutput(func(f map[string]any) { f["tier"] = "critical" })},
		{name: "unknown gate", text: mutatedRiskTierOutput(func(f map[string]any) { f["gate"] = "failure" })},
		{name: "render change as JSON boolean", text: mutatedRiskTierOutput(func(f map[string]any) { f["ui_render_change"] = false })},
		{name: "override as string", text: mutatedRiskTierOutput(func(f map[string]any) { f["override"] = "false" })},
		{name: "override null", text: mutatedRiskTierOutput(func(f map[string]any) { f["override"] = nil })},
		{name: "published_at not a date-time", text: mutatedRiskTierOutput(func(f map[string]any) { f["published_at"] = "yesterday" })},
		{name: "uppercase head sha", text: mutatedRiskTierGeneration(func(g map[string]any) { g["head_sha"] = strings.ToUpper(riskTierTestHead) })},
		{name: "short head sha", text: mutatedRiskTierGeneration(func(g map[string]any) { g["head_sha"] = riskTierTestHead[:39] })},
		{name: "unknown generation property", text: mutatedRiskTierGeneration(func(g map[string]any) { g["author"] = "someone" })},
		{name: "missing generation mode", text: mutatedRiskTierGeneration(func(g map[string]any) { delete(g, "mode") })},
		{name: "offline mode", text: mutatedRiskTierGeneration(func(g map[string]any) { g["mode"] = "offline" })},
		{name: "zero pull request number", text: mutatedRiskTierGeneration(func(g map[string]any) { g["pr"] = 0 })},
		{name: "fractional pull request number", text: mutatedRiskTierGeneration(func(g map[string]any) { g["pr"] = 1.5 })},
		{name: "pull request number as string", text: mutatedRiskTierGeneration(func(g map[string]any) { g["pr"] = "42" })},
		{name: "malformed index id", text: mutatedRiskTierGeneration(func(g map[string]any) { g["index_id"] = "abc" })},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			payload, err := ExtractCodeReviewRiskTierPayload(tt.text(t))
			if tt.expected == nil {
				require.Error(t, err, "output without exactly one whole-line block that validates against the schema must be rejected")
				return
			}
			require.NoError(t, err, "exactly one schema-valid block should parse")
			require.Equal(t, *tt.expected, payload, "parsed payload should carry every contract field")
		})
	}
}

func mutatedRiskTierOutput(mutate func(map[string]any)) func(t *testing.T) string {
	return func(t *testing.T) string {
		fields := riskTierTestPayloadFields(riskTierTestHead)
		mutate(fields)
		return riskTierTestOutput(t, fields)
	}
}

func mutatedRiskTierGeneration(mutate func(map[string]any)) func(t *testing.T) string {
	return mutatedRiskTierOutput(func(fields map[string]any) { mutate(fields["generation"].(map[string]any)) })
}

func TestCodeReviewRiskTierCheckPolicyValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		policy  *CodeReviewRiskTierCheckPolicy
		message string
	}{
		{name: "absent setting", policy: nil},
		{name: "disabled without app id", policy: &CodeReviewRiskTierCheckPolicy{}},
		{name: "enabled with app id and default check name", policy: &CodeReviewRiskTierCheckPolicy{Enabled: true, AppID: riskTierTestAppID}},
		{name: "enabled with display slug and explicit check name", policy: &CodeReviewRiskTierCheckPolicy{Enabled: true, AppID: riskTierTestAppID, AppSlug: riskTierTestApp, CheckName: "risk-tier / enforce"}},
		{name: "enabled without app id", policy: &CodeReviewRiskTierCheckPolicy{Enabled: true, AppSlug: riskTierTestApp}, message: "risk_tier_check.app_id is required when risk_tier_check is enabled"},
		{name: "negative app id", policy: &CodeReviewRiskTierCheckPolicy{AppID: -1}, message: "risk_tier_check.app_id must be a positive GitHub App ID"},
		{name: "uppercase display slug", policy: &CodeReviewRiskTierCheckPolicy{Enabled: true, AppID: riskTierTestAppID, AppSlug: "Risk-Tier"}, message: "risk_tier_check.app_slug must be a GitHub App slug of lowercase letters, digits, and single hyphens"},
		{name: "display slug with bot suffix", policy: &CodeReviewRiskTierCheckPolicy{Enabled: true, AppID: riskTierTestAppID, AppSlug: "risk-tier[bot]"}, message: "risk_tier_check.app_slug must be a GitHub App slug of lowercase letters, digits, and single hyphens"},
		{name: "check name with surrounding whitespace", policy: &CodeReviewRiskTierCheckPolicy{Enabled: true, AppID: riskTierTestAppID, CheckName: " risk-tier"}, message: "risk_tier_check.check_name must be an exact check name without surrounding whitespace and at most 255 bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.policy.Validate()
			if tt.message == "" {
				require.NoError(t, err, "valid risk-tier settings should pass validation")
				return
			}
			require.EqualError(t, err, tt.message, "invalid risk-tier settings should explain the violation")
			config := DefaultCodeReviewPolicyConfig()
			config.RiskPolicy.RiskTierCheck = tt.policy
			var fieldErr *CodeReviewPolicyValidationError
			require.ErrorAs(t, config.Validate(), &fieldErr, "policy validation should reject the invalid risk-tier setting")
			require.Equal(t, CodeReviewPolicyFieldRiskPolicy, fieldErr.Field, "the validation error should point at risk_policy")
		})
	}
}

func TestValidateCodeReviewPolicyConfigFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		config  string
		message string
	}{
		{name: "no risk policy", config: `{"enabled":true}`},
		{name: "risk policy without the setting", config: `{"risk_policy":{"max_lines_changed":400,"unrelated_legacy_field":true}}`},
		{name: "null setting", config: `{"risk_policy":{"risk_tier_check":null}}`},
		{name: "exact setting", config: `{"risk_policy":{"risk_tier_check":{"enabled":true,"app_id":101,"app_slug":"example-risk-tier","check_name":"risk-tier"}}}`},
		{name: "misspelled check name", config: `{"risk_policy":{"risk_tier_check":{"enabled":true,"app_id":101,"check_nam":"risk-tier"}}}`, message: `risk_tier_check: json: unknown field "check_nam"`},
		{name: "retired slug-only binding", config: `{"risk_policy":{"risk_tier_check":{"enabled":true,"app":"example-risk-tier"}}}`, message: `risk_tier_check: json: unknown field "app"`},
		{name: "wrong value type", config: `{"risk_policy":{"risk_tier_check":{"enabled":true,"app_id":"101"}}}`, message: "risk_tier_check: json: cannot unmarshal string into Go struct field CodeReviewRiskTierCheckPolicy.app_id of type int64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateCodeReviewPolicyConfigFields(json.RawMessage(tt.config))
			if tt.message == "" {
				require.NoError(t, err, "well-formed settings should pass the write-boundary check")
				return
			}
			var fieldErr *CodeReviewPolicyValidationError
			require.ErrorAs(t, err, &fieldErr, "a malformed risk-tier setting should be a policy validation error")
			require.Equal(t, CodeReviewPolicyFieldRiskPolicy, fieldErr.Field, "the error should point at risk_policy")
			require.EqualError(t, err, tt.message, "the error should name the offending field")
		})
	}
}

func TestCodeReviewRiskTierCheckPolicyPersistence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		patch       string
		expected    *CodeReviewRiskTierCheckPolicy
		expectError bool
	}{
		{name: "absent setting keeps today's policy", patch: `{"inline_comment_limit":4}`},
		{name: "enabled setting round trips", patch: `{"risk_policy":{"risk_tier_check":{"enabled":true,"app_id":101,"app_slug":"example-risk-tier"}}}`, expected: &CodeReviewRiskTierCheckPolicy{Enabled: true, AppID: riskTierTestAppID, AppSlug: riskTierTestApp}},
		{name: "enabled setting requires app id", patch: `{"risk_policy":{"risk_tier_check":{"enabled":true,"app_slug":"example-risk-tier"}}}`, expectError: true},
		{name: "misspelled field is rejected", patch: `{"risk_policy":{"risk_tier_check":{"enabled":true,"app_id":101,"check_nam":"pr-risk"}}}`, expectError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			patched, err := ApplyCodeReviewPolicyMergePatch(DefaultCodeReviewPolicyConfig(), json.RawMessage(tt.patch))
			if tt.expectError {
				require.Error(t, err, "invalid risk-tier settings must not be saved")
				return
			}
			require.NoError(t, err, "valid patches should apply")
			require.Equal(t, tt.expected, patched.RiskPolicy.RiskTierCheck, "the risk-tier setting should survive patching and resolution")
			encoded, err := json.Marshal(patched.RiskPolicy)
			require.NoError(t, err, "risk policy should encode")
			require.Equal(t, tt.expected != nil, strings.Contains(string(encoded), `"risk_tier_check"`), "an absent setting must not change the stored risk policy shape")
			var decoded CodeReviewRiskPolicy
			require.NoError(t, json.Unmarshal(encoded, &decoded), "stored risk policy should decode")
			require.Equal(t, patched.RiskPolicy, ResolveCodeReviewPolicyConfig(&CodeReviewPolicyConfig{RiskPolicy: decoded}).RiskPolicy, "resolution should preserve the stored setting")
		})
	}
}

func TestResolveCodeReviewRiskTierSignal(t *testing.T) {
	t.Parallel()
	enabled := &CodeReviewRiskTierCheckPolicy{Enabled: true, AppID: riskTierTestAppID, AppSlug: riskTierTestApp}
	run := func(t *testing.T, id int64, tier, render, mode string) CodeReviewCheckRun {
		return riskTierTestCheckRun(t, id, riskTierTestAppID, riskTierTestHead, tier, render, mode)
	}
	enforce := CodeReviewRiskTierModeEnforce
	tests := []struct {
		name     string
		policy   *CodeReviewRiskTierCheckPolicy
		runs     func(t *testing.T) []CodeReviewCheckRun
		expected CodeReviewRiskTierSignal
	}{
		{name: "setting absent", policy: nil, runs: func(t *testing.T) []CodeReviewCheckRun {
			return []CodeReviewCheckRun{run(t, 1, "low", "false", "enforce")}
		}},
		{name: "setting disabled", policy: &CodeReviewRiskTierCheckPolicy{AppID: riskTierTestAppID}, runs: func(t *testing.T) []CodeReviewCheckRun {
			return []CodeReviewCheckRun{run(t, 1, "low", "false", "enforce")}
		}},
		{name: "no check run", policy: enabled, runs: func(*testing.T) []CodeReviewCheckRun { return nil }, expected: riskTierTestSignal(CodeReviewRiskTierSourceMissing, "", "", CodeReviewRiskTierObservation{Source: CodeReviewRiskTierSourceMissing})},
		{name: "other check names are ignored", policy: enabled, runs: func(t *testing.T) []CodeReviewCheckRun {
			other := run(t, 1, "low", "false", "enforce")
			other.Name = "Risk-Tier"
			return []CodeReviewCheckRun{other}
		}, expected: riskTierTestSignal(CodeReviewRiskTierSourceMissing, "", "", CodeReviewRiskTierObservation{Source: CodeReviewRiskTierSourceMissing})},
		{name: "same name and slug from another app id", policy: enabled, runs: func(t *testing.T) []CodeReviewCheckRun {
			return []CodeReviewCheckRun{riskTierTestCheckRun(t, 9, 202, riskTierTestHead, "low", "false", "enforce")}
		}, expected: riskTierTestSignal(CodeReviewRiskTierSourceUntrustedApp, "", "", CodeReviewRiskTierObservation{Source: CodeReviewRiskTierSourceUntrustedApp})},
		{name: "enforce low", policy: enabled, runs: func(t *testing.T) []CodeReviewCheckRun {
			return []CodeReviewCheckRun{run(t, 2, "low", "false", "enforce")}
		},
			expected: riskTierTestSignal(CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierLow, CodeReviewUIRenderChangeFalse, riskTierTestObservation(2, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierLow, CodeReviewUIRenderChangeFalse, enforce))},
		{name: "shadow low is not authoritative", policy: enabled, runs: func(t *testing.T) []CodeReviewCheckRun {
			return []CodeReviewCheckRun{run(t, 2, "low", "false", "shadow")}
		},
			expected: riskTierTestSignal(CodeReviewRiskTierSourceShadowMode, "", "", riskTierTestObservation(2, CodeReviewRiskTierSourceShadowMode, CodeReviewRiskTierLow, CodeReviewUIRenderChangeFalse, CodeReviewRiskTierModeShadow))},
		{name: "older high and newer low keeps high", policy: enabled, runs: func(t *testing.T) []CodeReviewCheckRun {
			return []CodeReviewCheckRun{run(t, 7, "high", "false", "enforce"), run(t, 8, "low", "false", "enforce")}
		}, expected: riskTierTestSignal(CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierHigh, CodeReviewUIRenderChangeFalse,
			riskTierTestObservation(7, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierHigh, CodeReviewUIRenderChangeFalse, enforce),
			riskTierTestObservation(8, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierLow, CodeReviewUIRenderChangeFalse, enforce))},
		{name: "older low and newer high keeps high", policy: enabled, runs: func(t *testing.T) []CodeReviewCheckRun {
			return []CodeReviewCheckRun{run(t, 8, "high", "false", "enforce"), run(t, 7, "low", "false", "enforce")}
		}, expected: riskTierTestSignal(CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierHigh, CodeReviewUIRenderChangeFalse,
			riskTierTestObservation(7, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierLow, CodeReviewUIRenderChangeFalse, enforce),
			riskTierTestObservation(8, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierHigh, CodeReviewUIRenderChangeFalse, enforce))},
		{name: "unscored outranks medium", policy: enabled, runs: func(t *testing.T) []CodeReviewCheckRun {
			return []CodeReviewCheckRun{run(t, 7, "medium", "false", "enforce"), run(t, 8, "unscored", "false", "enforce")}
		}, expected: riskTierTestSignal(CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierUnscored, CodeReviewUIRenderChangeFalse,
			riskTierTestObservation(7, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierMedium, CodeReviewUIRenderChangeFalse, enforce),
			riskTierTestObservation(8, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierUnscored, CodeReviewUIRenderChangeFalse, enforce))},
		{name: "older true and newer false keeps true", policy: enabled, runs: func(t *testing.T) []CodeReviewCheckRun {
			return []CodeReviewCheckRun{run(t, 7, "low", "true", "enforce"), run(t, 8, "low", "false", "enforce")}
		}, expected: riskTierTestSignal(CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierLow, CodeReviewUIRenderChangeTrue,
			riskTierTestObservation(7, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierLow, CodeReviewUIRenderChangeTrue, enforce),
			riskTierTestObservation(8, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierLow, CodeReviewUIRenderChangeFalse, enforce))},
		{name: "older false and newer true keeps true", policy: enabled, runs: func(t *testing.T) []CodeReviewCheckRun {
			return []CodeReviewCheckRun{run(t, 7, "low", "false", "enforce"), run(t, 8, "low", "true", "enforce")}
		}, expected: riskTierTestSignal(CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierLow, CodeReviewUIRenderChangeTrue,
			riskTierTestObservation(7, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierLow, CodeReviewUIRenderChangeFalse, enforce),
			riskTierTestObservation(8, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierLow, CodeReviewUIRenderChangeTrue, enforce))},
		{name: "unknown outranks false", policy: enabled, runs: func(t *testing.T) []CodeReviewCheckRun {
			return []CodeReviewCheckRun{run(t, 7, "low", "unknown", "enforce"), run(t, 8, "low", "false", "enforce")}
		}, expected: riskTierTestSignal(CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierLow, CodeReviewUIRenderChangeUnknown,
			riskTierTestObservation(7, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierLow, CodeReviewUIRenderChangeUnknown, enforce),
			riskTierTestObservation(8, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierLow, CodeReviewUIRenderChangeFalse, enforce))},
		{name: "an unparsable configured-app run makes the result non-authoritative", policy: enabled, runs: func(t *testing.T) []CodeReviewCheckRun {
			broken := run(t, 5, "low", "false", "enforce")
			broken.OutputText = "scoring in progress"
			return []CodeReviewCheckRun{run(t, 4, "low", "false", "enforce"), broken}
		}, expected: riskTierTestSignal(CodeReviewRiskTierSourceUnparsable, CodeReviewRiskTierLow, CodeReviewUIRenderChangeFalse,
			riskTierTestObservation(4, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierLow, CodeReviewUIRenderChangeFalse, enforce),
			CodeReviewRiskTierObservation{CheckRunID: 5, Source: CodeReviewRiskTierSourceUnparsable})},
		{name: "payload generated for another head", policy: enabled, runs: func(t *testing.T) []CodeReviewCheckRun {
			return []CodeReviewCheckRun{riskTierTestCheckRun(t, 3, riskTierTestAppID, riskTierTestOther, "low", "false", "enforce")}
		}, expected: func() CodeReviewRiskTierSignal {
			observation := riskTierTestObservation(3, CodeReviewRiskTierSourceStaleHead, CodeReviewRiskTierLow, CodeReviewUIRenderChangeFalse, enforce)
			observation.HeadSHA = riskTierTestOther
			return riskTierTestSignal(CodeReviewRiskTierSourceStaleHead, "", "", observation)
		}()},
		{name: "check run attached to another commit", policy: enabled, runs: func(t *testing.T) []CodeReviewCheckRun {
			moved := run(t, 3, "low", "false", "enforce")
			moved.HeadSHA = riskTierTestOther
			return []CodeReviewCheckRun{moved}
		}, expected: riskTierTestSignal(CodeReviewRiskTierSourceStaleHead, "", "", CodeReviewRiskTierObservation{CheckRunID: 3, Source: CodeReviewRiskTierSourceStaleHead})},
		{name: "configured check name", policy: &CodeReviewRiskTierCheckPolicy{Enabled: true, AppID: riskTierTestAppID, AppSlug: riskTierTestApp, CheckName: "pr-risk"}, runs: func(t *testing.T) []CodeReviewCheckRun {
			named := run(t, 6, "unscored", "unknown", "enforce")
			named.Name = "pr-risk"
			return []CodeReviewCheckRun{run(t, 7, "low", "false", "enforce"), named}
		}, expected: func() CodeReviewRiskTierSignal {
			signal := riskTierTestSignal(CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierUnscored, CodeReviewUIRenderChangeUnknown, riskTierTestObservation(6, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierUnscored, CodeReviewUIRenderChangeUnknown, enforce))
			signal.CheckName = "pr-risk"
			return signal
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, ResolveCodeReviewRiskTierSignal(tt.policy, strings.ToUpper(riskTierTestHead), tt.runs(t)), "every enforce-mode run from the configured App ID should combine by the most restrictive result")
		})
	}
}

func TestCodeReviewRiskTierSignalWithLive(t *testing.T) {
	t.Parallel()
	enabled := &CodeReviewRiskTierCheckPolicy{Enabled: true, AppID: riskTierTestAppID, AppSlug: riskTierTestApp}
	enforce := CodeReviewRiskTierModeEnforce
	resolve := func(t *testing.T, tier, render string) CodeReviewRiskTierSignal {
		return ResolveCodeReviewRiskTierSignal(enabled, riskTierTestHead, []CodeReviewCheckRun{riskTierTestCheckRun(t, 4, riskTierTestAppID, riskTierTestHead, tier, render, "enforce")})
	}
	live := func(observation CodeReviewRiskTierObservation) CodeReviewRiskTierObservation {
		observation.Live = true
		return observation
	}
	tests := []struct {
		name       string
		checkpoint func(t *testing.T) CodeReviewRiskTierSignal
		live       func(t *testing.T) CodeReviewRiskTierSignal
		expected   CodeReviewRiskTierSignal
		waives     bool
	}{
		{name: "unchanged live result keeps the checkpoint", checkpoint: func(t *testing.T) CodeReviewRiskTierSignal { return resolve(t, "low", "false") }, live: func(t *testing.T) CodeReviewRiskTierSignal { return resolve(t, "low", "false") },
			expected: riskTierTestSignal(CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierLow, CodeReviewUIRenderChangeFalse, riskTierTestObservation(4, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierLow, CodeReviewUIRenderChangeFalse, enforce)), waives: true},
		{name: "live rescore to high revokes the waiver", checkpoint: func(t *testing.T) CodeReviewRiskTierSignal { return resolve(t, "low", "false") }, live: func(t *testing.T) CodeReviewRiskTierSignal { return resolve(t, "high", "false") },
			expected: riskTierTestSignal(CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierHigh, CodeReviewUIRenderChangeFalse,
				riskTierTestObservation(4, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierLow, CodeReviewUIRenderChangeFalse, enforce),
				live(riskTierTestObservation(4, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierHigh, CodeReviewUIRenderChangeFalse, enforce)))},
		{name: "live rescore to true requires screenshots", checkpoint: func(t *testing.T) CodeReviewRiskTierSignal { return resolve(t, "low", "false") }, live: func(t *testing.T) CodeReviewRiskTierSignal { return resolve(t, "low", "true") },
			expected: riskTierTestSignal(CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierLow, CodeReviewUIRenderChangeTrue,
				riskTierTestObservation(4, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierLow, CodeReviewUIRenderChangeFalse, enforce),
				live(riskTierTestObservation(4, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierLow, CodeReviewUIRenderChangeTrue, enforce))), waives: true},
		{name: "live rescore to low cannot grant a waiver late", checkpoint: func(t *testing.T) CodeReviewRiskTierSignal { return resolve(t, "high", "false") }, live: func(t *testing.T) CodeReviewRiskTierSignal { return resolve(t, "low", "false") },
			expected: riskTierTestSignal(CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierHigh, CodeReviewUIRenderChangeFalse,
				riskTierTestObservation(4, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierHigh, CodeReviewUIRenderChangeFalse, enforce),
				live(riskTierTestObservation(4, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierLow, CodeReviewUIRenderChangeFalse, enforce)))},
		{name: "missing checkpoint stays non-authoritative when the publisher catches up", checkpoint: func(*testing.T) CodeReviewRiskTierSignal {
			return ResolveCodeReviewRiskTierSignal(enabled, riskTierTestHead, nil)
		}, live: func(t *testing.T) CodeReviewRiskTierSignal { return resolve(t, "low", "false") },
			expected: riskTierTestSignal(CodeReviewRiskTierSourceMissing, CodeReviewRiskTierLow, CodeReviewUIRenderChangeFalse,
				CodeReviewRiskTierObservation{Source: CodeReviewRiskTierSourceMissing},
				live(riskTierTestObservation(4, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierLow, CodeReviewUIRenderChangeFalse, enforce)))},
		{name: "lookup failure after a waived checkpoint applies the limits", checkpoint: func(t *testing.T) CodeReviewRiskTierSignal { return resolve(t, "low", "false") }, live: func(*testing.T) CodeReviewRiskTierSignal {
			return CodeReviewRiskTierLookupFailedSignal(enabled, riskTierTestHead)
		}, expected: riskTierTestSignal(CodeReviewRiskTierSourceLookupFailed, CodeReviewRiskTierLow, CodeReviewUIRenderChangeFalse,
			riskTierTestObservation(4, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierLow, CodeReviewUIRenderChangeFalse, enforce),
			CodeReviewRiskTierObservation{Source: CodeReviewRiskTierSourceLookupFailed, Live: true})},
		{name: "lookup failure after an applied checkpoint keeps applying the limits", checkpoint: func(t *testing.T) CodeReviewRiskTierSignal { return resolve(t, "high", "true") }, live: func(*testing.T) CodeReviewRiskTierSignal {
			return CodeReviewRiskTierLookupFailedSignal(enabled, riskTierTestHead)
		}, expected: riskTierTestSignal(CodeReviewRiskTierSourceLookupFailed, CodeReviewRiskTierHigh, CodeReviewUIRenderChangeTrue,
			riskTierTestObservation(4, CodeReviewRiskTierSourceAuthoritative, CodeReviewRiskTierHigh, CodeReviewUIRenderChangeTrue, enforce),
			CodeReviewRiskTierObservation{Source: CodeReviewRiskTierSourceLookupFailed, Live: true})},
		{name: "disabled checkpoint ignores live state", checkpoint: func(*testing.T) CodeReviewRiskTierSignal { return CodeReviewRiskTierSignal{} }, live: func(t *testing.T) CodeReviewRiskTierSignal { return resolve(t, "high", "true") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			combined := tt.checkpoint(t).WithLive(tt.live(t))
			require.Equal(t, tt.expected, combined, "the live read should only ever make the checkpoint more restrictive and is recorded when it differs")
			require.Equal(t, tt.waives, combined.WaivesSizeSafeguard(), "only an authoritative low or medium across checkpoint and live state waives the limits")
		})
	}
}

func TestEvaluateCodeReviewRiskTierConditionalSizeSafeguard(t *testing.T) {
	t.Parallel()
	oversized := CodeReviewRiskInput{FilesChanged: 6, Additions: 301, Deletions: 12, DescriptionPassed: true}
	sizeReasons := []CodeReviewRiskReason{{Code: CodeReviewRiskReasonFilesLimitExceeded, Actual: 6, Limit: 5}, {Code: CodeReviewRiskReasonAdditionsLimitExceeded, Actual: 301, Limit: 300}}
	oversizedMeasurements := [3]CodeReviewSizeMeasurement{{Actual: 6, Limit: 5, Exceeded: true}, {Actual: 301, Limit: 300, Exceeded: true}, {Actual: 12, Limit: 300}}
	indexID := riskTierTestHash
	publishedAt := riskTierTestPublishedAt
	enabled := &CodeReviewRiskTierCheckPolicy{Enabled: true, AppID: riskTierTestAppID, AppSlug: riskTierTestApp}
	resolved := func(t *testing.T, tier, mode string) *CodeReviewRiskTierSignal {
		signal := ResolveCodeReviewRiskTierSignal(enabled, riskTierTestHead, []CodeReviewCheckRun{riskTierTestCheckRun(t, 11, riskTierTestAppID, riskTierTestHead, tier, "unknown", mode)})
		return &signal
	}
	unauthoritative := func(source CodeReviewRiskTierSource) func(*testing.T) *CodeReviewRiskTierSignal {
		return func(*testing.T) *CodeReviewRiskTierSignal {
			signal := riskTierTestSignal(source, "", "", CodeReviewRiskTierObservation{Source: source})
			return &signal
		}
	}
	record := func(signal *CodeReviewRiskTierSignal, outcome CodeReviewSizeSafeguardOutcome, measurements [3]CodeReviewSizeMeasurement, deciding bool) CodeReviewRiskReason {
		decision := CodeReviewSizeSafeguardDecision{
			Outcome: outcome, Source: signal.Source, Tier: signal.Tier, HeadSHA: riskTierTestHead, AppID: riskTierTestAppID,
			Files: measurements[0], Additions: measurements[1], Deletions: measurements[2], Observations: signal.Observations,
		}
		if deciding {
			decision.CheckRunID, decision.Mode, decision.IndexID, decision.PolicyHash, decision.PublishedAt = 11, signal.Observations[0].Mode, &indexID, riskTierTestHash, &publishedAt
		}
		return CodeReviewRiskReason{Code: CodeReviewRiskReasonSizeSafeguard, SizeSafeguard: &decision}
	}
	tests := []struct {
		name       string
		input      CodeReviewRiskInput
		signal     func(t *testing.T) *CodeReviewRiskTierSignal
		acceptable bool
		expected   func(signal *CodeReviewRiskTierSignal) []CodeReviewRiskReason
	}{
		{name: "no signal keeps today's limits", input: oversized, signal: func(*testing.T) *CodeReviewRiskTierSignal { return nil }, expected: func(*CodeReviewRiskTierSignal) []CodeReviewRiskReason { return sizeReasons }},
		{name: "disabled signal keeps today's limits", input: oversized, signal: func(*testing.T) *CodeReviewRiskTierSignal { return &CodeReviewRiskTierSignal{} }, expected: func(*CodeReviewRiskTierSignal) []CodeReviewRiskReason { return sizeReasons }},
		{name: "enforce low waives the limits", input: oversized, signal: func(t *testing.T) *CodeReviewRiskTierSignal { return resolved(t, "low", "enforce") }, acceptable: true,
			expected: func(s *CodeReviewRiskTierSignal) []CodeReviewRiskReason {
				return []CodeReviewRiskReason{record(s, CodeReviewSizeSafeguardWaived, oversizedMeasurements, true)}
			}},
		{name: "enforce medium waives the limits", input: oversized, signal: func(t *testing.T) *CodeReviewRiskTierSignal { return resolved(t, "medium", "enforce") }, acceptable: true,
			expected: func(s *CodeReviewRiskTierSignal) []CodeReviewRiskReason {
				return []CodeReviewRiskReason{record(s, CodeReviewSizeSafeguardWaived, oversizedMeasurements, true)}
			}},
		{name: "shadow low applies the limits", input: oversized, signal: func(t *testing.T) *CodeReviewRiskTierSignal { return resolved(t, "low", "shadow") },
			expected: func(s *CodeReviewRiskTierSignal) []CodeReviewRiskReason {
				return append(append([]CodeReviewRiskReason{}, sizeReasons...), record(s, CodeReviewSizeSafeguardApplied, oversizedMeasurements, true))
			}},
		{name: "high applies the limits", input: oversized, signal: func(t *testing.T) *CodeReviewRiskTierSignal { return resolved(t, "high", "enforce") },
			expected: func(s *CodeReviewRiskTierSignal) []CodeReviewRiskReason {
				return append(append([]CodeReviewRiskReason{}, sizeReasons...), record(s, CodeReviewSizeSafeguardApplied, oversizedMeasurements, true))
			}},
		{name: "unavailable applies the limits", input: oversized, signal: func(t *testing.T) *CodeReviewRiskTierSignal { return resolved(t, "unavailable", "enforce") },
			expected: func(s *CodeReviewRiskTierSignal) []CodeReviewRiskReason {
				return append(append([]CodeReviewRiskReason{}, sizeReasons...), record(s, CodeReviewSizeSafeguardApplied, oversizedMeasurements, true))
			}},
		{name: "unscored applies the limits", input: oversized, signal: func(t *testing.T) *CodeReviewRiskTierSignal { return resolved(t, "unscored", "enforce") },
			expected: func(s *CodeReviewRiskTierSignal) []CodeReviewRiskReason {
				return append(append([]CodeReviewRiskReason{}, sizeReasons...), record(s, CodeReviewSizeSafeguardApplied, oversizedMeasurements, true))
			}},
		{name: "missing check applies the limits", input: oversized, signal: unauthoritative(CodeReviewRiskTierSourceMissing),
			expected: func(s *CodeReviewRiskTierSignal) []CodeReviewRiskReason {
				return append(append([]CodeReviewRiskReason{}, sizeReasons...), record(s, CodeReviewSizeSafeguardApplied, oversizedMeasurements, false))
			}},
		{name: "lookup failure applies the limits", input: oversized, signal: unauthoritative(CodeReviewRiskTierSourceLookupFailed),
			expected: func(s *CodeReviewRiskTierSignal) []CodeReviewRiskReason {
				return append(append([]CodeReviewRiskReason{}, sizeReasons...), record(s, CodeReviewSizeSafeguardApplied, oversizedMeasurements, false))
			}},
		{name: "small change records every measurement without exceeded limits", input: CodeReviewRiskInput{FilesChanged: 1, Additions: 3, DescriptionPassed: true}, signal: func(t *testing.T) *CodeReviewRiskTierSignal { return resolved(t, "high", "enforce") }, acceptable: true,
			expected: func(s *CodeReviewRiskTierSignal) []CodeReviewRiskReason {
				return []CodeReviewRiskReason{record(s, CodeReviewSizeSafeguardApplied, [3]CodeReviewSizeMeasurement{{Actual: 1, Limit: 5}, {Actual: 3, Limit: 300}, {Actual: 0, Limit: 300}}, true)}
			}},
		{name: "waiver does not hide other safeguards", input: CodeReviewRiskInput{FilesChanged: 6, Additions: 301, Deletions: 12}, signal: func(t *testing.T) *CodeReviewRiskTierSignal { return resolved(t, "low", "enforce") },
			expected: func(s *CodeReviewRiskTierSignal) []CodeReviewRiskReason {
				return []CodeReviewRiskReason{record(s, CodeReviewSizeSafeguardWaived, oversizedMeasurements, true), {Code: CodeReviewRiskReasonDescriptionFailed}}
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := tt.input
			input.RiskTier = tt.signal(t)
			risk := EvaluateCodeReviewRisk(DefaultCodeReviewPolicyConfig(), input)
			require.Equal(t, tt.expected(input.RiskTier), risk.ReasonDetails, "the size safeguard should follow the enforce-mode tier and archive every measurement and the deciding generation")
			require.Equal(t, tt.acceptable, risk.Acceptable, "only a waived or unexceeded size safeguard should leave the review acceptable")
			require.NotContains(t, risk.Reasons, "", "informational safeguard records must not add blocker messages")
		})
	}
}

func TestCodeReviewInformationalRiskReasonsStayOutOfBlockers(t *testing.T) {
	t.Parallel()
	waived := CodeReviewRiskReason{Code: CodeReviewRiskReasonSizeSafeguard, SizeSafeguard: &CodeReviewSizeSafeguardDecision{Outcome: CodeReviewSizeSafeguardWaived, Source: CodeReviewRiskTierSourceAuthoritative, Tier: CodeReviewRiskTierLow, HeadSHA: riskTierTestHead}}
	blocking := CodeReviewRiskReason{Code: CodeReviewRiskReasonBlockedPath, Subject: "deploy/prod.yml"}
	approvePolicy := DefaultCodeReviewPolicyConfig()
	approvePolicy.ApprovalMode = CodeReviewApprovalModeApproveAcceptable
	tests := []struct {
		name     string
		policy   CodeReviewPolicyConfig
		risk     CodeReviewRiskEvaluation
		expected CodeReviewDecisionEvaluation
	}{
		{name: "approved review keeps the safeguard record", policy: approvePolicy, risk: CodeReviewRiskEvaluation{Acceptable: true, ReasonDetails: []CodeReviewRiskReason{waived}}, expected: CodeReviewDecisionEvaluation{Decision: CodeReviewDecisionApproved, Acceptable: true, RiskReasonDetails: []CodeReviewRiskReason{waived}}},
		{name: "comment-only review keeps the safeguard record", policy: DefaultCodeReviewPolicyConfig(), risk: CodeReviewRiskEvaluation{Acceptable: true, ReasonDetails: []CodeReviewRiskReason{waived}}, expected: CodeReviewDecisionEvaluation{Decision: CodeReviewDecisionCommentOnly, Acceptable: true, RiskReasonDetails: []CodeReviewRiskReason{waived}}},
		{name: "approved review without a record is unchanged", policy: approvePolicy, risk: CodeReviewRiskEvaluation{Acceptable: true, ReasonDetails: []CodeReviewRiskReason{}}, expected: CodeReviewDecisionEvaluation{Decision: CodeReviewDecisionApproved, Acceptable: true}},
		{name: "blocked review keeps blockers and the record", policy: approvePolicy, risk: CodeReviewRiskEvaluation{Reasons: []string{blocking.Message()}, ReasonDetails: []CodeReviewRiskReason{waived, blocking}}, expected: CodeReviewDecisionEvaluation{Decision: CodeReviewDecisionNeedsHumanReview, RiskReasons: []string{blocking.Message()}, RiskReasonDetails: []CodeReviewRiskReason{waived, blocking}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			decision := EvaluateCodeReviewDecision(tt.policy, tt.risk)
			require.Equal(t, tt.expected, decision, "decisions should archive the safeguard record without treating it as a blocker")
			assessedAt := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
			withRecord := CodeReviewFinalReviewInput{Decision: decision.Decision, Acceptable: decision.Acceptable, RiskReasons: decision.RiskReasonDetails, HeadSHA: riskTierTestHead, AssessedAt: assessedAt}
			withoutRecord := withRecord
			withoutRecord.RiskReasons = CodeReviewBlockingRiskReasons(decision.RiskReasonDetails)
			require.Equal(t, BuildCodeReviewFinalReviewBody(withoutRecord), BuildCodeReviewFinalReviewBody(withRecord), "the GitHub review body should not mention informational records")
			require.Equal(t, BuildCodeReviewProvisionalBody(withoutRecord), BuildCodeReviewProvisionalBody(withRecord), "the provisional body should not mention informational records")
			require.Equal(t, CodeReviewRiskReasonMessages(CodeReviewBlockingRiskReasons(decision.RiskReasonDetails)), CodeReviewRiskReasonMessages(decision.RiskReasonDetails), "prompt risk messages should skip informational records")
		})
	}
	require.NoError(t, CodeReviewRiskReasonSizeSafeguard.Validate(), "the safeguard record code must be a valid API value")
	require.False(t, IsCodeReviewStableDeterministicRiskReason(CodeReviewRiskReasonSizeSafeguard), "the safeguard record depends on external check state and is not a stable blocker")
	require.Equal(t, []string{"size_safeguard"}, CodeReviewInformationalRiskReasonStrings(), "SQL filters should exclude exactly the informational codes")
}
