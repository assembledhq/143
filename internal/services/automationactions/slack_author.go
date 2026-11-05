package automationactions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

var slackAuthorIDPattern = regexp.MustCompile(`^[UW][A-Z0-9]{8,31}$`)

func (p *Providers) slackPRAuthorMention(ctx context.Context, scope models.AutomationActionScope) (string, error) {
	if scope.Config.SlackPRAuthorMapPath == "" || scope.TargetID == uuid.Nil || scope.PRNumber <= 0 {
		return "", nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	mention, err := p.lookupSlackPRAuthor(githubRequestContext(lookupCtx, scope), scope)
	if err == nil {
		err = lookupCtx.Err()
	}
	if err != nil {
		return "", &SendFailure{Code: "SLACK_PR_AUTHOR_LOOKUP_FAILED", Definitive: true, Err: err}
	}
	return mention, nil
}

func (p *Providers) lookupSlackPRAuthor(ctx context.Context, scope models.AutomationActionScope) (string, error) {
	if err := ValidateRepositoryName(scope.RepositoryName); err != nil {
		return "", err
	}
	token, err := p.githubToken(ctx, scope)
	if err != nil {
		return "", fmt.Errorf("get PR author lookup credentials: %w", err)
	}
	pr, err := p.inspectPullRequest(ctx, scope, token)
	if err != nil {
		return "", fmt.Errorf("read PR author: %w", err)
	}
	var file struct {
		Type     string `json:"type"`
		Encoding string `json:"encoding"`
		Size     int64  `json:"size"`
		Content  string `json:"content"`
	}
	endpoint := p.githubURL + "/repos/" + scope.RepositoryName + "/contents/" + scope.Config.SlackPRAuthorMapPath
	if err = p.request(ctx, http.MethodGet, endpoint, token, "", nil, &file); err != nil {
		return "", fmt.Errorf("read PR author map: %w", err)
	}
	const maxMapBytes = 512 << 10
	if file.Type != "file" || file.Encoding != "base64" || file.Size < 0 || file.Size > maxMapBytes {
		return "", errors.New("PR author map has an unsupported file envelope")
	}
	data, err := base64.StdEncoding.DecodeString(file.Content)
	if err != nil {
		return "", fmt.Errorf("decode PR author map: %w", err)
	}
	if len(data) > maxMapBytes || !utf8.Valid(data) {
		return "", errors.New("PR author map exceeds its size or encoding limits")
	}
	var entries []struct {
		GitHubName json.RawMessage `json:"github_name"`
		SlackID    json.RawMessage `json:"slack_id"`
	}
	if err = json.Unmarshal(data, &entries); err != nil {
		return "", fmt.Errorf("parse PR author map: %w", err)
	}
	if entries == nil {
		return "", errors.New("PR author map must contain a JSON list")
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	var selectedID json.RawMessage
	matches := 0
	for _, entry := range entries {
		var githubName string
		if json.Unmarshal(entry.GitHubName, &githubName) == nil && strings.EqualFold(githubName, pr.Author) {
			selectedID = entry.SlackID
			matches++
		}
	}
	if matches != 1 {
		reason := "missing_mapping"
		if matches > 1 {
			reason = "duplicate_mapping"
		}
		zerolog.Ctx(ctx).Warn().Str("reason", reason).Msg("Slack PR author mention omitted")
		return "", nil
	}
	var slackID string
	if json.Unmarshal(selectedID, &slackID) != nil || !slackAuthorIDPattern.MatchString(slackID) {
		zerolog.Ctx(ctx).Warn().Str("reason", "invalid_mapping").Msg("Slack PR author mention omitted")
		return "", nil
	}
	return "<@" + slackID + ">", nil
}
