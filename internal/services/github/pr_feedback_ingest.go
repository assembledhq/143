package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type FeedbackWebhookMetadata struct {
	DeliveryID string
	EventType  string
	Payload    json.RawMessage
	Headers    json.RawMessage
}

type FeedbackGitHubAppIdentity struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
}

type normalizedPRFeedback struct {
	Metadata          FeedbackWebhookMetadata
	OwnerOrgID        *uuid.UUID
	RepositoryID      int64
	Repository        string
	PullRequestNumber int
	Surface           models.PRFeedbackSurface
	ProviderObjectID  int64
	GitHubReviewID    *int64
	ThreadRootID      *int64
	InReplyToID       *int64
	AuthorLogin       string
	AuthorType        string
	AuthorAssociation string
	GitHubAppID       *int64
	GitHubAppSlug     *string
	Body              string
	Path              *string
	Line              *int
	Side              *string
	DiffHunk          *string
	CommitSHA         *string
}

func (s *PRService) ingestPRFeedback(ctx context.Context, input normalizedPRFeedback) error {
	if s.feedback == nil || input.OwnerOrgID == nil || input.Metadata.DeliveryID == "" {
		return nil
	}
	pr, err := s.getWebhookPullRequest(ctx, input.OwnerOrgID, input.Repository, input.PullRequestNumber)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if pr.Status != models.PullRequestStatusOpen || pr.SessionID == nil {
		return nil
	}
	repo, err := s.repos.GetByOrgAndGitHubIDAnyStatus(ctx, pr.OrgID, input.RepositoryID)
	if err != nil {
		return err
	}
	bodyHash := sha256.Sum256([]byte(input.Body))
	authorType := models.PRFeedbackAuthorType(input.AuthorType)
	if authorType.Validate() != nil {
		authorType = models.PRFeedbackAuthorTypeUnknown
	}
	deliveryID := input.Metadata.DeliveryID
	delivery := &models.WebhookDelivery{OrgID: pr.OrgID, IntegrationID: repo.IntegrationID, Provider: "github", DeliveryID: &deliveryID, EventType: input.Metadata.EventType, Payload: input.Metadata.Payload, Headers: input.Metadata.Headers, Status: "processed"}
	item := &models.PullRequestFeedbackItem{
		OrgID: pr.OrgID, PullRequestID: pr.ID, Surface: input.Surface,
		ProviderObjectID: input.ProviderObjectID, GitHubDeliveryID: &deliveryID,
		GitHubReviewID: input.GitHubReviewID, GitHubThreadRootCommentID: input.ThreadRootID,
		InReplyToCommentID: input.InReplyToID, AuthorLogin: strings.ToLower(input.AuthorLogin),
		GitHubAppID: input.GitHubAppID, GitHubAppSlug: input.GitHubAppSlug,
		AuthorType: authorType, AuthorAssociation: input.AuthorAssociation,
		Body: input.Body, BodyHash: hex.EncodeToString(bodyHash[:]), Path: nonEmptyStringPointer(input.Path),
		Line: input.Line, Side: nonEmptyStringPointer(input.Side), DiffHunk: nonEmptyStringPointer(input.DiffHunk),
		CommentCommitSHA: nonEmptyStringPointer(input.CommitSHA), Intent: models.PRFeedbackIntentUnknown,
		Status: models.PRFeedbackItemStatusPending,
	}
	if pr.HeadSHA != nil {
		item.ObservedHeadSHA = *pr.HeadSHA
	}
	_, err = s.feedback.Ingest(ctx, delivery, item)
	return err
}

func feedbackGitHubAppIdentity(app *FeedbackGitHubAppIdentity) (*int64, *string) {
	if app == nil || app.ID == 0 {
		return nil, nil
	}
	id := app.ID
	var slug *string
	if app.Slug != "" {
		value := app.Slug
		slug = &value
	}
	return &id, slug
}

func nonEmptyStringPointer(value *string) *string {
	if value == nil || *value == "" {
		return nil
	}
	return value
}
