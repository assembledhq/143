package models

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const CodeReviewExportSchemaVersion = 1

type ReviewEvidenceAvailability string

const (
	ReviewEvidenceRecorded      ReviewEvidenceAvailability = "recorded"
	ReviewEvidenceReconstructed ReviewEvidenceAvailability = "reconstructed"
	ReviewEvidenceUnavailable   ReviewEvidenceAvailability = "unavailable"
)

func (v ReviewEvidenceAvailability) Validate() error {
	switch v {
	case ReviewEvidenceRecorded, ReviewEvidenceReconstructed, ReviewEvidenceUnavailable:
		return nil
	}
	return fmt.Errorf("invalid evidence availability %q", v)
}

type ReviewSafeguardResult string

const (
	ReviewSafeguardPassed       ReviewSafeguardResult = "passed"
	ReviewSafeguardFailed       ReviewSafeguardResult = "failed"
	ReviewSafeguardNotEvaluated ReviewSafeguardResult = "not_evaluated"
	ReviewSafeguardUnknown      ReviewSafeguardResult = "unknown"
)

func (v ReviewSafeguardResult) Validate() error {
	switch v {
	case ReviewSafeguardPassed, ReviewSafeguardFailed, ReviewSafeguardNotEvaluated, ReviewSafeguardUnknown:
		return nil
	}
	return fmt.Errorf("invalid safeguard result %q", v)
}

type ReviewExportTimeBasis string

const (
	ReviewExportCreated            ReviewExportTimeBasis = "created"
	ReviewExportCompleted          ReviewExportTimeBasis = "completed"
	ReviewExportCreatedOrCompleted ReviewExportTimeBasis = "created_or_completed"
	ReviewExportPublished          ReviewExportTimeBasis = "published"
)

func (v ReviewExportTimeBasis) Validate() error {
	switch v {
	case ReviewExportCreated, ReviewExportCompleted, ReviewExportCreatedOrCompleted, ReviewExportPublished:
		return nil
	}
	return fmt.Errorf("invalid export time basis %q", v)
}

type ReviewExportSourceKind string

const (
	ReviewExportAssessment ReviewExportSourceKind = "assessment"
	ReviewExportLegacy     ReviewExportSourceKind = "legacy"
)

func (v ReviewExportSourceKind) Validate() error {
	if v == ReviewExportAssessment || v == ReviewExportLegacy {
		return nil
	}
	return fmt.Errorf("invalid export source kind %q", v)
}

type CodeReviewExportFilters struct {
	RepositoryID uuid.UUID             `json:"repository_id"`
	From         time.Time             `json:"from"`
	Until        time.Time             `json:"until"`
	TimeBasis    ReviewExportTimeBasis `json:"time_basis"`
	// Published-time acquisitions always retain unresolved timing.
	IncludeUnresolvedPublicationTime bool `json:"include_unresolved_publication_time"`
}

func (f CodeReviewExportFilters) Validate() error {
	if f.RepositoryID == uuid.Nil || f.From.IsZero() || !f.Until.After(f.From) || f.Until.Sub(f.From) > 366*24*time.Hour {
		return fmt.Errorf("repository_id and a nonempty interval of at most 366 days are required")
	}
	if !f.IncludeUnresolvedPublicationTime {
		return fmt.Errorf("unresolved publication timing must be retained")
	}
	return f.TimeBasis.Validate()
}

type ReviewExportEvidence struct {
	Value        any                        `json:"value"`
	Availability ReviewEvidenceAvailability `json:"availability"`
	Provenance   string                     `json:"provenance"`
}
type ReviewExportSafeguard struct {
	ID            string                `json:"id"`
	Result        ReviewSafeguardResult `json:"result"`
	Inputs        ReviewExportEvidence  `json:"inputs"`
	Threshold     ReviewExportEvidence  `json:"threshold"`
	Applicability ReviewExportEvidence  `json:"applicability"`
	Provenance    string                `json:"provenance"`
}

// Raw evidence is frozen in the acquisition; it never joins live state on paging.
type CodeReviewExportSource struct {
	Kind                      ReviewExportSourceKind `json:"kind"`
	ID                        uuid.UUID              `json:"id"`
	Source                    json.RawMessage        `json:"source"`
	Policy                    json.RawMessage        `json:"policy"`
	PullRequest               json.RawMessage        `json:"pull_request"`
	AgentResults              json.RawMessage        `json:"agent_results"`
	LegacyAliases             json.RawMessage        `json:"legacy_aliases"`
	ProviderObservations      json.RawMessage        `json:"provider_observations"`
	PublicationTime           *time.Time             `json:"publication_time"`
	PublicationTimeProvenance string                 `json:"publication_time_provenance"`
}
type CodeReviewExportRecord struct {
	SourceIdentity string                          `json:"source_identity"`
	Raw            CodeReviewExportSource          `json:"raw"`
	Evidence       map[string]ReviewExportEvidence `json:"evidence"`
	Safeguards     []ReviewExportSafeguard         `json:"safeguards"`
}
type CodeReviewExportCounts struct {
	Records                       int64 `json:"records"`
	Assessments                   int64 `json:"assessments"`
	Legacy                        int64 `json:"legacy"`
	RepresentedLegacy             int64 `json:"represented_legacy"`
	UnresolvedPublicationTime     int64 `json:"unresolved_publication_time"`
	IncompleteSubstantiveEvidence int64 `json:"incomplete_substantive_evidence"`
	MissingHeadSHA                int64 `json:"missing_head_sha"`
	MissingBaseSHA                int64 `json:"missing_base_sha"`
	MissingBaseRef                int64 `json:"missing_base_ref"`
	MissingPolicySnapshot         int64 `json:"missing_policy_snapshot"`
}
type ReviewExportConsistency string

const ReviewExportFrozenPayloads ReviewExportConsistency = "frozen_payloads_repeatable_read"

func (v ReviewExportConsistency) Validate() error {
	if v == ReviewExportFrozenPayloads {
		return nil
	}
	return fmt.Errorf("invalid export consistency %q", v)
}

type CodeReviewExportAcquisition struct {
	ID            uuid.UUID               `json:"id"`
	SchemaVersion int                     `json:"schema_version"`
	Filters       CodeReviewExportFilters `json:"filters"`
	AcquiredAt    time.Time               `json:"acquired_at"`
	ExpiresAt     time.Time               `json:"expires_at"`
	Counts        CodeReviewExportCounts  `json:"counts"`
	Consistency   ReviewExportConsistency `json:"consistency"`
}
type CodeReviewExportCursor struct {
	AcquisitionID uuid.UUID `json:"acquisition_id"`
	Position      int64     `json:"position"`
}
type CodeReviewExportPage struct {
	Data        []CodeReviewExportRecord    `json:"data"`
	Acquisition CodeReviewExportAcquisition `json:"acquisition"`
	Meta        CodeReviewExportPageMeta    `json:"meta"`
}
type CodeReviewExportPageMeta struct {
	NextCursor *string `json:"next_cursor"`
	Returned   int     `json:"returned"`
	Position   int64   `json:"position"`
	Complete   bool    `json:"complete"`
}

// ResolveCodeReviewExportPublicationTime binds a retained provider submission to
// this output, rather than mistaking an old provider object's creation time for
// a later assessment's publication. This only annotates evidence; it does not
// establish review eligibility, approvals, or any replay verdict.
func ResolveCodeReviewExportPublicationTime(raw CodeReviewExportSource, boundary time.Time) CodeReviewExportSource {
	out := raw
	out.PublicationTime = nil
	out.PublicationTimeProvenance = "no unambiguous retained provider submission bound to this source's review ID, head, output marker, and lifecycle interval"
	var src struct {
		HeadSHA            string    `json:"head_sha"`
		SubmittedCommitSHA string    `json:"submitted_commit_sha"`
		PublicationKey     string    `json:"publication_key"`
		ReviewOutputKey    string    `json:"review_output_key"`
		ReviewID           *int64    `json:"github_review_id"`
		CreatedAt          time.Time `json:"created_at"`
	}
	if json.Unmarshal(raw.Source, &src) != nil || src.ReviewID == nil || *src.ReviewID <= 0 || src.HeadSHA == "" || src.CreatedAt.IsZero() || (!boundary.IsZero() && src.CreatedAt.After(boundary)) {
		return out
	}
	if src.SubmittedCommitSHA != "" && src.SubmittedCommitSHA != src.HeadSHA {
		return out
	}
	key := src.PublicationKey
	if raw.Kind == ReviewExportLegacy {
		key = src.ReviewOutputKey
	}
	if strings.TrimSpace(key) == "" {
		return out
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(key)))
	marker := fmt.Sprintf("<!-- 143-code-review-output:%x -->", digest[:])
	var observations []struct {
		ReviewID    int64  `json:"review_id"`
		CommitSHA   string `json:"commit_sha"`
		Body        string `json:"body"`
		SubmittedAt string `json:"submitted_at"`
	}
	if json.Unmarshal(raw.ProviderObservations, &observations) != nil {
		return out
	}
	var submitted *time.Time
	for _, event := range observations {
		if event.ReviewID != *src.ReviewID || event.CommitSHA != src.HeadSHA || !strings.Contains(event.Body, marker) {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, event.SubmittedAt)
		if err != nil || at.Before(src.CreatedAt) || (!boundary.IsZero() && at.After(boundary)) {
			continue
		}
		at = at.UTC()
		if submitted != nil && !submitted.Equal(at) {
			return out
		}
		submitted = &at
	}
	if submitted != nil {
		out.PublicationTime = submitted
		out.PublicationTimeProvenance = "retained GitHub submitted-review webhook matched by tenant, immutable repository provider ID, PR number, review ID, commit SHA, exact publication marker, and source creation/acquisition interval; conflicting times remain unresolved"
	}
	return out
}
