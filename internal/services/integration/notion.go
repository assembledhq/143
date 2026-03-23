package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// NotionDocumentStore implements DocumentStore for the Notion API.
// It provides search and document retrieval using Notion's REST API,
// converting Notion's block-based content to readable markdown.
//
// Authentication uses an internal integration token (Bearer auth) with
// a required Notion-Version header. Pages/databases must be explicitly
// shared with the integration before they're accessible.
type NotionDocumentStore struct {
	httpClient *http.Client
	apiURL     string
	authToken  string
	apiVersion string
}

// NotionDocumentStoreConfig holds the connection details for a Notion DocumentStore.
type NotionDocumentStoreConfig struct {
	AuthToken  string // Notion internal integration token
	APIURL     string // defaults to "https://api.notion.com"
	APIVersion string // defaults to "2022-06-28"
}

const (
	notionDefaultAPIURL     = "https://api.notion.com"
	notionDefaultAPIVersion = "2022-06-28"
	notionMaxBlockDepth  = 3
	notionMaxTotalBlocks = 500
	notionPageSize       = 100
)

// NewNotionDocumentStore creates a Notion DocumentStore from the given config.
func NewNotionDocumentStore(cfg NotionDocumentStoreConfig) *NotionDocumentStore {
	apiURL := cfg.APIURL
	if apiURL == "" {
		apiURL = notionDefaultAPIURL
	}
	apiURL = strings.TrimRight(apiURL, "/")

	apiVersion := cfg.APIVersion
	if apiVersion == "" {
		apiVersion = notionDefaultAPIVersion
	}

	return &NotionDocumentStore{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		apiURL:     apiURL,
		authToken:  cfg.AuthToken,
		apiVersion: apiVersion,
	}
}

func (n *NotionDocumentStore) Name() string { return "notion" }

// SearchDocuments finds pages in Notion matching the text query.
// An empty query returns all accessible pages (useful for discovery).
//
// TODO: Use filter.Workspace to scope search to a specific database
// via the Notion search API's filter parameter.
func (n *NotionDocumentStore) SearchDocuments(ctx context.Context, query string, filter DocFilter) ([]DocSummary, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > notionPageSize {
		limit = notionPageSize
	}

	body := notionSearchRequest{
		Query:    query,
		PageSize: limit,
		Filter: &notionSearchFilter{
			Value:    "page",
			Property: "object",
		},
	}

	var resp notionSearchResponse
	if err := n.doRequest(ctx, http.MethodPost, "/v1/search", body, &resp); err != nil {
		return nil, fmt.Errorf("notion search documents: %w", err)
	}

	summaries := make([]DocSummary, 0, len(resp.Results))
	for _, page := range resp.Results {
		summaries = append(summaries, pageToDocSummary(page))
	}
	return summaries, nil
}

// GetDocument fetches a Notion page's metadata and full block content,
// converting the block tree into markdown.
func (n *NotionDocumentStore) GetDocument(ctx context.Context, docID string) (*Document, error) {
	// Fetch page metadata.
	var page notionPage
	if err := n.doRequest(ctx, http.MethodGet, "/v1/pages/"+url.PathEscape(docID), nil, &page); err != nil {
		return nil, fmt.Errorf("notion get page: %w", err)
	}

	// Fetch all blocks (content).
	var totalFetched int
	blocks, err := n.fetchAllBlocks(ctx, docID, 0, &totalFetched)
	if err != nil {
		return nil, fmt.Errorf("notion get page blocks: %w", err)
	}

	content := blocksToMarkdown(blocks, 0)

	summary := pageToDocSummary(page)
	return &Document{
		DocSummary: summary,
		Content:    content,
		Properties: extractPageProperties(page.Properties),
	}, nil
}

// ListAllPages returns all accessible pages (empty query search), paginated.
// This is useful for the initial document discovery run.
func (n *NotionDocumentStore) ListAllPages(ctx context.Context, maxResults int) ([]DocSummary, error) {
	if maxResults <= 0 {
		maxResults = notionPageSize
	}

	var allSummaries []DocSummary
	var cursor string

	for {
		pageSize := notionPageSize
		remaining := maxResults - len(allSummaries)
		if remaining < pageSize {
			pageSize = remaining
		}

		body := notionSearchRequest{
			PageSize: pageSize,
			Filter: &notionSearchFilter{
				Value:    "page",
				Property: "object",
			},
		}
		if cursor != "" {
			body.StartCursor = cursor
		}

		var resp notionSearchResponse
		if err := n.doRequest(ctx, http.MethodPost, "/v1/search", body, &resp); err != nil {
			return allSummaries, fmt.Errorf("notion list all pages: %w", err)
		}

		for _, page := range resp.Results {
			allSummaries = append(allSummaries, pageToDocSummary(page))
		}

		if !resp.HasMore || resp.NextCursor == "" || len(allSummaries) >= maxResults {
			break
		}
		cursor = resp.NextCursor
	}

	return allSummaries, nil
}

// ListDatabases returns all accessible databases. Useful for understanding
// the structure of a Notion workspace during initial discovery.
func (n *NotionDocumentStore) ListDatabases(ctx context.Context) ([]DocSummary, error) {
	body := notionSearchRequest{
		PageSize: notionPageSize,
		Filter: &notionSearchFilter{
			Value:    "database",
			Property: "object",
		},
	}

	var resp notionSearchResponse
	if err := n.doRequest(ctx, http.MethodPost, "/v1/search", body, &resp); err != nil {
		return nil, fmt.Errorf("notion list databases: %w", err)
	}

	summaries := make([]DocSummary, 0, len(resp.Results))
	for _, db := range resp.Results {
		summaries = append(summaries, pageToDocSummary(db))
	}
	return summaries, nil
}

// --- HTTP helper ---

func (n *NotionDocumentStore) doRequest(ctx context.Context, method, path string, body interface{}, target interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	url := n.apiURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+n.authToken)
	req.Header.Set("Notion-Version", n.apiVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := n.httpClient.Do(req) // #nosec G107 -- URL is from internal config
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		var notionErr notionErrorResponse
		if json.Unmarshal(respBody, &notionErr) == nil && notionErr.Message != "" {
			return fmt.Errorf("notion API %d: %s", resp.StatusCode, notionErr.Message)
		}
		return fmt.Errorf("notion API returned %d", resp.StatusCode)
	}

	return json.NewDecoder(resp.Body).Decode(target)
}

// --- Block fetching ---

// fetchAllBlocks recursively fetches all blocks for a page/block, handling
// pagination and nested children up to notionMaxBlockDepth levels. The
// totalFetched counter is shared across recursion levels to enforce the
// global notionMaxTotalBlocks limit.
func (n *NotionDocumentStore) fetchAllBlocks(ctx context.Context, blockID string, depth int, totalFetched *int) ([]notionBlock, error) {
	if depth >= notionMaxBlockDepth {
		return nil, nil
	}

	var allBlocks []notionBlock
	var cursor string

	for {
		path := fmt.Sprintf("/v1/blocks/%s/children?page_size=%d", url.PathEscape(blockID), notionPageSize)
		if cursor != "" {
			path += "&start_cursor=" + url.QueryEscape(cursor)
		}

		var resp notionBlocksResponse
		if err := n.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return allBlocks, fmt.Errorf("fetch blocks for %s: %w", blockID, err)
		}

		for i := range resp.Results {
			block := resp.Results[i]

			// Recursively fetch children if present and within depth limit.
			if block.HasChildren && depth+1 < notionMaxBlockDepth {
				children, err := n.fetchAllBlocks(ctx, block.ID, depth+1, totalFetched)
				if err != nil {
					// Continue with what we have rather than fail entirely.
					break
				}
				block.Children = children
			}

			allBlocks = append(allBlocks, block)
			*totalFetched++

			if *totalFetched >= notionMaxTotalBlocks {
				return allBlocks, nil
			}
		}

		if !resp.HasMore || resp.NextCursor == "" {
			break
		}
		cursor = resp.NextCursor
	}

	return allBlocks, nil
}

// --- Block-to-Markdown conversion ---

// blocksToMarkdown converts a tree of Notion blocks into markdown text.
func blocksToMarkdown(blocks []notionBlock, indent int) string {
	var sb strings.Builder
	prefix := strings.Repeat("  ", indent)

	for i, block := range blocks {
		switch block.Type {
		case "paragraph":
			if block.Paragraph == nil {
				break
			}
			text := richTextToMarkdown(block.Paragraph.RichText)
			if text != "" {
				sb.WriteString(prefix + text + "\n")
			}
			sb.WriteString("\n")

		case "heading_1":
			if block.Heading1 == nil {
				break
			}
			sb.WriteString("# " + richTextToMarkdown(block.Heading1.RichText) + "\n\n")

		case "heading_2":
			if block.Heading2 == nil {
				break
			}
			sb.WriteString("## " + richTextToMarkdown(block.Heading2.RichText) + "\n\n")

		case "heading_3":
			if block.Heading3 == nil {
				break
			}
			sb.WriteString("### " + richTextToMarkdown(block.Heading3.RichText) + "\n\n")

		case "bulleted_list_item":
			if block.BulletedListItem == nil {
				break
			}
			text := richTextToMarkdown(block.BulletedListItem.RichText)
			sb.WriteString(prefix + "- " + text + "\n")
			if len(block.Children) > 0 {
				sb.WriteString(blocksToMarkdown(block.Children, indent+1))
			}

		case "numbered_list_item":
			if block.NumberedListItem == nil {
				break
			}
			text := richTextToMarkdown(block.NumberedListItem.RichText)
			sb.WriteString(prefix + "1. " + text + "\n")
			if len(block.Children) > 0 {
				sb.WriteString(blocksToMarkdown(block.Children, indent+1))
			}

		case "to_do":
			if block.ToDo == nil {
				break
			}
			checkbox := "[ ]"
			if block.ToDo.Checked {
				checkbox = "[x]"
			}
			text := richTextToMarkdown(block.ToDo.RichText)
			sb.WriteString(prefix + "- " + checkbox + " " + text + "\n")

		case "toggle":
			if block.Toggle == nil {
				break
			}
			text := richTextToMarkdown(block.Toggle.RichText)
			sb.WriteString(prefix + "**" + text + "**\n")
			if len(block.Children) > 0 {
				sb.WriteString(blocksToMarkdown(block.Children, indent+1))
			}
			sb.WriteString("\n")

		case "code":
			if block.Code == nil {
				break
			}
			lang := block.Code.Language
			text := richTextToMarkdown(block.Code.RichText)
			sb.WriteString("```" + lang + "\n" + text + "\n```\n\n")

		case "quote":
			if block.Quote == nil {
				break
			}
			text := richTextToMarkdown(block.Quote.RichText)
			for _, line := range strings.Split(text, "\n") {
				sb.WriteString(prefix + "> " + line + "\n")
			}
			if len(block.Children) > 0 {
				sb.WriteString(blocksToMarkdown(block.Children, indent))
			}
			sb.WriteString("\n")

		case "callout":
			if block.Callout == nil {
				break
			}
			icon := ""
			if block.Callout.Icon != nil && block.Callout.Icon.Emoji != "" {
				icon = block.Callout.Icon.Emoji + " "
			}
			text := richTextToMarkdown(block.Callout.RichText)
			sb.WriteString(prefix + "> " + icon + text + "\n\n")

		case "divider":
			sb.WriteString("---\n\n")

		case "image":
			if block.Image == nil {
				break
			}
			caption := richTextToMarkdown(block.Image.Caption)
			url := ""
			if block.Image.File != nil {
				url = block.Image.File.URL
			} else if block.Image.External != nil {
				url = block.Image.External.URL
			}
			if url != "" {
				sb.WriteString("![" + caption + "](" + url + ")\n\n")
			}

		case "bookmark":
			if block.Bookmark == nil {
				break
			}
			url := block.Bookmark.URL
			caption := richTextToMarkdown(block.Bookmark.Caption)
			if caption == "" {
				caption = url
			}
			sb.WriteString("[" + caption + "](" + url + ")\n\n")

		case "table":
			if block.Table == nil {
				break
			}
			if len(block.Children) > 0 {
				sb.WriteString(tableBlocksToMarkdown(block.Children, block.Table.HasColumnHeader))
				sb.WriteString("\n")
			}

		case "child_page":
			if block.ChildPage == nil {
				break
			}
			sb.WriteString(prefix + "[Page: " + block.ChildPage.Title + "]\n\n")

		case "child_database":
			if block.ChildDatabase == nil {
				break
			}
			sb.WriteString(prefix + "[Database: " + block.ChildDatabase.Title + "]\n\n")

		case "column_list":
			// Render column children sequentially.
			if len(block.Children) > 0 {
				sb.WriteString(blocksToMarkdown(block.Children, indent))
			}

		case "column":
			if len(block.Children) > 0 {
				sb.WriteString(blocksToMarkdown(block.Children, indent))
			}

		default:
			// Skip unsupported block types silently.
		}

		// Add spacing between adjacent list-terminating blocks.
		if i+1 < len(blocks) && isListItem(block.Type) && !isListItem(blocks[i+1].Type) {
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

func isListItem(blockType string) bool {
	return blockType == "bulleted_list_item" || blockType == "numbered_list_item" || blockType == "to_do"
}

// tableBlocksToMarkdown converts table_row blocks into a markdown table.
func tableBlocksToMarkdown(rows []notionBlock, hasHeader bool) string {
	if len(rows) == 0 {
		return ""
	}

	var sb strings.Builder
	rowCount := 0

	for _, row := range rows {
		if row.Type != "table_row" || row.TableRow == nil {
			continue
		}

		cells := make([]string, len(row.TableRow.Cells))
		for j, cell := range row.TableRow.Cells {
			cells[j] = richTextToMarkdown(cell)
		}

		sb.WriteString("| " + strings.Join(cells, " | ") + " |\n")

		// Add header separator after first data row if it's a header.
		if rowCount == 0 && hasHeader {
			separators := make([]string, len(cells))
			for j := range separators {
				separators[j] = "---"
			}
			sb.WriteString("| " + strings.Join(separators, " | ") + " |\n")
		}
		rowCount++
	}

	return sb.String()
}

// richTextToMarkdown converts a Notion rich text array into markdown.
func richTextToMarkdown(rt []notionRichText) string {
	var sb strings.Builder
	for _, t := range rt {
		text := t.PlainText

		// Apply annotations in order: code first (innermost), then others.
		if t.Annotations.Code {
			text = "`" + text + "`"
		}
		if t.Annotations.Bold {
			text = "**" + text + "**"
		}
		if t.Annotations.Italic {
			text = "*" + text + "*"
		}
		if t.Annotations.Strikethrough {
			text = "~~" + text + "~~"
		}

		// Links wrap the entire annotated text.
		if t.Href != "" {
			text = "[" + text + "](" + t.Href + ")"
		}

		sb.WriteString(text)
	}
	return sb.String()
}

// --- Page helpers ---

func pageToDocSummary(page notionPage) DocSummary {
	title := extractPageTitle(page.Properties)
	if title == "" {
		// Databases use a "title" array at top level.
		for _, titleItem := range page.Title {
			title += titleItem.PlainText
		}
	}
	if title == "" {
		title = "Untitled"
	}

	var author string
	if page.CreatedBy.Name != "" {
		author = page.CreatedBy.Name
	}

	lastEdited, _ := time.Parse(time.RFC3339, page.LastEditedTime)

	return DocSummary{
		ID:         page.ID,
		Title:      title,
		LastEdited: lastEdited,
		Author:     author,
		WebURL:     page.URL,
	}
}

// extractPageTitle finds the title property in a Notion page's properties.
func extractPageTitle(properties map[string]notionProperty) string {
	for _, prop := range properties {
		if prop.Type == "title" {
			var title string
			for _, t := range prop.Title {
				title += t.PlainText
			}
			return title
		}
	}
	return ""
}

// extractPageProperties converts non-title Notion properties into a simple
// string map for the Document.Properties field.
func extractPageProperties(properties map[string]notionProperty) map[string]string {
	result := make(map[string]string)
	for name, prop := range properties {
		switch prop.Type {
		case "title":
			// Already used as the document title.
			continue
		case "rich_text":
			var text string
			for _, t := range prop.RichText {
				text += t.PlainText
			}
			if text != "" {
				result[name] = text
			}
		case "select":
			if prop.Select != nil {
				result[name] = prop.Select.Name
			}
		case "multi_select":
			var names []string
			for _, opt := range prop.MultiSelect {
				names = append(names, opt.Name)
			}
			if len(names) > 0 {
				result[name] = strings.Join(names, ", ")
			}
		case "status":
			if prop.Status != nil {
				result[name] = prop.Status.Name
			}
		case "date":
			if prop.Date != nil && prop.Date.Start != "" {
				result[name] = prop.Date.Start
			}
		case "checkbox":
			if prop.Checkbox {
				result[name] = "true"
			}
		case "url":
			if prop.URL != "" {
				result[name] = prop.URL
			}
		case "number":
			if prop.Number != nil {
				result[name] = fmt.Sprintf("%v", *prop.Number)
			}
		}
	}
	return result
}

// --- Notion API types ---

type notionErrorResponse struct {
	Status  int    `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type notionSearchRequest struct {
	Query       string              `json:"query,omitempty"`
	Filter      *notionSearchFilter `json:"filter,omitempty"`
	PageSize    int                 `json:"page_size,omitempty"`
	StartCursor string             `json:"start_cursor,omitempty"`
}

type notionSearchFilter struct {
	Value    string `json:"value"`
	Property string `json:"property"`
}

type notionSearchResponse struct {
	Results    []notionPage `json:"results"`
	HasMore    bool         `json:"has_more"`
	NextCursor string      `json:"next_cursor"`
}

type notionPage struct {
	ID             string                      `json:"id"`
	URL            string                      `json:"url"`
	CreatedTime    string                      `json:"created_time"`
	LastEditedTime string                      `json:"last_edited_time"`
	CreatedBy      notionUser                  `json:"created_by"`
	Properties     map[string]notionProperty   `json:"properties"`
	Title          []notionRichText            `json:"title"` // databases use this
}

type notionUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type notionProperty struct {
	Type        string           `json:"type"`
	Title       []notionRichText `json:"title,omitempty"`
	RichText    []notionRichText `json:"rich_text,omitempty"`
	Select      *notionOption    `json:"select,omitempty"`
	MultiSelect []notionOption   `json:"multi_select,omitempty"`
	Status      *notionOption    `json:"status,omitempty"`
	Date        *notionDate      `json:"date,omitempty"`
	Checkbox    bool             `json:"checkbox,omitempty"`
	URL         string           `json:"url,omitempty"`
	Number      *float64         `json:"number,omitempty"`
}

type notionOption struct {
	Name string `json:"name"`
}

type notionDate struct {
	Start string `json:"start"`
	End   string `json:"end,omitempty"`
}

type notionBlocksResponse struct {
	Results    []notionBlock `json:"results"`
	HasMore    bool          `json:"has_more"`
	NextCursor string       `json:"next_cursor"`
}

type notionBlock struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	HasChildren     bool   `json:"has_children"`

	// Type-specific content. Only the field matching Type is populated.
	Paragraph        *notionTextBlock   `json:"paragraph,omitempty"`
	Heading1         *notionTextBlock   `json:"heading_1,omitempty"`
	Heading2         *notionTextBlock   `json:"heading_2,omitempty"`
	Heading3         *notionTextBlock   `json:"heading_3,omitempty"`
	BulletedListItem *notionTextBlock   `json:"bulleted_list_item,omitempty"`
	NumberedListItem *notionTextBlock   `json:"numbered_list_item,omitempty"`
	ToDo             *notionToDoBlock   `json:"to_do,omitempty"`
	Toggle           *notionTextBlock   `json:"toggle,omitempty"`
	Code             *notionCodeBlock   `json:"code,omitempty"`
	Quote            *notionTextBlock   `json:"quote,omitempty"`
	Callout          *notionCalloutBlock `json:"callout,omitempty"`
	Image            *notionFileBlock   `json:"image,omitempty"`
	Bookmark         *notionBookmark    `json:"bookmark,omitempty"`
	Table            *notionTableBlock  `json:"table,omitempty"`
	TableRow         *notionTableRow    `json:"table_row,omitempty"`
	ChildPage        *notionChildRef    `json:"child_page,omitempty"`
	ChildDatabase    *notionChildRef    `json:"child_database,omitempty"`

	// Populated by fetchAllBlocks for blocks with has_children=true.
	Children []notionBlock `json:"-"`
}

type notionTextBlock struct {
	RichText []notionRichText `json:"rich_text"`
}

type notionToDoBlock struct {
	RichText []notionRichText `json:"rich_text"`
	Checked  bool             `json:"checked"`
}

type notionCodeBlock struct {
	RichText []notionRichText `json:"rich_text"`
	Language string           `json:"language"`
}

type notionCalloutBlock struct {
	RichText []notionRichText `json:"rich_text"`
	Icon     *notionIcon      `json:"icon,omitempty"`
}

type notionIcon struct {
	Type  string `json:"type"`
	Emoji string `json:"emoji,omitempty"`
}

type notionFileBlock struct {
	Caption  []notionRichText `json:"caption"`
	File     *notionFileRef   `json:"file,omitempty"`
	External *notionFileRef   `json:"external,omitempty"`
}

type notionFileRef struct {
	URL string `json:"url"`
}

type notionBookmark struct {
	URL     string           `json:"url"`
	Caption []notionRichText `json:"caption"`
}

type notionTableBlock struct {
	TableWidth     int  `json:"table_width"`
	HasColumnHeader bool `json:"has_column_header"`
	HasRowHeader   bool `json:"has_row_header"`
}

type notionTableRow struct {
	Cells [][]notionRichText `json:"cells"`
}

type notionChildRef struct {
	Title string `json:"title"`
}

type notionRichText struct {
	PlainText   string              `json:"plain_text"`
	Href        string              `json:"href,omitempty"`
	Annotations notionAnnotations   `json:"annotations"`
}

type notionAnnotations struct {
	Bold          bool `json:"bold"`
	Italic        bool `json:"italic"`
	Strikethrough bool `json:"strikethrough"`
	Underline     bool `json:"underline"`
	Code          bool `json:"code"`
}
