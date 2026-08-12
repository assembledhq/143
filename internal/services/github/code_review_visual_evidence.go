package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/sync/errgroup"

	"github.com/assembledhq/143/internal/models"
)

const (
	codeReviewVisualEvidenceDiscoveryVersion = 1
	githubFullJSONMediaType                  = "application/vnd.github.full+json"
	githubVisualEvidencePageSize             = 100
	maxVisualEvidenceContextRunes            = 500
)

type githubVisualEvidenceActor struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type githubVisualEvidenceContent struct {
	ID                int64                      `json:"id"`
	HTMLURL           string                     `json:"html_url"`
	BodyHTML          string                     `json:"body_html"`
	AuthorAssociation string                     `json:"author_association"`
	User              *githubVisualEvidenceActor `json:"user"`
	CreatedAt         *time.Time                 `json:"created_at"`
	UpdatedAt         *time.Time                 `json:"updated_at"`
	SubmittedAt       *time.Time                 `json:"submitted_at"`
}

type githubVisualEvidencePullRequest struct {
	Number    int                        `json:"number"`
	HTMLURL   string                     `json:"html_url"`
	BodyHTML  string                     `json:"body_html"`
	User      *githubVisualEvidenceActor `json:"user"`
	CreatedAt *time.Time                 `json:"created_at"`
	UpdatedAt *time.Time                 `json:"updated_at"`
	Head      struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

// DiscoverCodeReviewVisualEvidence reads every GitHub discussion surface that
// may contain review evidence. Description images are always captured;
// discussion images are admitted only from human User or Mannequin actors.
func (s *PRService) DiscoverCodeReviewVisualEvidence(ctx context.Context, orgID, repositoryID uuid.UUID, number int) (models.CodeReviewVisualEvidenceDiscovery, error) {
	if s == nil || s.repos == nil {
		return models.CodeReviewVisualEvidenceDiscovery{}, fmt.Errorf("repository store is unavailable")
	}
	if orgID == uuid.Nil || repositoryID == uuid.Nil || number <= 0 {
		return models.CodeReviewVisualEvidenceDiscovery{}, fmt.Errorf("org_id, repository_id, and positive pull request number are required")
	}

	repository, err := s.repos.GetByID(ctx, orgID, repositoryID)
	if err != nil {
		return models.CodeReviewVisualEvidenceDiscovery{}, fmt.Errorf("load repository for code review visual evidence: %w", err)
	}
	token, err := s.getInstallationTokenForRepo(ctx, orgID, &repository)
	if err != nil {
		return models.CodeReviewVisualEvidenceDiscovery{}, fmt.Errorf("load installation token for code review visual evidence: %w", err)
	}
	owner, repo := splitRepo(repository.FullName)

	var details githubVisualEvidencePullRequest
	var issueComments, reviews, reviewComments []githubVisualEvidenceContent
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		path := fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number)
		body, requestErr := s.doGitHubRequestWithAccept(groupCtx, token, http.MethodGet, path, nil, githubFullJSONMediaType)
		if requestErr != nil {
			return fmt.Errorf("load pull request visual evidence: %w", requestErr)
		}
		if decodeErr := json.Unmarshal(body, &details); decodeErr != nil {
			return fmt.Errorf("decode pull request visual evidence: %w", decodeErr)
		}
		return nil
	})
	group.Go(func() error {
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, number)
		items, requestErr := s.listCodeReviewVisualEvidenceContent(groupCtx, token, path)
		if requestErr != nil {
			return fmt.Errorf("list pull request issue comments for visual evidence: %w", requestErr)
		}
		issueComments = items
		return nil
	})
	group.Go(func() error {
		path := fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", owner, repo, number)
		items, requestErr := s.listCodeReviewVisualEvidenceContent(groupCtx, token, path)
		if requestErr != nil {
			return fmt.Errorf("list pull request reviews for visual evidence: %w", requestErr)
		}
		reviews = items
		return nil
	})
	group.Go(func() error {
		path := fmt.Sprintf("/repos/%s/%s/pulls/%d/comments", owner, repo, number)
		items, requestErr := s.listCodeReviewVisualEvidenceContent(groupCtx, token, path)
		if requestErr != nil {
			return fmt.Errorf("list pull request review comments for visual evidence: %w", requestErr)
		}
		reviewComments = items
		return nil
	})
	if err := group.Wait(); err != nil {
		return models.CodeReviewVisualEvidenceDiscovery{}, err
	}
	if strings.TrimSpace(details.Head.SHA) == "" {
		return models.CodeReviewVisualEvidenceDiscovery{}, fmt.Errorf("pull request head SHA is missing from visual evidence snapshot")
	}

	sources := make([]models.CodeReviewVisualEvidenceSource, 0)
	descriptionAuthor := models.CodeReviewEvidenceAuthorTypeUnknown
	descriptionLogin := ""
	if details.User != nil {
		descriptionAuthor = codeReviewEvidenceAuthorType(details.User.Type)
		descriptionLogin = strings.TrimSpace(details.User.Login)
	}
	sources = append(sources, visualEvidenceSourcesFromHTML(visualEvidenceSourceMetadata{
		Surface:          models.CodeReviewEvidenceSurfaceDescription,
		ProviderObjectID: strconv.Itoa(details.Number),
		SourceURL:        details.HTMLURL,
		AuthorLogin:      descriptionLogin,
		AuthorType:       descriptionAuthor,
		CreatedAt:        details.CreatedAt,
		UpdatedAt:        details.UpdatedAt,
	}, details.BodyHTML)...)
	sources = append(sources, visualEvidenceSourcesFromDiscussion(models.CodeReviewEvidenceSurfaceIssueComment, issueComments)...)
	sources = append(sources, visualEvidenceSourcesFromDiscussion(models.CodeReviewEvidenceSurfaceReviewBody, reviews)...)
	sources = append(sources, visualEvidenceSourcesFromDiscussion(models.CodeReviewEvidenceSurfaceReviewComment, reviewComments)...)

	return models.CodeReviewVisualEvidenceDiscovery{
		Version:           codeReviewVisualEvidenceDiscoveryVersion,
		RepositoryID:      repositoryID,
		Repository:        repository.FullName,
		PullRequestNumber: number,
		HeadSHA:           details.Head.SHA,
		CapturedAt:        time.Now().UTC(),
		Sources:           sources,
	}, nil
}

func (s *PRService) listCodeReviewVisualEvidenceContent(ctx context.Context, token, basePath string) ([]githubVisualEvidenceContent, error) {
	items := make([]githubVisualEvidenceContent, 0)
	for page := 1; ; page++ {
		separator := "?"
		if strings.Contains(basePath, "?") {
			separator = "&"
		}
		path := fmt.Sprintf("%s%sper_page=%d&page=%d", basePath, separator, githubVisualEvidencePageSize, page)
		body, err := s.doGitHubRequestWithAccept(ctx, token, http.MethodGet, path, nil, githubFullJSONMediaType)
		if err != nil {
			return nil, err
		}
		var pageItems []githubVisualEvidenceContent
		if err := json.Unmarshal(body, &pageItems); err != nil {
			return nil, fmt.Errorf("decode GitHub visual evidence page %d: %w", page, err)
		}
		items = append(items, pageItems...)
		if len(pageItems) < githubVisualEvidencePageSize {
			break
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		iTime := visualEvidenceContentTime(items[i])
		jTime := visualEvidenceContentTime(items[j])
		if !iTime.Equal(jTime) {
			return iTime.Before(jTime)
		}
		return items[i].ID < items[j].ID
	})
	return items, nil
}

func visualEvidenceContentTime(content githubVisualEvidenceContent) time.Time {
	if content.CreatedAt != nil {
		return *content.CreatedAt
	}
	if content.SubmittedAt != nil {
		return *content.SubmittedAt
	}
	return time.Time{}
}

func visualEvidenceSourcesFromDiscussion(surface models.CodeReviewEvidenceSurface, content []githubVisualEvidenceContent) []models.CodeReviewVisualEvidenceSource {
	sources := make([]models.CodeReviewVisualEvidenceSource, 0)
	for _, item := range content {
		if item.User == nil {
			continue
		}
		authorType := codeReviewEvidenceAuthorType(item.User.Type)
		if !authorType.IsHuman() || strings.TrimSpace(item.User.Login) == "" {
			continue
		}
		createdAt := item.CreatedAt
		if createdAt == nil {
			createdAt = item.SubmittedAt
		}
		sources = append(sources, visualEvidenceSourcesFromHTML(visualEvidenceSourceMetadata{
			Surface:           surface,
			ProviderObjectID:  strconv.FormatInt(item.ID, 10),
			SourceURL:         item.HTMLURL,
			AuthorLogin:       strings.TrimSpace(item.User.Login),
			AuthorType:        authorType,
			AuthorAssociation: strings.TrimSpace(item.AuthorAssociation),
			CreatedAt:         createdAt,
			UpdatedAt:         item.UpdatedAt,
		}, item.BodyHTML)...)
	}
	return sources
}

type visualEvidenceSourceMetadata struct {
	Surface           models.CodeReviewEvidenceSurface
	ProviderObjectID  string
	SourceURL         string
	AuthorLogin       string
	AuthorType        models.CodeReviewEvidenceAuthorType
	AuthorAssociation string
	CreatedAt         *time.Time
	UpdatedAt         *time.Time
}

func visualEvidenceSourcesFromHTML(metadata visualEvidenceSourceMetadata, renderedHTML string) []models.CodeReviewVisualEvidenceSource {
	fragment, err := html.ParseFragment(strings.NewReader(renderedHTML), &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return nil
	}
	sources := make([]models.CodeReviewVisualEvidenceSource, 0)
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "img" {
			if imageURL := renderedImageURL(node); imageURL != "" {
				imageIndex := len(sources) + 1
				sources = append(sources, models.CodeReviewVisualEvidenceSource{
					SourceID:          codeReviewVisualEvidenceSourceID(metadata.Surface, metadata.ProviderObjectID, imageIndex, imageURL),
					Surface:           metadata.Surface,
					ProviderObjectID:  metadata.ProviderObjectID,
					SourceURL:         strings.TrimSpace(metadata.SourceURL),
					AuthorLogin:       metadata.AuthorLogin,
					AuthorType:        metadata.AuthorType,
					AuthorAssociation: metadata.AuthorAssociation,
					CreatedAt:         metadata.CreatedAt,
					UpdatedAt:         metadata.UpdatedAt,
					ImageIndex:        imageIndex,
					ImageURL:          imageURL,
					AltText:           boundedVisualEvidenceText(htmlAttribute(node, "alt")),
					ContextText:       visualEvidenceContext(node),
					Untrusted:         true,
				})
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	for _, node := range fragment {
		walk(node)
	}
	return sources
}

func renderedImageURL(node *html.Node) string {
	candidates := make([]string, 0, 3)
	if srcset := htmlAttribute(node, "srcset"); srcset != "" {
		candidates = append(candidates, lastSrcsetURL(srcset))
	}
	if node.Parent != nil && node.Parent.Type == html.ElementNode && node.Parent.Data == "picture" {
		for sibling := node.Parent.FirstChild; sibling != nil; sibling = sibling.NextSibling {
			if sibling.Type == html.ElementNode && sibling.Data == "source" {
				candidates = append(candidates, lastSrcsetURL(htmlAttribute(sibling, "srcset")))
			}
		}
	}
	candidates = append(candidates, htmlAttribute(node, "src"))
	for _, candidate := range candidates {
		if normalized := normalizeRenderedImageURL(candidate); normalized != "" {
			return normalized
		}
	}
	return ""
}

func lastSrcsetURL(srcset string) string {
	parts := strings.Split(srcset, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		fields := strings.Fields(strings.TrimSpace(parts[i]))
		if len(fields) > 0 {
			return fields[0]
		}
	}
	return ""
}

func normalizeRenderedImageURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if parsed.IsAbs() {
		return parsed.String()
	}
	base, err := url.Parse("https://github.com")
	if err != nil {
		return raw
	}
	return base.ResolveReference(parsed).String()
}

func visualEvidenceContext(image *html.Node) string {
	contextNode := image.Parent
	for node := image.Parent; node != nil; node = node.Parent {
		if node.Type == html.ElementNode {
			switch node.Data {
			case "p", "figure", "figcaption", "li", "blockquote":
				contextNode = node
				node = nil
			}
		}
		if node == nil {
			break
		}
	}
	if contextNode == nil {
		return ""
	}
	var textBuilder strings.Builder
	var walkText func(*html.Node)
	walkText = func(node *html.Node) {
		if node.Type == html.TextNode {
			textBuilder.WriteString(node.Data)
			textBuilder.WriteByte(' ')
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walkText(child)
		}
	}
	walkText(contextNode)
	return boundedVisualEvidenceText(textBuilder.String())
}

func boundedVisualEvidenceText(value string) string {
	contextText := strings.Join(strings.Fields(value), " ")
	if utf8.RuneCountInString(contextText) <= maxVisualEvidenceContextRunes {
		return contextText
	}
	runes := []rune(contextText)
	return string(runes[:maxVisualEvidenceContextRunes])
}

func htmlAttribute(node *html.Node, key string) string {
	for _, attribute := range node.Attr {
		if strings.EqualFold(attribute.Key, key) {
			return attribute.Val
		}
	}
	return ""
}

func codeReviewEvidenceAuthorType(raw string) models.CodeReviewEvidenceAuthorType {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "user":
		return models.CodeReviewEvidenceAuthorTypeUser
	case "mannequin":
		return models.CodeReviewEvidenceAuthorTypeMannequin
	case "bot":
		return models.CodeReviewEvidenceAuthorTypeBot
	case "app":
		return models.CodeReviewEvidenceAuthorTypeApp
	case "organization":
		return models.CodeReviewEvidenceAuthorTypeOrganization
	default:
		return models.CodeReviewEvidenceAuthorTypeUnknown
	}
}

func codeReviewVisualEvidenceSourceID(surface models.CodeReviewEvidenceSurface, providerObjectID string, imageIndex int, imageURL string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%s", surface, providerObjectID, imageIndex, imageURL)))
	return "ves_" + hex.EncodeToString(digest[:12])
}
