package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

const (
	codeReviewTextPageSize           = 100
	codeReviewTextMaxDiscussionItems = 500
	codeReviewTextMaxBytes           = 256 * 1024
)

// CodeReviewTextSource is a bounded, exact GitHub Markdown body. It is untrusted
// content even when its author is a human. The caller chooses which sections
// qualify as evidence and retains all other text for intent-change detection.
type CodeReviewTextSource struct {
	Surface          models.CodeReviewEvidenceSurface
	ProviderObjectID string
	SourceURL        string
	AuthorLogin      string
	Body             string
}

type CodeReviewTextDiscovery struct {
	HeadSHA  string
	Body     string
	Sources  []CodeReviewTextSource
	Complete bool
}

// CodeReviewEvidenceDiscovery derives Markdown and rendered-image evidence from
// one GitHub inventory. It is local to a capture; callers must not reuse it as a
// freshness check for another capture.
type CodeReviewEvidenceDiscovery struct {
	Text   CodeReviewTextDiscovery
	Visual models.CodeReviewVisualEvidenceDiscovery
}

// codeReviewTextInventory retains bounded text while visual discovery continues
// through every page to count images. Each discussion surface owns one inventory.
type codeReviewTextInventory struct {
	sources        []CodeReviewTextSource
	visited, bytes int
	incomplete     bool
}

func (i *codeReviewTextInventory) add(surface models.CodeReviewEvidenceSurface, items []githubVisualEvidenceContent) {
	i.visited += len(items)
	if i.incomplete || i.visited > codeReviewTextMaxDiscussionItems {
		i.incomplete = true
		i.sources = nil
		return
	}
	for _, item := range items {
		if item.User == nil || !codeReviewEvidenceAuthorType(item.User.Type).IsHuman() || strings.TrimSpace(item.User.Login) == "" {
			continue
		}
		i.bytes += len(item.Body)
		if i.bytes > codeReviewTextMaxBytes {
			i.incomplete = true
			i.sources = nil
			return
		}
		i.sources = append(i.sources, CodeReviewTextSource{Surface: surface, ProviderObjectID: fmt.Sprint(item.ID), SourceURL: item.HTMLURL, AuthorLogin: strings.TrimSpace(item.User.Login), Body: item.Body})
	}
}

func codeReviewTextFromInventories(headSHA, body string, inventories ...codeReviewTextInventory) CodeReviewTextDiscovery {
	result := CodeReviewTextDiscovery{HeadSHA: headSHA, Body: body, Complete: true}
	visited, totalBytes := 0, len(body)
	if totalBytes > codeReviewTextMaxBytes {
		return CodeReviewTextDiscovery{HeadSHA: headSHA, Complete: false}
	}
	for _, inventory := range inventories {
		visited += inventory.visited
		totalBytes += inventory.bytes
		if inventory.incomplete || visited > codeReviewTextMaxDiscussionItems || totalBytes > codeReviewTextMaxBytes {
			return CodeReviewTextDiscovery{HeadSHA: headSHA, Complete: false}
		}
		result.Sources = append(result.Sources, inventory.sources...)
	}
	sort.Slice(result.Sources, func(i, j int) bool {
		a, b := result.Sources[i], result.Sources[j]
		if a.Surface != b.Surface {
			return a.Surface < b.Surface
		}
		return a.ProviderObjectID < b.ProviderObjectID
	})
	return result
}

// DiscoverCodeReviewTextEvidence reads the PR body and all human discussion
// bodies. Bot/app output is excluded to avoid reacting to our own publication.
// An over-budget inventory is explicitly incomplete and cannot support reuse.
func (s *PRService) DiscoverCodeReviewTextEvidence(ctx context.Context, orgID, repositoryID uuid.UUID, number int) (CodeReviewTextDiscovery, error) {
	if s == nil || s.repos == nil || orgID == uuid.Nil || repositoryID == uuid.Nil || number <= 0 {
		return CodeReviewTextDiscovery{}, fmt.Errorf("text evidence requires repository and PR identity")
	}
	repository, err := s.repos.GetByID(ctx, orgID, repositoryID)
	if err != nil {
		return CodeReviewTextDiscovery{}, err
	}
	resolution, err := s.getInstallationResolutionForRepo(ctx, orgID, &repository)
	if err != nil {
		return CodeReviewTextDiscovery{}, err
	}
	ctx = withGitHubResolutionContext(ctx, resolution, repository.InstallationID, "code_review_text_evidence")
	owner, repo := splitRepo(repository.FullName)
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number)
	body, err := s.doGitHubRequestWithAccept(ctx, resolution.Token, http.MethodGet, path, nil, githubFullJSONMediaType)
	if err != nil {
		return CodeReviewTextDiscovery{}, err
	}
	var pr struct {
		Body string `json:"body"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := json.Unmarshal(body, &pr); err != nil {
		return CodeReviewTextDiscovery{}, err
	}
	if strings.TrimSpace(pr.Head.SHA) == "" {
		return CodeReviewTextDiscovery{}, fmt.Errorf("text evidence PR head is missing")
	}
	result := CodeReviewTextDiscovery{HeadSHA: pr.Head.SHA, Body: pr.Body, Complete: true}
	totalBytes := len(pr.Body)
	if totalBytes > codeReviewTextMaxBytes {
		return CodeReviewTextDiscovery{HeadSHA: pr.Head.SHA, Complete: false}, nil
	}
	visited := 0
	for _, surface := range []struct {
		path string
		kind models.CodeReviewEvidenceSurface
	}{
		{fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, number), models.CodeReviewEvidenceSurfaceIssueComment},
		{fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", owner, repo, number), models.CodeReviewEvidenceSurfaceReviewBody},
		{fmt.Sprintf("/repos/%s/%s/pulls/%d/comments", owner, repo, number), models.CodeReviewEvidenceSurfaceReviewComment},
	} {
		for page := 1; ; page++ {
			path := fmt.Sprintf("%s?per_page=%d&page=%d", surface.path, codeReviewTextPageSize, page)
			payload, requestErr := s.doGitHubRequestWithAccept(ctx, resolution.Token, http.MethodGet, path, nil, githubFullJSONMediaType)
			if requestErr != nil {
				return CodeReviewTextDiscovery{}, requestErr
			}
			var items []githubVisualEvidenceContent
			if err := json.Unmarshal(payload, &items); err != nil {
				return CodeReviewTextDiscovery{}, err
			}
			visited += len(items)
			if visited > codeReviewTextMaxDiscussionItems {
				return CodeReviewTextDiscovery{HeadSHA: pr.Head.SHA, Complete: false}, nil
			}
			for _, item := range items {
				if item.User == nil || !codeReviewEvidenceAuthorType(item.User.Type).IsHuman() || strings.TrimSpace(item.User.Login) == "" {
					continue
				}
				result.Sources = append(result.Sources, CodeReviewTextSource{Surface: surface.kind, ProviderObjectID: fmt.Sprint(item.ID), SourceURL: item.HTMLURL, AuthorLogin: strings.TrimSpace(item.User.Login), Body: item.Body})
				totalBytes += len(item.Body)
				if len(result.Sources) > codeReviewTextMaxDiscussionItems || totalBytes > codeReviewTextMaxBytes {
					return CodeReviewTextDiscovery{HeadSHA: pr.Head.SHA, Complete: false}, nil
				}
			}
			if len(items) < codeReviewTextPageSize {
				break
			}
		}
	}
	sort.Slice(result.Sources, func(i, j int) bool {
		a, b := result.Sources[i], result.Sources[j]
		if a.Surface != b.Surface {
			return a.Surface < b.Surface
		}
		return a.ProviderObjectID < b.ProviderObjectID
	})
	return result, nil
}
