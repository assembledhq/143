package codereview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	"github.com/assembledhq/143/internal/services/agent"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
)

var ErrAssessmentReuseUnavailable = errors.New("assessment inputs cannot establish reusable coverage")

type AssessmentInputCaptureRequest struct {
	Fresh                                                       bool
	OrgID, RepositoryID, PullRequestID, SessionID, AssessmentID uuid.UUID
	RequestContext                                              *ReviewRequestContext
}

type AssessmentInputCaptureResult struct {
	Manifest       ReviewInputManifest
	VisualEvidence models.CodeReviewVisualEvidenceSnapshot
	PullRequest    models.PullRequest
	Policy         models.CodeReviewPolicyRecord
	Files          []PullRequestFile
	Snapshot       ghservice.CodeReviewPullRequestSnapshot
}

type AssessmentInputCapturer interface {
	CaptureAssessmentInputs(context.Context, AssessmentInputCaptureRequest) (AssessmentInputCaptureResult, error)
}

type assessmentRepositoryStore interface {
	GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Repository, error)
}
type assessmentOrganizationStore interface {
	GetByID(context.Context, uuid.UUID) (models.Organization, error)
}
type assessmentVisualCapturer interface {
	Capture(context.Context, CaptureVisualEvidenceInput) (models.CodeReviewVisualEvidenceSnapshot, error)
}
type assessmentFileLister interface {
	ListPullRequestFiles(context.Context, PullRequestFilesRequest) ([]PullRequestFile, error)
}
type assessmentSnapshotter interface {
	scheduleSnapshotter
	PullRequestSyncer
}

type assessmentExternalContextResolver interface {
	ResolveCodeReviewExternalContext(context.Context, uuid.UUID) (string, string, error)
}

type assessmentAuthorTeamMembershipChecker interface {
	IsActiveTeamMember(context.Context, int64, string, string, string) (bool, error)
}

// AssessmentInputCaptureService captures inputs through the same provider
// boundaries as full review. Call outside a database transaction: discovery
// and image downloads are bounded network operations, not row-lock work.
type AssessmentInputCaptureService struct {
	policies    PolicyStore
	prs         PullRequestStore
	repos       assessmentRepositoryStore
	orgs        assessmentOrganizationStore
	snapshots   assessmentSnapshotter
	visual      assessmentVisualCapturer
	files       assessmentFileLister
	external    assessmentExternalContextResolver
	authorTeams assessmentAuthorTeamMembershipChecker
}

func NewAssessmentInputCaptureService(policies PolicyStore, prs PullRequestStore, repos assessmentRepositoryStore, orgs assessmentOrganizationStore, snapshots assessmentSnapshotter, visual assessmentVisualCapturer, files assessmentFileLister) *AssessmentInputCaptureService {
	return &AssessmentInputCaptureService{policies: policies, prs: prs, repos: repos, orgs: orgs, snapshots: snapshots, visual: visual, files: files}
}

func (s *AssessmentInputCaptureService) SetExternalContextResolver(resolver assessmentExternalContextResolver) {
	s.external = resolver
}

func (s *AssessmentInputCaptureService) SetAuthorTeamMembershipChecker(checker assessmentAuthorTeamMembershipChecker) {
	s.authorTeams = checker
}

func (s *AssessmentInputCaptureService) CaptureAssessmentInputs(ctx context.Context, in AssessmentInputCaptureRequest) (AssessmentInputCaptureResult, error) {
	var result AssessmentInputCaptureResult
	if s == nil || s.policies == nil || s.prs == nil || s.repos == nil || s.orgs == nil || s.snapshots == nil || s.visual == nil || s.files == nil || s.external == nil || in.OrgID == uuid.Nil || in.RepositoryID == uuid.Nil || in.PullRequestID == uuid.Nil || in.SessionID == uuid.Nil || in.AssessmentID == uuid.Nil {
		return result, errors.New("assessment capture dependencies and identity are required")
	}
	_, externalDigest, err := s.external.ResolveCodeReviewExternalContext(ctx, in.OrgID)
	if err != nil {
		if errors.Is(err, agent.ErrCodeReviewExternalContextUnfingerprinted) {
			return result, fmt.Errorf("%w: %v", ErrAssessmentReuseUnavailable, err)
		}
		return result, fmt.Errorf("resolve code review external prompt context: %w", err)
	}
	repo, err := s.repos.GetByID(ctx, in.OrgID, in.RepositoryID)
	if err != nil {
		return result, err
	}
	pr, err := s.prs.GetByID(ctx, in.OrgID, in.PullRequestID)
	if err != nil {
		return result, err
	}
	if pr.GitHubRepo != repo.FullName || repo.OrgID != in.OrgID {
		return result, errors.New("assessment repository does not match pull request")
	}
	resolved, err := s.policies.ResolvePolicy(ctx, in.OrgID)
	if err != nil {
		return result, err
	}
	if resolved.Policy == nil || !resolved.Config.Enabled {
		return result, ErrReviewIneligible
	}
	org, err := s.orgs.GetByID(ctx, in.OrgID)
	if err != nil {
		return result, err
	}
	reader, err := s.snapshots.PrepareCodeReviewPullRequestSnapshot(ctx, in.OrgID, in.RepositoryID)
	if err != nil {
		return result, err
	}
	snapshot, err := reader(ctx, pr.GitHubPRNumber)
	if err != nil {
		return result, err
	}
	if snapshot.State != "open" {
		return result, ErrReviewIneligible
	}
	if err = s.snapshots.SyncPullRequestState(ctx, in.OrgID, in.PullRequestID); err != nil {
		return result, err
	}
	health, err := s.prs.GetHealthCurrent(ctx, in.OrgID, in.PullRequestID)
	if err != nil {
		return result, err
	}
	if health.HeadSHA != snapshot.HeadSHA || health.BaseSHA != snapshot.BaseSHA {
		return result, errors.New("assessment health does not cover current revision")
	}
	var summary models.PullRequestHealthSummary
	if err = json.Unmarshal(health.SummaryJSON, &summary); err != nil {
		return result, err
	}
	sort.Slice(summary.Checks, func(i, j int) bool {
		a, b := summary.Checks[i], summary.Checks[j]
		if a.Name == b.Name {
			return a.Provider < b.Provider
		}
		return a.Name < b.Name
	})
	files, err := s.files.ListPullRequestFiles(ctx, PullRequestFilesRequest{InstallationID: repo.InstallationID, Repository: repo.FullName, PullNumber: pr.GitHubPRNumber})
	if err != nil {
		return result, err
	}
	visual, err := s.visual.Capture(ctx, CaptureVisualEvidenceInput{Fresh: in.Fresh, OrgID: in.OrgID, RepositoryID: in.RepositoryID, SessionID: in.SessionID, AssessmentID: &in.AssessmentID, PullRequestNumber: pr.GitHubPRNumber, HeadSHA: snapshot.HeadSHA})
	if err != nil {
		return result, err
	}
	// Bracket the multi-request capture. A change during capture remains pending
	// instead of creating an input manifest made from two different revisions.
	after, err := reader(ctx, pr.GitHubPRNumber)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(snapshot, after) {
		return result, errors.New("pull request changed during assessment capture")
	}
	if visual.Overflow || visual.OmittedSourceCount > 0 {
		return result, fmt.Errorf("%w: visual evidence exceeds the complete capture budget", ErrAssessmentReuseUnavailable)
	}
	promptDigest, err := prompts.CodeReviewContractDigest()
	if err != nil {
		return result, err
	}
	codeFiles := make([]ReviewChangedFile, 0, len(files))
	for _, file := range files {
		codeFiles = append(codeFiles, ReviewChangedFile{Path: file.Filename, Status: file.Status, PatchDigest: digestJSON(file)})
	}
	images := make([]ReviewVisualImage, 0, len(visual.Evidence))
	for _, e := range visual.Evidence {
		// Fetch failures remain part of the content identity and can never be
		// cited as available evidence by the response validator.
		content := e.ContentSHA256
		if e.Status != models.CodeReviewVisualEvidenceFetchStatusAvailable {
			content = digestJSON(struct {
				Status models.CodeReviewVisualEvidenceFetchStatus
				Reason string
			}{e.Status, e.FailureReason})
		}
		images = append(images, ReviewVisualImage{SourceID: e.Source.SourceID, SourceURL: e.Source.SourceURL, SourceText: e.Source.ContextText, AltText: e.Source.AltText, ContentDigest: content})
	}
	text := ""
	if in.RequestContext != nil {
		text = in.RequestContext.Body
	}
	config := resolved.Config
	activeAuthorTeams := make([]string, 0, len(config.RiskPolicy.EligibleAuthorTeams))
	owner, _, hasOwner := strings.Cut(strings.TrimSpace(repo.FullName), "/")
	for _, teamRef := range config.RiskPolicy.EligibleAuthorTeams {
		organization, teamSlug, qualified := strings.Cut(strings.TrimSpace(teamRef), "/")
		if !qualified || !hasOwner || !strings.EqualFold(strings.TrimSpace(organization), owner) || strings.TrimSpace(teamSlug) == "" {
			continue
		}
		if s.authorTeams == nil || repo.InstallationID <= 0 || strings.TrimSpace(snapshot.AuthorLogin) == "" {
			return result, fmt.Errorf("%w: author team eligibility cannot be fingerprinted", ErrAssessmentReuseUnavailable)
		}
		active, checkErr := s.authorTeams.IsActiveTeamMember(ctx, repo.InstallationID, organization, teamSlug, snapshot.AuthorLogin)
		if checkErr != nil {
			return result, fmt.Errorf("check author team eligibility: %w", checkErr)
		}
		if active {
			activeAuthorTeams = append(activeAuthorTeams, strings.TrimSpace(teamRef))
		}
	}
	sort.Strings(activeAuthorTeams)
	input := ReviewInputCapture{
		Code: ReviewCodeInput{OrgID: in.OrgID, RepositoryID: in.RepositoryID, PullRequestID: in.PullRequestID, HeadSHA: snapshot.HeadSHA, BaseSHA: snapshot.BaseSHA, BaseRef: snapshot.BaseRef, Files: codeFiles, FilesComplete: true},
		Contract: ReviewContractInput{PolicyID: resolved.Policy.ID, PolicyVersion: int64(resolved.Policy.Version), PolicyDigest: digestJSON(config), RosterDigest: digestJSON(config.AgentRoster), ModelConfigurationDigest: digestJSON(struct {
			Roster     models.CodeReviewAgentRoster
			Org        json.RawMessage
			Repository json.RawMessage
		}{config.AgentRoster, org.Settings, repo.Settings}), PromptContractVersion: "code-review-recheck-v1", PromptContentDigest: promptDigest, InstructionsDigest: digestJSON(struct {
			Head, Base, Review, Approval string
			Repository                   json.RawMessage
			ExternalContextDigest        string
		}{snapshot.HeadSHA, snapshot.BaseSHA, config.ReviewInstructions, config.AutomatedApprovalPolicy, repo.Settings, externalDigest}), ExternalInputsComplete: true},
		Title: snapshot.Title, Description: snapshot.Body, Visual: ReviewVisualInput{Images: images, CaptureComplete: visual.Complete && !visual.Overflow && visual.OmittedSourceCount == 0, SourceProvenanceComplete: visual.Complete}, Request: ReviewRequestInput{SubstantiveText: text},
		Gates: ReviewGateInput{SnapshotDigest: digestJSON(struct {
			Summary           models.PullRequestHealthSummary
			Author            string
			ActiveAuthorTeams []string
			Fork, Draft       bool
			State             string
		}{summary, snapshot.AuthorLogin, activeAuthorTeams, snapshot.FromFork, snapshot.IsDraft, snapshot.State}), Complete: true},
	}
	manifest, err := BuildReviewInputManifest(input)
	if err != nil {
		return result, fmt.Errorf("capture assessment inputs: %w", err)
	}
	pr.Title = snapshot.Title
	pr.Body = &snapshot.Body
	pr.HeadSHA = &snapshot.HeadSHA
	pr.BaseSHA = &snapshot.BaseSHA
	return AssessmentInputCaptureResult{Manifest: manifest, VisualEvidence: visual, PullRequest: pr, Policy: *resolved.Policy, Files: files, Snapshot: snapshot}, nil
}
