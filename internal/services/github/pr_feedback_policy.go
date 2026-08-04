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

// PRFeedbackHiddenMarker returns the stable marker used on machine-authored
// PR feedback replies. Inbound provenance checks reject comments containing
// this prefix so response publication cannot recursively trigger more work.
func PRFeedbackHiddenMarker(id string) string {
	return prFeedbackHiddenMarker + strings.TrimSpace(id) + " -->"
}

type PRFeedbackProvenanceInput struct {
	AuthorLogin string
	AuthorType  models.PRFeedbackAuthorType
	Association string
	OwnAppLogin string
	Body        string
	Deleted     bool
}

type PRFeedbackProvenance struct {
	Recordable bool
	// Trusted mirrors the long-standing association-only rule used by feedback
	// follow-through. Repository visibility deliberately does not widen it:
	// disputes apply their own private-repo relaxation in
	// models.CodeReviewDispute.CurrentTrust, and folding that here would
	// silently make every private-repo commenter eligible for automatic
	// follow-through.
	Trusted      bool
	IgnoreReason string
}

// EvaluatePRFeedbackProvenance is the shared, product-neutral half of PR
// feedback eligibility. It deliberately excludes follow-through mode and
// mention settings so code-review disputes can record and answer external
// contributors without inheriting an unrelated product switch.
func EvaluatePRFeedbackProvenance(input PRFeedbackProvenanceInput) PRFeedbackProvenance {
	input.AuthorLogin = strings.TrimSpace(input.AuthorLogin)
	input.OwnAppLogin = strings.TrimSpace(input.OwnAppLogin)
	input.Association = strings.ToUpper(strings.TrimSpace(input.Association))
	input.Body = strings.TrimSpace(input.Body)
	login := canonicalBotLogin(input.AuthorLogin)
	if login == canonicalBotLogin(input.OwnAppLogin) && login != "" {
		return PRFeedbackProvenance{IgnoreReason: "self_authored"}
	}
	if input.Deleted || strings.TrimSpace(input.Body) == "" {
		return PRFeedbackProvenance{IgnoreReason: "empty_or_deleted"}
	}
	if strings.Contains(input.Body, prFeedbackHiddenMarker) {
		return PRFeedbackProvenance{IgnoreReason: "hidden_response_marker"}
	}
	if input.AuthorType == models.PRFeedbackAuthorTypeBot {
		return PRFeedbackProvenance{IgnoreReason: "bot_authored"}
	}
	trustedAssociation := input.Association == "OWNER" || input.Association == "MEMBER" || input.Association == "COLLABORATOR"
	return PRFeedbackProvenance{Recordable: true, Trusted: trustedAssociation}
}

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
	provenance := EvaluatePRFeedbackProvenance(PRFeedbackProvenanceInput{
		AuthorLogin: input.AuthorLogin, AuthorType: input.AuthorType,
		Association: input.Association, OwnAppLogin: input.OwnAppLogin, Body: input.Body, Deleted: input.Deleted,
	})
	if !provenance.Recordable && (input.AuthorType != models.PRFeedbackAuthorTypeBot || provenance.IgnoreReason != "bot_authored") {
		return prFeedbackEligibility{IgnoreReason: provenance.IgnoreReason}
	}
	login := canonicalBotLogin(input.AuthorLogin)
	if input.AuthorType != models.PRFeedbackAuthorTypeBot {
		if input.HumanMode == models.PRFeedbackHumanModeOff {
			return prFeedbackEligibility{IgnoreReason: "human_mode_off"}
		}
		if !provenance.Trusted && !input.Mentioned {
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
