package github

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestEvaluatePRFeedbackEligibility(t *testing.T) {
	t.Parallel()

	baseHuman := prFeedbackEligibilityInput{HumanMode: models.PRFeedbackHumanModeAllTrusted, BotMode: models.PRFeedbackBotModeAll, AuthorLogin: "octocat", AuthorType: models.PRFeedbackAuthorTypeUser, Association: "MEMBER", Body: "Please add a regression test"}
	baseBot := prFeedbackEligibilityInput{HumanMode: models.PRFeedbackHumanModeAllTrusted, BotMode: models.PRFeedbackBotModeAll, AuthorLogin: "reviewer[bot]", AuthorType: models.PRFeedbackAuthorTypeBot, Body: "Possible nil dereference"}
	tests := []struct {
		name     string
		input    prFeedbackEligibilityInput
		expected prFeedbackEligibility
	}{
		{name: "trusted human", input: baseHuman, expected: prFeedbackEligibility{Eligible: true}},
		{name: "untrusted human requires mention", input: withEligibility(baseHuman, func(v *prFeedbackEligibilityInput) { v.Association = "NONE" }), expected: prFeedbackEligibility{IgnoreReason: "untrusted_human_without_mention"}},
		{name: "untrusted mentioned human", input: withEligibility(baseHuman, func(v *prFeedbackEligibilityInput) { v.Association = "NONE"; v.Mentioned = true }), expected: prFeedbackEligibility{Eligible: true}},
		{name: "mention mode", input: withEligibility(baseHuman, func(v *prFeedbackEligibilityInput) { v.HumanMode = models.PRFeedbackHumanModeMentions }), expected: prFeedbackEligibility{IgnoreReason: "mention_required"}},
		{name: "self authored", input: withEligibility(baseBot, func(v *prFeedbackEligibilityInput) { v.OwnAppLogin = "reviewer" }), expected: prFeedbackEligibility{IgnoreReason: "self_authored"}},
		{name: "private bot", input: withEligibility(baseBot, func(v *prFeedbackEligibilityInput) { v.PrivateRepo = true }), expected: prFeedbackEligibility{Eligible: true, BotEligibility: models.PRFeedbackBotEligibilityPrivateAll}},
		{name: "public first party bot", input: withEligibility(baseBot, func(v *prFeedbackEligibilityInput) { v.AuthorLogin = "dependabot[bot]" }), expected: prFeedbackEligibility{Eligible: true, BotEligibility: models.PRFeedbackBotEligibilityGitHubFirstParty}},
		{name: "public installed app", input: withEligibility(baseBot, func(v *prFeedbackEligibilityInput) { v.InstalledApp = true }), expected: prFeedbackEligibility{Eligible: true, BotEligibility: models.PRFeedbackBotEligibilityInstalledApp}},
		{name: "public unverified bot", input: baseBot, expected: prFeedbackEligibility{IgnoreReason: "public_bot_provenance_unverified"}},
		{name: "explicit allowlist", input: withEligibility(baseBot, func(v *prFeedbackEligibilityInput) {
			v.BotMode = models.PRFeedbackBotModeAllowlist
			v.BotAllowlist = []string{"Reviewer[bot]"}
		}), expected: prFeedbackEligibility{Eligible: true, BotEligibility: models.PRFeedbackBotEligibilityAllowlist}},
		{name: "empty", input: withEligibility(baseHuman, func(v *prFeedbackEligibilityInput) { v.Body = "" }), expected: prFeedbackEligibility{IgnoreReason: "empty_or_deleted"}},
		{name: "hidden marker", input: withEligibility(baseHuman, func(v *prFeedbackEligibilityInput) { v.Body = "done " + prFeedbackHiddenMarker + "x -->" }), expected: prFeedbackEligibility{IgnoreReason: "hidden_response_marker"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, evaluatePRFeedbackEligibility(tt.input), "eligibility should apply provenance and noise policy")
		})
	}
}

func TestDeterministicPRFeedbackTriage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		classified bool
		intent     models.PRFeedbackIntent
	}{
		{name: "thanks", body: "Thanks!", classified: true, intent: models.PRFeedbackIntentAcknowledgement},
		{name: "emoji", body: "👍", classified: true, intent: models.PRFeedbackIntentAcknowledgement},
		{name: "successful checks", body: "All checks passed", classified: true, intent: models.PRFeedbackIntentAcknowledgement},
		{name: "change request", body: "Please add a regression test", classified: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, classified := deterministicPRFeedbackTriage(models.PullRequestFeedbackItem{Body: tt.body})
			require.Equal(t, tt.classified, classified, "deterministic triage should classify only exact noise patterns")
			require.Equal(t, tt.intent, result.Intent, "deterministic triage should return expected intent")
		})
	}
}

func withEligibility(input prFeedbackEligibilityInput, mutate func(*prFeedbackEligibilityInput)) prFeedbackEligibilityInput {
	mutate(&input)
	return input
}
