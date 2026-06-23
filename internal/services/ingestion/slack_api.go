package ingestion

import (
	"bytes"
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

type SlackUser struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	RealName string `json:"real_name"`
	TeamID   string `json:"team_id"`
	Profile  struct {
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
		RealName    string `json:"real_name"`
	} `json:"profile"`
}

type SlackPostedMessage struct {
	Channel   string `json:"channel"`
	Timestamp string `json:"ts"`
}

type SlackBlock struct {
	Type      string            `json:"type"`
	Text      *SlackTextObject  `json:"text,omitempty"`
	Label     *SlackTextObject  `json:"label,omitempty"`
	Element   map[string]any    `json:"element,omitempty"`
	Elements  []map[string]any  `json:"elements,omitempty"`
	Fields    []SlackTextObject `json:"fields,omitempty"`
	Accessory map[string]any    `json:"accessory,omitempty"`
	BlockID   string            `json:"block_id,omitempty"`
	Optional  bool              `json:"optional,omitempty"`
}

type SlackTextObject struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type SlackHomeView struct {
	Type            string           `json:"type"`
	Title           *SlackTextObject `json:"title,omitempty"`
	Submit          *SlackTextObject `json:"submit,omitempty"`
	Close           *SlackTextObject `json:"close,omitempty"`
	CallbackID      string           `json:"callback_id,omitempty"`
	PrivateMetadata string           `json:"private_metadata,omitempty"`
	Blocks          []SlackBlock     `json:"blocks,omitempty"`
}

type SlackAuthInfo struct {
	URL    string `json:"url"`
	Team   string `json:"team"`
	User   string `json:"user"`
	TeamID string `json:"team_id"`
	UserID string `json:"user_id"`
	BotID  string `json:"bot_id"`
}

type SlackFile struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Title      string `json:"title"`
	Mimetype   string `json:"mimetype"`
	URLPrivate string `json:"url_private"`
	Permalink  string `json:"permalink"`
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

type slackPostMessageResponse struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

type slackUserInfoResponse struct {
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
	User  SlackUser `json:"user"`
}

type slackOpenConversationResponse struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Channel struct {
		ID string `json:"id"`
	} `json:"channel"`
}

type slackPermalinkResponse struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	Permalink string `json:"permalink"`
}

type slackAuthTestResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	SlackAuthInfo
}

type slackFileInfoResponse struct {
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
	File  SlackFile `json:"file"`
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

func (c *SlackAPIClient) PostMessage(ctx context.Context, accessToken, channelID, threadTS, text string) (SlackPostedMessage, error) {
	payload := map[string]string{
		"channel": channelID,
		"text":    text,
	}
	if threadTS != "" {
		payload["thread_ts"] = threadTS
	}
	var resp slackPostMessageResponse
	if err := c.slackPost(ctx, accessToken, "chat.postMessage", payload, &resp); err != nil {
		return SlackPostedMessage{}, err
	}
	if !resp.OK {
		return SlackPostedMessage{}, fmt.Errorf("slack chat.postMessage: %s", resp.Error)
	}
	return SlackPostedMessage{Channel: resp.Channel, Timestamp: resp.TS}, nil
}

func (c *SlackAPIClient) PostMessageWithBlocks(ctx context.Context, accessToken, channelID, threadTS, text string, blocks []SlackBlock) (SlackPostedMessage, error) {
	payload := map[string]any{
		"channel": channelID,
		"text":    text,
		"blocks":  blocks,
	}
	if threadTS != "" {
		payload["thread_ts"] = threadTS
	}
	var resp slackPostMessageResponse
	if err := c.slackPost(ctx, accessToken, "chat.postMessage", payload, &resp); err != nil {
		return SlackPostedMessage{}, err
	}
	if !resp.OK {
		return SlackPostedMessage{}, fmt.Errorf("slack chat.postMessage: %s", resp.Error)
	}
	return SlackPostedMessage{Channel: resp.Channel, Timestamp: resp.TS}, nil
}

func (c *SlackAPIClient) UpdateMessage(ctx context.Context, accessToken, channelID, messageTS, text string) error {
	payload := map[string]string{
		"channel": channelID,
		"ts":      messageTS,
		"text":    text,
	}
	var resp slackPostMessageResponse
	if err := c.slackPost(ctx, accessToken, "chat.update", payload, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("slack chat.update: %s", resp.Error)
	}
	return nil
}

func (c *SlackAPIClient) UpdateMessageWithBlocks(ctx context.Context, accessToken, channelID, messageTS, text string, blocks []SlackBlock) error {
	payload := map[string]any{
		"channel": channelID,
		"ts":      messageTS,
		"text":    text,
		"blocks":  blocks,
	}
	var resp slackPostMessageResponse
	if err := c.slackPost(ctx, accessToken, "chat.update", payload, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("slack chat.update: %s", resp.Error)
	}
	return nil
}

func (c *SlackAPIClient) AddReaction(ctx context.Context, accessToken, channelID, messageTS, name string) error {
	payload := map[string]string{
		"channel":   channelID,
		"timestamp": messageTS,
		"name":      name,
	}
	var resp slackPostMessageResponse
	if err := c.slackPost(ctx, accessToken, "reactions.add", payload, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("slack reactions.add: %s", resp.Error)
	}
	return nil
}

func (c *SlackAPIClient) DeleteMessage(ctx context.Context, accessToken, channelID, messageTS string) error {
	payload := map[string]string{
		"channel": channelID,
		"ts":      messageTS,
	}
	var resp slackPostMessageResponse
	if err := c.slackPost(ctx, accessToken, "chat.delete", payload, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("slack chat.delete: %s", resp.Error)
	}
	return nil
}

func (c *SlackAPIClient) PostEphemeral(ctx context.Context, accessToken, channelID, userID, text string) error {
	payload := map[string]string{
		"channel": channelID,
		"user":    userID,
		"text":    text,
	}
	var resp slackPostMessageResponse
	if err := c.slackPost(ctx, accessToken, "chat.postEphemeral", payload, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("slack chat.postEphemeral: %s", resp.Error)
	}
	return nil
}

func (c *SlackAPIClient) OpenDM(ctx context.Context, accessToken, userID string) (string, error) {
	payload := map[string]string{"users": userID}
	var resp slackOpenConversationResponse
	if err := c.slackPost(ctx, accessToken, "conversations.open", payload, &resp); err != nil {
		return "", err
	}
	if !resp.OK {
		return "", fmt.Errorf("slack conversations.open: %s", resp.Error)
	}
	return resp.Channel.ID, nil
}

func (c *SlackAPIClient) PublishHome(ctx context.Context, accessToken, userID string, view SlackHomeView) error {
	payload := map[string]any{
		"user_id": userID,
		"view":    view,
	}
	var resp slackPostMessageResponse
	if err := c.slackPost(ctx, accessToken, "views.publish", payload, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("slack views.publish: %s", resp.Error)
	}
	return nil
}

func (c *SlackAPIClient) OpenView(ctx context.Context, accessToken, triggerID string, view SlackHomeView) error {
	payload := map[string]any{
		"trigger_id": triggerID,
		"view":       view,
	}
	var resp slackPostMessageResponse
	if err := c.slackPost(ctx, accessToken, "views.open", payload, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("slack views.open: %s", resp.Error)
	}
	return nil
}

func (c *SlackAPIClient) UpdateView(ctx context.Context, accessToken, viewID string, view SlackHomeView) error {
	payload := map[string]any{
		"view_id": viewID,
		"view":    view,
	}
	var resp slackPostMessageResponse
	if err := c.slackPost(ctx, accessToken, "views.update", payload, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("slack views.update: %s", resp.Error)
	}
	return nil
}

func (c *SlackAPIClient) AuthTest(ctx context.Context, accessToken string) (SlackAuthInfo, error) {
	var resp slackAuthTestResponse
	if err := c.slackGet(ctx, accessToken, "auth.test", url.Values{}, &resp); err != nil {
		return SlackAuthInfo{}, err
	}
	if !resp.OK {
		return SlackAuthInfo{}, fmt.Errorf("slack auth.test: %s", resp.Error)
	}
	return resp.SlackAuthInfo, nil
}

func (c *SlackAPIClient) FetchUserInfo(ctx context.Context, accessToken, userID string) (SlackUser, error) {
	params := url.Values{"user": {userID}}
	var resp slackUserInfoResponse
	if err := c.slackGet(ctx, accessToken, "users.info", params, &resp); err != nil {
		return SlackUser{}, err
	}
	if !resp.OK {
		return SlackUser{}, fmt.Errorf("slack users.info: %s", resp.Error)
	}
	return resp.User, nil
}

func (c *SlackAPIClient) LookupUserByEmail(ctx context.Context, accessToken, email string) (SlackUser, error) {
	params := url.Values{"email": {email}}
	var resp slackUserInfoResponse
	if err := c.slackGet(ctx, accessToken, "users.lookupByEmail", params, &resp); err != nil {
		return SlackUser{}, err
	}
	if !resp.OK {
		return SlackUser{}, fmt.Errorf("slack users.lookupByEmail: %s", resp.Error)
	}
	return resp.User, nil
}

func (c *SlackAPIClient) GetPermalink(ctx context.Context, accessToken, channelID, messageTS string) (string, error) {
	params := url.Values{
		"channel":    {channelID},
		"message_ts": {messageTS},
	}
	var resp slackPermalinkResponse
	if err := c.slackGet(ctx, accessToken, "chat.getPermalink", params, &resp); err != nil {
		return "", err
	}
	if !resp.OK {
		return "", fmt.Errorf("slack chat.getPermalink: %s", resp.Error)
	}
	return resp.Permalink, nil
}

func (c *SlackAPIClient) FetchFileInfo(ctx context.Context, accessToken, fileID string) (SlackFile, error) {
	params := url.Values{"file": {fileID}}
	var resp slackFileInfoResponse
	if err := c.slackGet(ctx, accessToken, "files.info", params, &resp); err != nil {
		return SlackFile{}, err
	}
	if !resp.OK {
		return SlackFile{}, fmt.Errorf("slack files.info: %s", resp.Error)
	}
	return resp.File, nil
}

func (c *SlackAPIClient) Unfurl(ctx context.Context, accessToken, channelID, messageTS string, unfurls map[string]any) error {
	payload := map[string]any{
		"channel": channelID,
		"ts":      messageTS,
		"unfurls": unfurls,
	}
	var resp slackPostMessageResponse
	if err := c.slackPost(ctx, accessToken, "chat.unfurl", payload, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("slack chat.unfurl: %s", resp.Error)
	}
	return nil
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

func (c *SlackAPIClient) slackPost(ctx context.Context, accessToken, method string, payload any, dest any) error {
	base := c.baseURL
	if base == "" {
		base = slackAPIBase
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal slack request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/%s", base, method), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

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
		responseBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("slack api %s: status %d: %s", method, resp.StatusCode, string(responseBody))
	}
	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return fmt.Errorf("decode slack response: %w", err)
	}
	return nil
}
