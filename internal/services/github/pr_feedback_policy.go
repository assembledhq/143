package github

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/assembledhq/143/internal/models"
)

const prFeedbackHiddenMarker = "<!-- 143:pr-feedback:"

type prFeedbackEligibilityInput struct {
	HumanMode    models.PRFeedbackHumanMode
	BotMode      models.PRFeedbackBotMode
	BotAllowlist []string
	PrivateRepo  bool
	AuthorLogin  string
	AuthorType   models.PRFeedbackAuthorType
	Association  string
	InstalledApp bool
	Mentioned    bool
	OwnAppLogin  string
	Body         string
	Deleted      bool
}

type prFeedbackEligibility struct {
	Eligible       bool
	IgnoreReason   string
	BotEligibility models.PRFeedbackBotEligibilitySource
}

func evaluatePRFeedbackEligibility(input prFeedbackEligibilityInput) prFeedbackEligibility {
	login := canonicalBotLogin(input.AuthorLogin)
	if login == canonicalBotLogin(input.OwnAppLogin) && login != "" {
		return prFeedbackEligibility{IgnoreReason: "self_authored"}
	}
	if input.Deleted || strings.TrimSpace(input.Body) == "" {
		return prFeedbackEligibility{IgnoreReason: "empty_or_deleted"}
	}
	if strings.Contains(input.Body, prFeedbackHiddenMarker) {
		return prFeedbackEligibility{IgnoreReason: "hidden_response_marker"}
	}
	if input.AuthorType != models.PRFeedbackAuthorTypeBot {
		if input.HumanMode == models.PRFeedbackHumanModeOff {
			return prFeedbackEligibility{IgnoreReason: "human_mode_off"}
		}
		trusted := input.Association == "OWNER" || input.Association == "MEMBER" || input.Association == "COLLABORATOR"
		if !trusted && !input.Mentioned {
			return prFeedbackEligibility{IgnoreReason: "untrusted_human_without_mention"}
		}
		if input.HumanMode == models.PRFeedbackHumanModeMentions && !input.Mentioned {
			return prFeedbackEligibility{IgnoreReason: "mention_required"}
		}
		return prFeedbackEligibility{Eligible: true}
	}
	if input.BotMode == models.PRFeedbackBotModeNone {
		return prFeedbackEligibility{IgnoreReason: "bot_mode_none"}
	}
	allowlisted := containsCanonicalLogin(input.BotAllowlist, login)
	if input.BotMode == models.PRFeedbackBotModeAllowlist && !allowlisted {
		return prFeedbackEligibility{IgnoreReason: "bot_not_allowlisted"}
	}
	if allowlisted {
		return prFeedbackEligibility{Eligible: true, BotEligibility: models.PRFeedbackBotEligibilityAllowlist}
	}
	if input.PrivateRepo {
		return prFeedbackEligibility{Eligible: true, BotEligibility: models.PRFeedbackBotEligibilityPrivateAll}
	}
	if isGitHubFirstPartyBot(login) {
		return prFeedbackEligibility{Eligible: true, BotEligibility: models.PRFeedbackBotEligibilityGitHubFirstParty}
	}
	if input.InstalledApp {
		return prFeedbackEligibility{Eligible: true, BotEligibility: models.PRFeedbackBotEligibilityInstalledApp}
	}
	return prFeedbackEligibility{IgnoreReason: "public_bot_provenance_unverified"}
}

var whitespacePattern = regexp.MustCompile(`\s+`)

func deterministicPRFeedbackTriage(item models.PullRequestFeedbackItem) (models.PRFeedbackTriageResult, bool) {
	body := strings.TrimSpace(item.Body)
	normalized := strings.ToLower(whitespacePattern.ReplaceAllString(body, " "))
	acknowledgement := strings.Trim(normalized, " .,!?:;")
	if body == "" {
		return models.PRFeedbackTriageResult{Intent: models.PRFeedbackIntentAcknowledgement, Reason: "empty feedback"}, true
	}
	if emojiOnly(body) || acknowledgement == "thanks" || acknowledgement == "thank you" || acknowledgement == "lgtm" || acknowledgement == "looks good" || acknowledgement == "approved" {
		return models.PRFeedbackTriageResult{Intent: models.PRFeedbackIntentAcknowledgement, Reason: "acknowledgement-only feedback"}, true
	}
	noisePrefixes := []string{"no issues found", "no problems found", "deployment succeeded", "deployment complete", "coverage report", "test coverage", "build succeeded", "all checks passed"}
	for _, prefix := range noisePrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return models.PRFeedbackTriageResult{Intent: models.PRFeedbackIntentAcknowledgement, Reason: "status-only bot output"}, true
		}
	}
	return models.PRFeedbackTriageResult{}, false
}

func feedbackFindingFingerprint(item models.PullRequestFeedbackItem) string {
	finding := ""
	if item.ProviderFindingKey != nil {
		finding = strings.TrimSpace(strings.ToLower(*item.ProviderFindingKey))
	}
	if finding == "" {
		finding = strings.ToLower(whitespacePattern.ReplaceAllString(strings.TrimSpace(item.Body), " "))
	}
	path := ""
	if item.Path != nil {
		path = *item.Path
	}
	line := 0
	if item.Line != nil {
		line = *item.Line
	}
	raw := strings.Join([]string{canonicalBotLogin(item.AuthorLogin), finding, path, strconv.Itoa(line)}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func canonicalBotLogin(login string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(login)), "[bot]")
}

func containsCanonicalLogin(logins []string, login string) bool {
	for _, candidate := range logins {
		if canonicalBotLogin(candidate) == login {
			return true
		}
	}
	return false
}

func isGitHubFirstPartyBot(login string) bool {
	switch login {
	case "dependabot", "github-actions", "github-advanced-security", "copilot-pull-request-reviewer":
		return true
	}
	return false
}

func emojiOnly(value string) bool {
	hasSymbol := false
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.Is(unicode.Punct, r) {
			continue
		}
		if unicode.Is(unicode.So, r) || unicode.Is(unicode.Sk, r) {
			hasSymbol = true
			continue
		}
		return false
	}
	return hasSymbol
}
