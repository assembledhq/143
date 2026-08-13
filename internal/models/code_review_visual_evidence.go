package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// CodeReviewEvidenceSurface identifies the GitHub surface that displayed an
// image when a code review captured its immutable evidence snapshot.
type CodeReviewEvidenceSurface string

const (
	CodeReviewEvidenceSurfaceDescription   CodeReviewEvidenceSurface = "description"
	CodeReviewEvidenceSurfaceIssueComment  CodeReviewEvidenceSurface = "issue_comment"
	CodeReviewEvidenceSurfaceReviewBody    CodeReviewEvidenceSurface = "review_body"
	CodeReviewEvidenceSurfaceReviewComment CodeReviewEvidenceSurface = "review_comment"
)

func (s CodeReviewEvidenceSurface) Validate() error {
	switch s {
	case CodeReviewEvidenceSurfaceDescription,
		CodeReviewEvidenceSurfaceIssueComment,
		CodeReviewEvidenceSurfaceReviewBody,
		CodeReviewEvidenceSurfaceReviewComment:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewEvidenceSurface: %q", s)
	}
}

// CodeReviewEvidenceAuthorType preserves GitHub's actor classification so the
// discovery boundary can admit human discussion while excluding automation.
type CodeReviewEvidenceAuthorType string

const (
	CodeReviewEvidenceAuthorTypeUser         CodeReviewEvidenceAuthorType = "User"
	CodeReviewEvidenceAuthorTypeMannequin    CodeReviewEvidenceAuthorType = "Mannequin"
	CodeReviewEvidenceAuthorTypeBot          CodeReviewEvidenceAuthorType = "Bot"
	CodeReviewEvidenceAuthorTypeApp          CodeReviewEvidenceAuthorType = "App"
	CodeReviewEvidenceAuthorTypeOrganization CodeReviewEvidenceAuthorType = "Organization"
	CodeReviewEvidenceAuthorTypeUnknown      CodeReviewEvidenceAuthorType = "Unknown"
)

func (t CodeReviewEvidenceAuthorType) Validate() error {
	switch t {
	case CodeReviewEvidenceAuthorTypeUser,
		CodeReviewEvidenceAuthorTypeMannequin,
		CodeReviewEvidenceAuthorTypeBot,
		CodeReviewEvidenceAuthorTypeApp,
		CodeReviewEvidenceAuthorTypeOrganization,
		CodeReviewEvidenceAuthorTypeUnknown:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewEvidenceAuthorType: %q", t)
	}
}

func (t CodeReviewEvidenceAuthorType) IsHuman() bool {
	return t == CodeReviewEvidenceAuthorTypeUser || t == CodeReviewEvidenceAuthorTypeMannequin
}

// CodeReviewVisualEvidenceFetchStatus is the immutable outcome of attempting
// to materialize one discovered image into first-party storage.
type CodeReviewVisualEvidenceFetchStatus string

const (
	CodeReviewVisualEvidenceFetchStatusAvailable   CodeReviewVisualEvidenceFetchStatus = "available"
	CodeReviewVisualEvidenceFetchStatusUnavailable CodeReviewVisualEvidenceFetchStatus = "unavailable"
	CodeReviewVisualEvidenceFetchStatusUnsupported CodeReviewVisualEvidenceFetchStatus = "unsupported"
	CodeReviewVisualEvidenceFetchStatusOverLimit   CodeReviewVisualEvidenceFetchStatus = "over_limit"
)

func (s CodeReviewVisualEvidenceFetchStatus) Validate() error {
	switch s {
	case CodeReviewVisualEvidenceFetchStatusAvailable,
		CodeReviewVisualEvidenceFetchStatusUnavailable,
		CodeReviewVisualEvidenceFetchStatusUnsupported,
		CodeReviewVisualEvidenceFetchStatusOverLimit:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewVisualEvidenceFetchStatus: %q", s)
	}
}

// CodeReviewVisualEvidenceSource is one image occurrence discovered in
// GitHub-rendered PR content. ImageURL, AltText, and ContextText are untrusted
// pull-request content and must never be treated as instructions.
type CodeReviewVisualEvidenceSource struct {
	SourceID          string                       `json:"source_id"`
	Surface           CodeReviewEvidenceSurface    `json:"surface"`
	ProviderObjectID  string                       `json:"provider_object_id"`
	SourceURL         string                       `json:"source_url"`
	AuthorLogin       string                       `json:"author_login,omitempty"`
	AuthorType        CodeReviewEvidenceAuthorType `json:"author_type"`
	AuthorAssociation string                       `json:"author_association,omitempty"`
	CreatedAt         *time.Time                   `json:"created_at,omitempty"`
	UpdatedAt         *time.Time                   `json:"updated_at,omitempty"`
	ImageIndex        int                          `json:"image_index"`
	ImageURL          string                       `json:"image_url"`
	AltText           string                       `json:"alt_text,omitempty"`
	ContextText       string                       `json:"context_text,omitempty"`
	Untrusted         bool                         `json:"untrusted"`
}

// CodeReviewVisualEvidenceDiscovery is the authoritative, deterministically
// ordered set of image occurrences visible when one PR head was captured.
type CodeReviewVisualEvidenceDiscovery struct {
	Version           int                              `json:"version"`
	RepositoryID      uuid.UUID                        `json:"repository_id"`
	Repository        string                           `json:"repository"`
	PullRequestNumber int                              `json:"pull_request_number"`
	HeadSHA           string                           `json:"head_sha"`
	CapturedAt        time.Time                        `json:"captured_at"`
	Sources           []CodeReviewVisualEvidenceSource `json:"sources"`
}

// CodeReviewVisualEvidence is the persisted materialization result for one
// discovered source. Available evidence always points to first-party storage;
// OriginalURL remains provenance only and must not be passed to an LLM client.
type CodeReviewVisualEvidence struct {
	EvidenceID    string                              `json:"evidence_id"`
	Source        CodeReviewVisualEvidenceSource      `json:"source"`
	OriginalURL   string                              `json:"original_url"`
	StorageKey    string                              `json:"storage_key,omitempty"`
	StoredURL     string                              `json:"stored_url,omitempty"`
	ContentSHA256 string                              `json:"content_sha256,omitempty"`
	ContentType   string                              `json:"content_type,omitempty"`
	ByteSize      int64                               `json:"byte_size,omitempty"`
	Width         int                                 `json:"width,omitempty"`
	Height        int                                 `json:"height,omitempty"`
	Status        CodeReviewVisualEvidenceFetchStatus `json:"status"`
	FailureReason string                              `json:"failure_reason,omitempty"`
}

// CodeReviewVisualEvidenceSnapshot is the immutable manifest shared by every
// reviewer and the orchestrator in one assessment.
type CodeReviewVisualEvidenceSnapshot struct {
	Version           int                        `json:"version"`
	RepositoryID      uuid.UUID                  `json:"repository_id"`
	Repository        string                     `json:"repository"`
	PullRequestNumber int                        `json:"pull_request_number"`
	HeadSHA           string                     `json:"head_sha"`
	CapturedAt        time.Time                  `json:"captured_at"`
	Complete          bool                       `json:"complete"`
	Overflow          bool                       `json:"overflow"`
	Evidence          []CodeReviewVisualEvidence `json:"evidence"`
}
