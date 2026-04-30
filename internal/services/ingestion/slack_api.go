package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/rs/zerolog"
)

const slackAPIBase = "https://slack.com/api"

// SlackMessage represents a single Slack message.
type SlackMessage struct {
	User       string `json:"user"`
	Text       string `json:"text"`
	Timestamp  string `json:"ts"`
	ThreadTS   string `json:"thread_ts,omitempty"`
	ReplyCount int    `json:"reply_count,omitempty"`
}

// SlackChannel represents a Slack channel.
type SlackChannel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ThreadAnalysis is the structured output from the LLM summarizer.
type ThreadAnalysis struct {
	Actionable bool   `json:"actionable"`
	Category   string `json:"category"` // bug_report, feature_request, customer_issue, discussion, not_actionable
	Summary    string `json:"summary"`  // max 200 chars
	Urgency    string `json:"urgency"`  // high, medium, low, none
}

// SlackThreadSummary is a thread with its analysis and raw messages.
type SlackThreadSummary struct {
	ChannelID    string          `json:"channel_id"`
	ChannelName  string          `json:"channel_name"`
	ThreadTS     string          `json:"thread_ts"`
	Analysis     *ThreadAnalysis `json:"analysis"`
	MessageCount int             `json:"message_count"`
	Participants []string        `json:"participants"`
	LastActivity time.Time       `json:"last_activity"`
	Messages     json.RawMessage `json:"messages"`
}

// slackResponseMetadata holds cursor-based pagination info from Slack API responses.
type slackResponseMetadata struct {
	NextCursor string `json:"next_cursor"`
}

// slackHistoryResponse is the response shape for conversations.history.
type slackHistoryResponse struct {
	OK               bool                  `json:"ok"`
	Error            string                `json:"error,omitempty"`
	Messages         []SlackMessage        `json:"messages"`
	HasMore          bool                  `json:"has_more"`
	ResponseMetadata slackResponseMetadata `json:"response_metadata"`
}

// slackRepliesResponse is the response shape for conversations.replies.
type slackRepliesResponse struct {
	OK       bool           `json:"ok"`
	Error    string         `json:"error,omitempty"`
	Messages []SlackMessage `json:"messages"`
}

// slackChannelInfoResponse is the response shape for conversations.info.
type slackChannelInfoResponse struct {
	OK      bool         `json:"ok"`
	Error   string       `json:"error,omitempty"`
	Channel SlackChannel `json:"channel"`
}

// slackChannelListResponse is the response shape for conversations.list.
type slackChannelListResponse struct {
	OK               bool                  `json:"ok"`
	Error            string                `json:"error,omitempty"`
	Channels         []SlackChannel        `json:"channels"`
	ResponseMetadata slackResponseMetadata `json:"response_metadata"`
}

// SlackAPIClient interacts with the Slack Web API.
type SlackAPIClient struct {
	client  *http.Client
	logger  zerolog.Logger
	baseURL string // override for testing; defaults to slackAPIBase
}

// NewSlackAPIClient creates a new Slack API client.
func NewSlackAPIClient(logger zerolog.Logger) *SlackAPIClient {
	return &SlackAPIClient{
		client: &http.Client{Timeout: 30 * time.Second},
		logger: logger,
	}
}

// FetchChannelMessages fetches messages from a channel since the given time.
func (c *SlackAPIClient) FetchChannelMessages(ctx context.Context, accessToken, channelID string, since time.Time) ([]SlackMessage, error) {
	var allMessages []SlackMessage
	cursor := ""

	for {
		params := url.Values{
			"channel": {channelID},
			"limit":   {"200"},
			"oldest":  {strconv.FormatInt(since.Unix(), 10)},
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}

		var resp slackHistoryResponse
		if err := c.slackGet(ctx, accessToken, "conversations.history", params, &resp); err != nil {
			return nil, err
		}
		if !resp.OK {
			return nil, fmt.Errorf("slack conversations.history: %s", resp.Error)
		}

		allMessages = append(allMessages, resp.Messages...)

		if !resp.HasMore || resp.ResponseMetadata.NextCursor == "" || len(allMessages) >= 200 {
			break
		}
		cursor = resp.ResponseMetadata.NextCursor
	}

	return allMessages, nil
}

// FetchThreadReplies fetches replies to a thread.
func (c *SlackAPIClient) FetchThreadReplies(ctx context.Context, accessToken, channelID, threadTS string) ([]SlackMessage, error) {
	params := url.Values{
		"channel": {channelID},
		"ts":      {threadTS},
		"limit":   {"200"},
	}

	var resp slackRepliesResponse
	if err := c.slackGet(ctx, accessToken, "conversations.replies", params, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("slack conversations.replies: %s", resp.Error)
	}
	return resp.Messages, nil
}

// FetchChannelInfo fetches info about a channel.
func (c *SlackAPIClient) FetchChannelInfo(ctx context.Context, accessToken, channelID string) (*SlackChannel, error) {
	params := url.Values{
		"channel": {channelID},
	}

	var resp slackChannelInfoResponse
	if err := c.slackGet(ctx, accessToken, "conversations.info", params, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("slack conversations.info: %s", resp.Error)
	}
	return &resp.Channel, nil
}

// ListChannels lists channels visible to the bot.
func (c *SlackAPIClient) ListChannels(ctx context.Context, accessToken string) ([]SlackChannel, error) {
	var allChannels []SlackChannel
	cursor := ""

	for {
		params := url.Values{
			"types":            {"public_channel,private_channel"},
			"exclude_archived": {"true"},
			"limit":            {"200"},
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}

		var resp slackChannelListResponse
		if err := c.slackGet(ctx, accessToken, "conversations.list", params, &resp); err != nil {
			return nil, err
		}
		if !resp.OK {
			return nil, fmt.Errorf("slack conversations.list: %s", resp.Error)
		}

		allChannels = append(allChannels, resp.Channels...)

		if resp.ResponseMetadata.NextCursor == "" {
			break
		}
		cursor = resp.ResponseMetadata.NextCursor
	}

	return allChannels, nil
}

// GroupIntoThreads groups flat messages into thread structures.
// Messages with reply_count > 0 become thread roots. Messages without thread_ts
// are standalone messages (each gets its own "thread" with thread_ts = ts).
func GroupIntoThreads(messages []SlackMessage) map[string][]SlackMessage {
	threads := make(map[string][]SlackMessage)
	for _, msg := range messages {
		key := msg.ThreadTS
		if key == "" {
			key = msg.Timestamp
		}
		threads[key] = append(threads[key], msg)
	}
	return threads
}

func (c *SlackAPIClient) slackGet(ctx context.Context, accessToken, method string, params url.Values, dest any) error {
	base := c.baseURL
	if base == "" {
		base = slackAPIBase
	}
	u := fmt.Sprintf("%s/%s?%s", base, method, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("slack api %s: %w", method, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := resp.Header.Get("Retry-After")
		return fmt.Errorf("slack rate limited, retry after %s seconds", retryAfter)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("slack api %s: status %d: %s", method, resp.StatusCode, string(body))
	}

	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return fmt.Errorf("decode slack response: %w", err)
	}
	return nil
}
