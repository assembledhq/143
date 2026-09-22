package automationactions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/ingestion"
	"github.com/google/uuid"
)

type InstallationTokens interface {
	GetInstallationToken(context.Context, int64) (string, error)
}
type Credentials interface {
	Get(context.Context, uuid.UUID, models.ProviderName) (*models.DecryptedCredential, error)
}
type SlackPoster interface {
	PostMessage(context.Context, string, string, string, string) (ingestion.SlackPostedMessage, error)
}

// Providers keeps credentials on the server; endpoints are fixed and never supplied by the agent.
type Providers struct {
	tokens               InstallationTokens
	credentials          Credentials
	slack                SlackPoster
	client               *http.Client
	githubURL, notionURL string
}

func NewProviders(tokens InstallationTokens, credentials Credentials, slack SlackPoster) *Providers {
	return &Providers{tokens: tokens, credentials: credentials, slack: slack, client: &http.Client{Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, githubURL: "https://api.github.com", notionURL: "https://api.notion.com"}
}
func (p *Providers) githubToken(ctx context.Context, scope models.AutomationActionScope) (string, error) {
	if p.tokens == nil {
		return "", errors.New("GitHub installation service unavailable")
	}
	return p.tokens.GetInstallationToken(ctx, scope.InstallationID)
}
func (p *Providers) credential(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (string, error) {
	if p.credentials == nil {
		return "", errors.New("credentials unavailable")
	}
	cred, err := p.credentials.Get(ctx, orgID, provider)
	if err != nil {
		return "", errors.New("provider credentials unavailable")
	}
	if cred == nil {
		return "", errors.New("provider credentials unavailable")
	}
	switch cfg := cred.Config.(type) {
	case models.NotionConfig:
		if provider == models.ProviderNotion && cfg.AccessToken != "" {
			return cfg.AccessToken, nil
		}
	case models.SlackConfig:
		if provider == models.ProviderSlack && cfg.AccessToken != "" {
			return cfg.AccessToken, nil
		}
	}
	return "", errors.New("provider credentials invalid")
}
func (p *Providers) githubPath(scope models.AutomationActionScope, resource string) string {
	return p.githubURL + "/repos/" + scope.RepositoryName + "/" + resource + "/" + strconv.Itoa(scope.PRNumber)
}
func (p *Providers) Inspect(ctx context.Context, scope models.AutomationActionScope) (PullRequest, error) {
	var result PullRequest
	if err := ValidateRepositoryName(scope.RepositoryName); err != nil {
		return result, err
	}
	token, err := p.githubToken(ctx, scope)
	if err != nil {
		return result, err
	}
	var pr struct {
		Title string `json:"title"`
		URL   string `json:"html_url"`
		State string `json:"state"`
		User  struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err = p.request(ctx, http.MethodGet, p.githubPath(scope, "pulls"), token, "", nil, &pr); err != nil {
		return result, err
	}
	if strings.TrimSpace(pr.Title) == "" || len(pr.Title) > 4096 || strings.TrimSpace(pr.User.Login) == "" || len(pr.User.Login) > 256 {
		return result, errors.New("GitHub pull request metadata is invalid")
	}
	result = PullRequest{Head: pr.Head.SHA, Open: pr.State == "open", Title: pr.Title, URL: "https://github.com/" + scope.RepositoryName + "/pull/" + strconv.Itoa(scope.PRNumber), Author: pr.User.Login}
	return result, nil
}
func (p *Providers) Preflight(ctx context.Context, scope models.AutomationActionScope, request models.AutomationActionRequest) error {
	if request.Kind == models.AutomationActionSlack {
		if p.slack == nil {
			return errors.New("slack unavailable")
		}
		_, err := p.credential(ctx, scope.Actor.OrgID, models.ProviderSlack)
		return err
	}
	if request.Kind != models.AutomationActionNotion {
		return nil
	}
	token, err := p.credential(ctx, scope.Actor.OrgID, models.ProviderNotion)
	if err != nil {
		return err
	}
	var source struct {
		Properties map[string]struct {
			Type   models.AutomationActionPropertyType `json:"type"`
			Select struct {
				Options []struct {
					Name string `json:"name"`
				} `json:"options"`
			} `json:"select"`
		} `json:"properties"`
	}
	if err = p.request(ctx, http.MethodGet, p.notionURL+"/v1/data_sources/"+scope.Config.NotionDataSourceID, token, "2025-09-03", nil, &source); err != nil {
		return err
	}
	for name, value := range request.Properties {
		kind := scope.Config.NotionProperties[name]
		if source.Properties[name].Type != kind {
			return fmt.Errorf("notion property %q must have type %s", name, kind)
		}
		if kind == models.AutomationActionPropertySelect {
			found := false
			for _, option := range source.Properties[name].Select.Options {
				found = found || option.Name == value
			}
			if !found {
				return fmt.Errorf("notion select property %q does not contain the requested option", name)
			}
		}
	}
	return nil
}
func (p *Providers) Send(ctx context.Context, scope models.AutomationActionScope, action models.AutomationAction, payload Payload) (Receipt, error) {
	switch action.Kind {
	case models.AutomationActionLabel, models.AutomationActionTeam, models.AutomationActionComment:
		return p.sendGitHub(ctx, scope, action, payload)
	case models.AutomationActionNotion:
		return p.sendNotion(ctx, scope, payload)
	case models.AutomationActionSlack:
		token, err := p.credential(ctx, scope.Actor.OrgID, models.ProviderSlack)
		if err != nil {
			return Receipt{}, &SendFailure{Code: "SLACK_NOT_CONNECTED", Definitive: true}
		}
		if p.slack == nil {
			return Receipt{}, &SendFailure{Code: "SLACK_NOT_CONNECTED", Definitive: true}
		}
		// Disable automatic parsing of user-controlled mention and link syntax by escaping Slack delimiters.
		escape := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
		text := escape.Replace(payload.Request.Text)
		posted, err := p.slack.PostMessage(ctx, token, scope.Config.SlackChannelID, "", text)
		if errors.Is(err, ingestion.ErrSlackRateLimited) {
			return Receipt{}, &SendFailure{Code: "SLACK_RATE_LIMITED", Definitive: true}
		}
		var rejected *ingestion.SlackMessageError
		if errors.As(err, &rejected) {
			// Slack documents internal_error/fatal_error as potentially partially successful.
			// Keep unrecognized errors uncertain; only documented rejections are retryable.
			// https://docs.slack.dev/reference/methods/chat.postMessage/#errors
			switch rejected.Code {
			case "channel_not_found", "not_in_channel", "is_archived", "missing_scope", "invalid_auth", "not_authed", "account_inactive", "token_revoked", "no_permission", "msg_too_long",
				"restricted_action", "restricted_action_read_only_channel", "restricted_action_thread_only_channel", "restricted_action_non_threadable_channel", "ekm_access_denied", "invalid_arguments", "ratelimited", "rate_limited", "access_denied", "app_access_restricted", "token_expired", "team_access_not_granted":
				return Receipt{}, &SendFailure{Code: "SLACK_" + strings.ToUpper(rejected.Code), Definitive: true}
			}
		}
		if err != nil {
			return Receipt{}, err
		}
		if posted.Timestamp == "" || posted.Channel != scope.Config.SlackChannelID {
			return Receipt{}, &SendFailure{Code: "SLACK_RECEIPT_INVALID"}
		}
		return Receipt{ID: posted.Channel + ":" + posted.Timestamp}, nil
	default:
		return Receipt{}, &SendFailure{Code: "INVALID_ACTION_KIND", Definitive: true}
	}
}
func (p *Providers) sendGitHub(ctx context.Context, scope models.AutomationActionScope, action models.AutomationAction, payload Payload) (Receipt, error) {
	token, err := p.githubToken(ctx, scope)
	if err != nil {
		return Receipt{}, &SendFailure{Code: "GITHUB_NOT_CONNECTED", Definitive: true}
	}
	switch action.Kind {
	case models.AutomationActionLabel:
		var labels []struct {
			Name string `json:"name"`
		}
		if err = p.request(ctx, http.MethodPost, p.githubPath(scope, "issues")+"/labels", token, "", map[string]any{"labels": []string{scope.Config.Label}}, &labels); err != nil {
			return Receipt{}, err
		}
		for _, label := range labels {
			if label.Name == scope.Config.Label {
				return Receipt{ID: scope.Config.Label, URL: payload.PR.URL}, nil
			}
		}
		return Receipt{}, &SendFailure{Code: "GITHUB_LABEL_RECEIPT_INVALID"}
	case models.AutomationActionTeam:
		// An already requested team satisfies the operation without another notification.
		var reviewers struct {
			Teams []struct {
				Slug string `json:"slug"`
			} `json:"teams"`
		}
		if err = p.request(ctx, http.MethodGet, p.githubPath(scope, "pulls")+"/requested_reviewers", token, "", nil, &reviewers); err != nil {
			return Receipt{}, &SendFailure{Code: "GITHUB_REVIEWERS_UNAVAILABLE", Definitive: true}
		}
		for _, team := range reviewers.Teams {
			if team.Slug == scope.Config.Team {
				return Receipt{ID: scope.Config.Team, URL: payload.PR.URL}, nil
			}
		}
		if err = p.request(ctx, http.MethodPost, p.githubPath(scope, "pulls")+"/requested_reviewers", token, "", map[string]any{"team_reviewers": []string{scope.Config.Team}}, nil); err != nil {
			return Receipt{}, err
		}
		return Receipt{ID: scope.Config.Team, URL: payload.PR.URL}, nil
	default:
		var comment struct {
			ID  int64  `json:"id"`
			URL string `json:"html_url"`
		}
		body := payload.Request.Text + "\n\n<!-- 143-automation-action:" + action.ID.String() + " -->"
		err = p.request(ctx, http.MethodPost, p.githubPath(scope, "issues")+"/comments", token, "", map[string]string{"body": body}, &comment)
		if err != nil {
			return Receipt{}, err
		}
		if comment.ID <= 0 {
			return Receipt{}, &SendFailure{Code: "GITHUB_COMMENT_RECEIPT_INVALID"}
		}
		return Receipt{ID: strconv.FormatInt(comment.ID, 10), URL: safeProviderURL(comment.URL, "github.com")}, nil
	}
}
func richText(text string) []any {
	// Notion limits individual rich-text fragments to 2,000 characters.
	runes := []rune(text)
	out := make([]any, 0, 1)
	for len(runes) > 0 {
		n := min(len(runes), 2000)
		out = append(out, map[string]any{"type": "text", "text": map[string]string{"content": string(runes[:n])}})
		runes = runes[n:]
	}
	return out
}
func (p *Providers) sendNotion(ctx context.Context, scope models.AutomationActionScope, payload Payload) (Receipt, error) {
	token, err := p.credential(ctx, scope.Actor.OrgID, models.ProviderNotion)
	if err != nil {
		return Receipt{}, &SendFailure{Code: "NOTION_NOT_CONNECTED", Definitive: true}
	}
	properties := map[string]any{}
	for name, value := range payload.Request.Properties {
		kind := scope.Config.NotionProperties[name]
		switch kind {
		case models.AutomationActionPropertyTitle, models.AutomationActionPropertyText:
			properties[name] = map[string]any{string(kind): richText(value)}
		case models.AutomationActionPropertyURL:
			properties[name] = map[string]string{"url": value}
		case models.AutomationActionPropertyDate:
			properties[name] = map[string]any{"date": map[string]string{"start": value}}
		case models.AutomationActionPropertySelect:
			properties[name] = map[string]any{"select": map[string]string{"name": value}}
		default:
			return Receipt{}, &SendFailure{Code: "INVALID_NOTION_PROPERTY", Definitive: true}
		}
	}
	body := map[string]any{"parent": map[string]string{"type": "data_source_id", "data_source_id": scope.Config.NotionDataSourceID}, "properties": properties}
	var page struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err = p.request(ctx, http.MethodPost, p.notionURL+"/v1/pages", token, "2025-09-03", body, &page); err != nil {
		return Receipt{}, err
	}
	if id, err := uuid.Parse(page.ID); err != nil || id == uuid.Nil {
		return Receipt{}, &SendFailure{Code: "NOTION_RECEIPT_INVALID"}
	}
	return Receipt{ID: page.ID, URL: safeProviderURL(page.URL, "notion.so")}, nil
}
func (p *Providers) request(ctx context.Context, method, endpoint, token, version string, body, dest any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return &SendFailure{Code: "INVALID_PROVIDER_REQUEST", Definitive: true}
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return &SendFailure{Code: "INVALID_PROVIDER_REQUEST", Definitive: true}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if version != "" {
		req.Header.Set("Notion-Version", version)
	} else {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return &SendFailure{Code: "PROVIDER_TRANSPORT_UNKNOWN"}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Only explicitly rejected requests are safe to resend. In particular 408/5xx are uncertain.
		definitive := resp.StatusCode == 400 || resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404 || resp.StatusCode == 405 || resp.StatusCode == 410 || resp.StatusCode == 451 || resp.StatusCode == 422 || resp.StatusCode == 429
		return &SendFailure{Code: fmt.Sprintf("PROVIDER_HTTP_%d", resp.StatusCode), Definitive: definitive}
	}
	if dest == nil {
		return nil
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(dest); err != nil {
		return &SendFailure{Code: "PROVIDER_RESPONSE_UNKNOWN"}
	}
	return nil
}

// ValidateRepositoryName prevents a corrupted repository name from changing the configured API path.
func ValidateRepositoryName(name string) error {
	parts := strings.Split(name, "/")
	if len(parts) != 2 {
		return errors.New("invalid repository name")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || url.PathEscape(part) != part {
			return errors.New("invalid repository name")
		}
	}
	return nil
}

// Provider links are evidence, not a route for arbitrary URL schemes or credentials.
func safeProviderURL(raw, host string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || len(raw) > 2048 || (u.Host != host && !strings.HasSuffix(u.Host, "."+host)) {
		return ""
	}
	return u.String()
}
