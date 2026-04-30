package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNotionName(t *testing.T) {
	t.Parallel()
	store := NewNotionDocumentStore(NotionDocumentStoreConfig{AuthToken: "test"})
	require.Equal(t, "notion", store.Name())
}

func TestNotionSearchDocuments(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v1/search", r.URL.Path)
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		require.Equal(t, "2022-06-28", r.Header.Get("Notion-Version"))
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var body notionSearchRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "roadmap", body.Query)
		require.Equal(t, "page", body.Filter.Value)

		resp := notionSearchResponse{
			Results: []notionPage{
				{
					ID:             "page-1",
					URL:            "https://notion.so/page-1",
					LastEditedTime: "2025-01-15T10:00:00.000Z",
					CreatedBy:      notionUser{Name: "Alice"},
					Properties: map[string]notionProperty{
						"Name": {
							Type:  "title",
							Title: []notionRichText{{PlainText: "Product Roadmap Q1"}},
						},
					},
				},
				{
					ID:             "page-2",
					URL:            "https://notion.so/page-2",
					LastEditedTime: "2025-01-10T08:00:00.000Z",
					CreatedBy:      notionUser{Name: "Bob"},
					Properties: map[string]notionProperty{
						"Name": {
							Type:  "title",
							Title: []notionRichText{{PlainText: "Engineering Roadmap"}},
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	store := NewNotionDocumentStore(NotionDocumentStoreConfig{
		AuthToken: "test-token",
		APIURL:    srv.URL,
	})

	results, err := store.SearchDocuments(context.Background(), "roadmap", DocFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, results, 2)

	require.Equal(t, "page-1", results[0].ID)
	require.Equal(t, "Product Roadmap Q1", results[0].Title)
	require.Equal(t, "Alice", results[0].Author)
	require.Equal(t, "https://notion.so/page-1", results[0].WebURL)

	require.Equal(t, "page-2", results[1].ID)
	require.Equal(t, "Engineering Roadmap", results[1].Title)
}

func TestNotionSearchDocumentsEmpty(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := notionSearchResponse{Results: []notionPage{}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	store := NewNotionDocumentStore(NotionDocumentStoreConfig{
		AuthToken: "test-token",
		APIURL:    srv.URL,
	})

	results, err := store.SearchDocuments(context.Background(), "nonexistent", DocFilter{})
	require.NoError(t, err)
	require.Empty(t, results)
}

func TestNotionSearchDocumentsAuthError(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(notionErrorResponse{
			Status:  401,
			Code:    "unauthorized",
			Message: "API token is invalid.",
		})
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	store := NewNotionDocumentStore(NotionDocumentStoreConfig{
		AuthToken: "bad-token",
		APIURL:    srv.URL,
	})

	_, err := store.SearchDocuments(context.Background(), "test", DocFilter{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "API token is invalid")
}

func TestNotionGetDocument(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()

	mux.HandleFunc("/v1/pages/page-123", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		page := notionPage{
			ID:             "page-123",
			URL:            "https://notion.so/page-123",
			LastEditedTime: "2025-01-15T10:00:00.000Z",
			CreatedBy:      notionUser{Name: "Alice"},
			Properties: map[string]notionProperty{
				"Name": {
					Type:  "title",
					Title: []notionRichText{{PlainText: "Architecture Decision"}},
				},
				"Status": {
					Type:   "select",
					Select: &notionOption{Name: "Approved"},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(page)
	})

	mux.HandleFunc("/v1/blocks/page-123/children", func(w http.ResponseWriter, r *http.Request) {
		resp := notionBlocksResponse{
			Results: []notionBlock{
				{
					ID:   "block-1",
					Type: "heading_1",
					Heading1: &notionTextBlock{
						RichText: []notionRichText{{PlainText: "Overview"}},
					},
				},
				{
					ID:   "block-2",
					Type: "paragraph",
					Paragraph: &notionTextBlock{
						RichText: []notionRichText{{PlainText: "This is the architecture for our system."}},
					},
				},
				{
					ID:   "block-3",
					Type: "bulleted_list_item",
					BulletedListItem: &notionTextBlock{
						RichText: []notionRichText{{PlainText: "First point"}},
					},
				},
				{
					ID:   "block-4",
					Type: "bulleted_list_item",
					BulletedListItem: &notionTextBlock{
						RichText: []notionRichText{{PlainText: "Second point"}},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	store := NewNotionDocumentStore(NotionDocumentStoreConfig{
		AuthToken: "test-token",
		APIURL:    srv.URL,
	})

	doc, err := store.GetDocument(context.Background(), "page-123")
	require.NoError(t, err)
	require.Equal(t, "page-123", doc.ID)
	require.Equal(t, "Architecture Decision", doc.Title)
	require.Equal(t, "Approved", doc.Properties["Status"])
	require.Contains(t, doc.Content, "# Overview")
	require.Contains(t, doc.Content, "This is the architecture for our system.")
	require.Contains(t, doc.Content, "- First point")
	require.Contains(t, doc.Content, "- Second point")
}

func TestNotionGetDocumentPagination(t *testing.T) {
	t.Parallel()

	callCount := 0
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/pages/page-paginated", func(w http.ResponseWriter, r *http.Request) {
		page := notionPage{
			ID:  "page-paginated",
			URL: "https://notion.so/page-paginated",
			Properties: map[string]notionProperty{
				"Name": {Type: "title", Title: []notionRichText{{PlainText: "Paginated Doc"}}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(page)
	})

	mux.HandleFunc("/v1/blocks/page-paginated/children", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		cursor := r.URL.Query().Get("start_cursor")

		var resp notionBlocksResponse
		if cursor == "" {
			resp = notionBlocksResponse{
				Results: []notionBlock{
					{ID: "b1", Type: "paragraph", Paragraph: &notionTextBlock{
						RichText: []notionRichText{{PlainText: "Page one content"}},
					}},
				},
				HasMore:    true,
				NextCursor: "cursor-2",
			}
		} else {
			resp = notionBlocksResponse{
				Results: []notionBlock{
					{ID: "b2", Type: "paragraph", Paragraph: &notionTextBlock{
						RichText: []notionRichText{{PlainText: "Page two content"}},
					}},
				},
				HasMore: false,
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	store := NewNotionDocumentStore(NotionDocumentStoreConfig{
		AuthToken: "test-token",
		APIURL:    srv.URL,
	})

	doc, err := store.GetDocument(context.Background(), "page-paginated")
	require.NoError(t, err)
	require.Contains(t, doc.Content, "Page one content")
	require.Contains(t, doc.Content, "Page two content")
	require.Equal(t, 2, callCount)
}

func TestNotionGetDocumentNotFound(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(notionErrorResponse{
			Status:  404,
			Code:    "object_not_found",
			Message: "Could not find page with ID: bad-id.",
		})
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	store := NewNotionDocumentStore(NotionDocumentStoreConfig{
		AuthToken: "test-token",
		APIURL:    srv.URL,
	})

	_, err := store.GetDocument(context.Background(), "bad-id")
	require.Error(t, err)
	require.Contains(t, err.Error(), "Could not find page")
}

func TestNotionRateLimitError(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(notionErrorResponse{
			Status:  429,
			Code:    "rate_limited",
			Message: "Rate limited. Please retry after a short delay.",
		})
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	store := NewNotionDocumentStore(NotionDocumentStoreConfig{
		AuthToken: "test-token",
		APIURL:    srv.URL,
	})

	_, err := store.SearchDocuments(context.Background(), "test", DocFilter{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "Rate limited")
}

func TestBlocksToMarkdown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		blocks   []notionBlock
		contains []string
	}{
		{
			name: "headings",
			blocks: []notionBlock{
				{Type: "heading_1", Heading1: &notionTextBlock{RichText: []notionRichText{{PlainText: "H1"}}}},
				{Type: "heading_2", Heading2: &notionTextBlock{RichText: []notionRichText{{PlainText: "H2"}}}},
				{Type: "heading_3", Heading3: &notionTextBlock{RichText: []notionRichText{{PlainText: "H3"}}}},
			},
			contains: []string{"# H1", "## H2", "### H3"},
		},
		{
			name: "code block",
			blocks: []notionBlock{
				{Type: "code", Code: &notionCodeBlock{
					Language: "python",
					RichText: []notionRichText{{PlainText: "print('hello')"}},
				}},
			},
			contains: []string{"```python", "print('hello')", "```"},
		},
		{
			name: "to-do items",
			blocks: []notionBlock{
				{Type: "to_do", ToDo: &notionToDoBlock{
					RichText: []notionRichText{{PlainText: "Done task"}},
					Checked:  true,
				}},
				{Type: "to_do", ToDo: &notionToDoBlock{
					RichText: []notionRichText{{PlainText: "Open task"}},
					Checked:  false,
				}},
			},
			contains: []string{"- [x] Done task", "- [ ] Open task"},
		},
		{
			name: "quote and callout",
			blocks: []notionBlock{
				{Type: "quote", Quote: &notionTextBlock{
					RichText: []notionRichText{{PlainText: "A wise quote"}},
				}},
				{Type: "callout", Callout: &notionCalloutBlock{
					RichText: []notionRichText{{PlainText: "Important note"}},
					Icon:     &notionIcon{Emoji: "💡"},
				}},
			},
			contains: []string{"> A wise quote", "> 💡 Important note"},
		},
		{
			name: "divider",
			blocks: []notionBlock{
				{Type: "divider"},
			},
			contains: []string{"---"},
		},
		{
			name: "image",
			blocks: []notionBlock{
				{Type: "image", Image: &notionFileBlock{
					Caption:  []notionRichText{{PlainText: "diagram"}},
					External: &notionFileRef{URL: "https://example.com/img.png"},
				}},
			},
			contains: []string{"![diagram](https://example.com/img.png)"},
		},
		{
			name: "bookmark",
			blocks: []notionBlock{
				{Type: "bookmark", Bookmark: &notionBookmark{
					URL: "https://example.com",
				}},
			},
			contains: []string{"[https://example.com](https://example.com)"},
		},
		{
			name: "child page and database",
			blocks: []notionBlock{
				{Type: "child_page", ChildPage: &notionChildRef{Title: "Sub Page"}},
				{Type: "child_database", ChildDatabase: &notionChildRef{Title: "Tasks DB"}},
			},
			contains: []string{"[Page: Sub Page]", "[Database: Tasks DB]"},
		},
		{
			name: "nested list items",
			blocks: []notionBlock{
				{
					Type:             "bulleted_list_item",
					BulletedListItem: &notionTextBlock{RichText: []notionRichText{{PlainText: "Parent"}}},
					Children: []notionBlock{
						{
							Type:             "bulleted_list_item",
							BulletedListItem: &notionTextBlock{RichText: []notionRichText{{PlainText: "Child"}}},
						},
					},
				},
			},
			contains: []string{"- Parent", "  - Child"},
		},
		{
			name: "table with header",
			blocks: []notionBlock{
				{
					Type:  "table",
					Table: &notionTableBlock{HasColumnHeader: true, TableWidth: 2},
					Children: []notionBlock{
						{Type: "table_row", TableRow: &notionTableRow{
							Cells: [][]notionRichText{
								{{PlainText: "Name"}},
								{{PlainText: "Value"}},
							},
						}},
						{Type: "table_row", TableRow: &notionTableRow{
							Cells: [][]notionRichText{
								{{PlainText: "foo"}},
								{{PlainText: "bar"}},
							},
						}},
					},
				},
			},
			contains: []string{"| Name | Value |", "| --- | --- |", "| foo | bar |"},
		},
		{
			name: "nil content fields do not panic",
			blocks: []notionBlock{
				{Type: "paragraph", Paragraph: nil},
				{Type: "heading_1", Heading1: nil},
				{Type: "heading_2", Heading2: nil},
				{Type: "heading_3", Heading3: nil},
				{Type: "bulleted_list_item", BulletedListItem: nil},
				{Type: "numbered_list_item", NumberedListItem: nil},
				{Type: "to_do", ToDo: nil},
				{Type: "toggle", Toggle: nil},
				{Type: "code", Code: nil},
				{Type: "quote", Quote: nil},
				{Type: "callout", Callout: nil},
				{Type: "image", Image: nil},
				{Type: "bookmark", Bookmark: nil},
				{Type: "table", Table: nil},
				{Type: "child_page", ChildPage: nil},
				{Type: "child_database", ChildDatabase: nil},
			},
			contains: []string{}, // should produce empty output without panicking
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := blocksToMarkdown(tt.blocks, 0)
			for _, expected := range tt.contains {
				require.Contains(t, result, expected, "expected %q in output:\n%s", expected, result)
			}
		})
	}
}

func TestRichTextToMarkdown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		richText []notionRichText
		want     string
	}{
		{
			name:     "plain text",
			richText: []notionRichText{{PlainText: "Hello world"}},
			want:     "Hello world",
		},
		{
			name: "bold text",
			richText: []notionRichText{{
				PlainText:   "bold",
				Annotations: notionAnnotations{Bold: true},
			}},
			want: "**bold**",
		},
		{
			name: "italic text",
			richText: []notionRichText{{
				PlainText:   "italic",
				Annotations: notionAnnotations{Italic: true},
			}},
			want: "*italic*",
		},
		{
			name: "code text",
			richText: []notionRichText{{
				PlainText:   "code",
				Annotations: notionAnnotations{Code: true},
			}},
			want: "`code`",
		},
		{
			name: "strikethrough text",
			richText: []notionRichText{{
				PlainText:   "deleted",
				Annotations: notionAnnotations{Strikethrough: true},
			}},
			want: "~~deleted~~",
		},
		{
			name: "link",
			richText: []notionRichText{{
				PlainText: "click here",
				Href:      "https://example.com",
			}},
			want: "[click here](https://example.com)",
		},
		{
			name: "bold italic",
			richText: []notionRichText{{
				PlainText:   "emphasis",
				Annotations: notionAnnotations{Bold: true, Italic: true},
			}},
			want: "***emphasis***",
		},
		{
			name: "multiple segments",
			richText: []notionRichText{
				{PlainText: "Hello "},
				{PlainText: "world", Annotations: notionAnnotations{Bold: true}},
				{PlainText: "!"},
			},
			want: "Hello **world**!",
		},
		{
			name:     "empty",
			richText: nil,
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := richTextToMarkdown(tt.richText)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestNotionListAllPages(t *testing.T) {
	t.Parallel()

	callCount := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var body notionSearchRequest
		json.NewDecoder(r.Body).Decode(&body)

		var resp notionSearchResponse
		if body.StartCursor == "" {
			resp = notionSearchResponse{
				Results: []notionPage{
					{ID: "p1", Properties: map[string]notionProperty{
						"Name": {Type: "title", Title: []notionRichText{{PlainText: "Page 1"}}},
					}},
				},
				HasMore:    true,
				NextCursor: "cur-2",
			}
		} else {
			resp = notionSearchResponse{
				Results: []notionPage{
					{ID: "p2", Properties: map[string]notionProperty{
						"Name": {Type: "title", Title: []notionRichText{{PlainText: "Page 2"}}},
					}},
				},
				HasMore: false,
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	store := NewNotionDocumentStore(NotionDocumentStoreConfig{
		AuthToken: "test-token",
		APIURL:    srv.URL,
	})

	pages, err := store.ListAllPages(context.Background(), 200)
	require.NoError(t, err)
	require.Len(t, pages, 2)
	require.Equal(t, "Page 1", pages[0].Title)
	require.Equal(t, "Page 2", pages[1].Title)
	require.Equal(t, 2, callCount)
}

func TestNotionListDatabases(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body notionSearchRequest
		json.NewDecoder(r.Body).Decode(&body)
		require.Equal(t, "database", body.Filter.Value)

		resp := notionSearchResponse{
			Results: []notionPage{
				{
					ID:    "db-1",
					URL:   "https://notion.so/db-1",
					Title: []notionRichText{{PlainText: "Sprint Board"}},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	store := NewNotionDocumentStore(NotionDocumentStoreConfig{
		AuthToken: "test-token",
		APIURL:    srv.URL,
	})

	dbs, err := store.ListDatabases(context.Background())
	require.NoError(t, err)
	require.Len(t, dbs, 1)
	require.Equal(t, "Sprint Board", dbs[0].Title)
}

func TestExtractPageProperties(t *testing.T) {
	t.Parallel()

	num := 42.0
	props := map[string]notionProperty{
		"Name":   {Type: "title", Title: []notionRichText{{PlainText: "Test"}}},
		"Status": {Type: "select", Select: &notionOption{Name: "Active"}},
		"Tags": {Type: "multi_select", MultiSelect: []notionOption{
			{Name: "backend"}, {Name: "urgent"},
		}},
		"Priority": {Type: "number", Number: &num},
		"Done":     {Type: "checkbox", Checkbox: true},
		"URL":      {Type: "url", URL: "https://example.com"},
		"Due":      {Type: "date", Date: &notionDate{Start: "2025-03-01"}},
		"Notes": {Type: "rich_text", RichText: []notionRichText{
			{PlainText: "Some notes"},
		}},
	}

	result := extractPageProperties(props)

	// Title should be excluded.
	_, hasName := result["Name"]
	require.False(t, hasName)

	require.Equal(t, "Active", result["Status"])
	require.Equal(t, "backend, urgent", result["Tags"])
	require.Equal(t, "42", result["Priority"])
	require.Equal(t, "true", result["Done"])
	require.Equal(t, "https://example.com", result["URL"])
	require.Equal(t, "2025-03-01", result["Due"])
	require.Equal(t, "Some notes", result["Notes"])
}

func TestNotionServerError(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	store := NewNotionDocumentStore(NotionDocumentStoreConfig{
		AuthToken: "test-token",
		APIURL:    srv.URL,
	})

	_, err := store.SearchDocuments(context.Background(), "test", DocFilter{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "500")
}

func TestNotionSearchDocuments_WorkspaceFilterByUUID(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/databases/a1b2c3d4-e5f6-7890-abcd-ef1234567890/query", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method, "should POST to database query endpoint")

		resp := notionSearchResponse{
			Results: []notionPage{
				{
					ID:  "page-in-db",
					URL: "https://notion.so/page-in-db",
					Properties: map[string]notionProperty{
						"Name": {Type: "title", Title: []notionRichText{{PlainText: "Task Alpha"}}},
					},
				},
				{
					ID:  "page-in-db-2",
					URL: "https://notion.so/page-in-db-2",
					Properties: map[string]notionProperty{
						"Name": {Type: "title", Title: []notionRichText{{PlainText: "Unrelated Page"}}},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	store := NewNotionDocumentStore(NotionDocumentStoreConfig{
		AuthToken: "test-token",
		APIURL:    srv.URL,
	})

	// With query filter — should client-side filter by title.
	results, err := store.SearchDocuments(context.Background(), "Alpha", DocFilter{
		Workspace: "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
		Limit:     10,
	})
	require.NoError(t, err, "search with UUID workspace should succeed")
	require.Len(t, results, 1, "should filter to matching title only")
	require.Equal(t, "Task Alpha", results[0].Title, "should return the matching page")
}

func TestNotionSearchDocuments_WorkspaceFilterByName(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()

	// ListDatabases is called to resolve the name.
	mux.HandleFunc("/v1/search", func(w http.ResponseWriter, r *http.Request) {
		var body notionSearchRequest
		json.NewDecoder(r.Body).Decode(&body)
		require.Equal(t, "database", body.Filter.Value, "should search for databases")

		resp := notionSearchResponse{
			Results: []notionPage{
				{
					ID:    "resolved-db-id",
					Title: []notionRichText{{PlainText: "Sprint Board"}},
				},
				{
					ID:    "other-db-id",
					Title: []notionRichText{{PlainText: "Knowledge Base"}},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	// Database query for the resolved ID.
	mux.HandleFunc("/v1/databases/resolved-db-id/query", func(w http.ResponseWriter, r *http.Request) {
		resp := notionSearchResponse{
			Results: []notionPage{
				{
					ID:  "sprint-page",
					URL: "https://notion.so/sprint-page",
					Properties: map[string]notionProperty{
						"Name": {Type: "title", Title: []notionRichText{{PlainText: "Sprint 42"}}},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	store := NewNotionDocumentStore(NotionDocumentStoreConfig{
		AuthToken: "test-token",
		APIURL:    srv.URL,
	})

	// Case-insensitive name match.
	results, err := store.SearchDocuments(context.Background(), "", DocFilter{
		Workspace: "sprint board",
		Limit:     10,
	})
	require.NoError(t, err, "search with name workspace should succeed")
	require.Len(t, results, 1, "should return pages from the resolved database")
	require.Equal(t, "Sprint 42", results[0].Title, "should return the page from Sprint Board db")
}

func TestNotionSearchDocuments_WorkspaceNameNotFound(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := notionSearchResponse{
			Results: []notionPage{
				{ID: "db-1", Title: []notionRichText{{PlainText: "Other DB"}}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	store := NewNotionDocumentStore(NotionDocumentStoreConfig{
		AuthToken: "test-token",
		APIURL:    srv.URL,
	})

	_, err := store.SearchDocuments(context.Background(), "test", DocFilter{
		Workspace: "Nonexistent DB",
	})
	require.Error(t, err, "should error when workspace name not found")
	require.Contains(t, err.Error(), "no database found matching", "error should indicate name not found")
}
