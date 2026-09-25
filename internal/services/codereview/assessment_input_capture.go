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
	Health         *models.PullRequestHealthResponse
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
	DiscoverCodeReviewTextEvidence(context.Context, uuid.UUID, uuid.UUID, int) (ghservice.CodeReviewTextDiscovery, error)
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
	if len(org.Settings) > 0 && !json.Valid(org.Settings) || len(repo.Settings) > 0 && !json.Valid(repo.Settings) {
		return result, fmt.Errorf("%w: organization or repository settings contain invalid JSON", ErrAssessmentReuseUnavailable)
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
			if a.Provider == b.Provider {
				return a.Category < b.Category
			}
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
	textDiscovery, err := s.snapshots.DiscoverCodeReviewTextEvidence(ctx, in.OrgID, in.RepositoryID, pr.GitHubPRNumber)
	if err != nil {
		return result, err
	}
	if !textDiscovery.Complete {
		return result, fmt.Errorf("%w: text evidence exceeds the complete capture budget", ErrAssessmentReuseUnavailable)
	}
	if textDiscovery.HeadSHA != snapshot.HeadSHA || textDiscovery.Body != snapshot.Body {
		return result, errors.New("pull request changed during text evidence capture")
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
	textItems := make([]ReviewTextEvidence, 0, 1+len(textDiscovery.Sources))
	prURL := "https://github.com/" + repo.FullName + "/pull/" + fmt.Sprint(pr.GitHubPRNumber)
	textItems = append(textItems, newReviewTextEvidence("pull_request_description", fmt.Sprint(pr.GitHubPRNumber), prURL, snapshot.AuthorLogin, snapshot.Body, "full"))
	_, descriptionSections, descriptionErr := splitReviewEvidenceSections(snapshot.Body)
	parseAmbiguous := descriptionErr != nil
	for _, section := range descriptionSections {
		textItems = append(textItems, newReviewTextEvidence("pull_request_description", fmt.Sprint(pr.GitHubPRNumber), prURL, snapshot.AuthorLogin, section.Content, section.Label))
	}
	type discussionIntent struct {
		Surface, ProviderObjectID, Intent string
		Opaque                            bool
	}
	var unclassified []discussionIntent
	for _, source := range textDiscovery.Sources {
		if source.SourceURL == "" || source.ProviderObjectID == "" {
			return result, fmt.Errorf("%w: text source lacks provenance", ErrAssessmentReuseUnavailable)
		}
		// Capture the complete discussion as untrusted evidence, including text
		// outside recognized sections. Routing no longer treats that text as a
		// reason to replace the code review, so the recheck must be able to see it.
		textItems = append(textItems, newReviewTextEvidence(string(source.Surface), source.ProviderObjectID, source.SourceURL, source.AuthorLogin, source.Body, "full"))
		intent, sections, parseErr := splitReviewEvidenceSections(source.Body)
		if parseErr != nil {
			parseAmbiguous = true
			intent = source.Body
		}
		if strings.TrimSpace(intent) != "" {
			unclassified = append(unclassified, discussionIntent{string(source.Surface), source.ProviderObjectID, intent, parseErr != nil})
		}
		for _, section := range sections {
			textItems = append(textItems, newReviewTextEvidence(string(source.Surface), source.ProviderObjectID, source.SourceURL, source.AuthorLogin, section.Content, section.Label))
		}
	}
	// A check projection proves only the named check's reported status for this
	// head. Its details URL is provenance, not a fetched or verified test log.
	checksVerified := summary.ChecksConfirmed && summary.CheckSetComplete != nil && *summary.CheckSetComplete
	seenChecks := make(map[string]bool, len(summary.Checks))
	checkBytes := 0
	if len(summary.Checks) > 100 {
		return result, fmt.Errorf("%w: check inventory exceeds capture budget", ErrAssessmentReuseUnavailable)
	}
	for _, check := range summary.Checks {
		identity := check.Provider + ":" + check.Name + ":" + string(check.Category)
		checkBytes += len(check.Name) + len(check.Provider) + len(check.Summary) + len(check.DetailsURL)
		if strings.TrimSpace(check.Name) == "" || seenChecks[identity] || len(check.Summary) > 4096 || checkBytes > 64*1024 {
			return result, fmt.Errorf("%w: check status lacks unique bounded provenance", ErrAssessmentReuseUnavailable)
		}
		seenChecks[identity] = true
		sourceURL := check.DetailsURL
		if sourceURL == "" {
			sourceURL = prURL + "/checks"
		}
		content := fmt.Sprintf("Check: %s\nProvider: %s\nCategory: %s\nStatus: %s\nHead: %s\nSummary: %s\n", check.Name, check.Provider, check.Category, check.Status, snapshot.HeadSHA, check.Summary)
		textItems = append(textItems, newReviewTextEvidence("check_status", identity, sourceURL, check.Provider, content, "status"))
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
		}{config.AgentRoster, org.Settings, repo.Settings}), PromptContractVersion: "code-review-recheck-v2", PromptContentDigest: promptDigest, InstructionsDigest: digestJSON(struct {
			Head, Base, Review, Approval string
			Repository                   json.RawMessage
			ExternalContextDigest        string
		}{snapshot.HeadSHA, snapshot.BaseSHA, config.ReviewInstructions, config.AutomatedApprovalPolicy, repo.Settings, externalDigest}), ExternalInputsComplete: true},
		Title: snapshot.Title, Description: snapshot.Body, Visual: ReviewVisualInput{Images: images, CaptureComplete: visual.Complete && !visual.Overflow && visual.OmittedSourceCount == 0, SourceProvenanceComplete: visual.Complete}, TextEvidence: ReviewTextInput{Items: textItems, UnclassifiedDigest: digestJSON(unclassified), Complete: true, SourceProvenanceComplete: true, ParseAmbiguous: parseAmbiguous}, Request: ReviewRequestInput{SubstantiveText: text},
		Gates: ReviewGateInput{EligibilityDigest: digestJSON(struct {
			MergeGuard        string
			HasConflicts      bool
			Author            string
			ActiveAuthorTeams []string
			Fork, Draft       bool
			State             string
		}{reviewMergeGuard(summary.MergeState), summary.HasConflicts, snapshot.AuthorLogin, activeAuthorTeams, snapshot.FromFork, snapshot.IsDraft, snapshot.State}), ChecksDigest: digestJSON(struct {
			FailingTestCount int
			ChecksConfirmed  bool
			CheckSetComplete *bool
			Checks           []models.PullRequestCheckSummary
		}{summary.FailingTestCount, summary.ChecksConfirmed, summary.CheckSetComplete, summary.Checks}), DynamicDigest: digestJSON(struct {
			MergeState       models.PullRequestMergeState
			NeedsAgentAction bool
		}{summary.MergeState, summary.NeedsAgentAction}), ChecksVerified: checksVerified, Complete: true},
	}
	input.Gates.SnapshotDigest = digestJSON([]string{input.Gates.EligibilityDigest, input.Gates.ChecksDigest, input.Gates.DynamicDigest})
	manifest, err := BuildReviewInputManifest(input)
	if err != nil {
		return result, fmt.Errorf("capture assessment inputs: %w", err)
	}
	pr.Title = snapshot.Title
	pr.Body = &snapshot.Body
	pr.HeadSHA = &snapshot.HeadSHA
	pr.BaseSHA = &snapshot.BaseSHA
	pr.Status = models.PullRequestStatusOpen
	capturedHealth := &models.PullRequestHealthResponse{
		PullRequestID: pr.ID, PullRequestNumber: pr.GitHubPRNumber, Repository: pr.GitHubRepo,
		URL: pr.GitHubPRURL, Status: models.PullRequestStatusOpen, HeadSHA: health.HeadSHA, BaseSHA: health.BaseSHA,
		HealthVersion: health.Version, SyncStatus: models.PullRequestHealthSyncStatusSynced,
		MergeState: summary.MergeState, HasConflicts: summary.HasConflicts, FailingTestCount: summary.FailingTestCount,
		NeedsAgentAction: summary.NeedsAgentAction, Checks: append([]models.PullRequestCheckSummary(nil), summary.Checks...),
		ChecksConfirmed: summary.ChecksConfirmed,
	}
	return AssessmentInputCaptureResult{Manifest: manifest, VisualEvidence: visual, Health: capturedHealth, PullRequest: pr, Policy: *resolved.Policy, Files: files, Snapshot: snapshot}, nil
}

func reviewMergeGuard(state models.PullRequestMergeState) string {
	switch state {
	case models.PullRequestMergeStateBehind, models.PullRequestMergeStateConflicted,
		models.PullRequestMergeStateMergeabilityPending, models.PullRequestMergeStateUnknown:
		return string(state)
	default:
		return "reviewable"
	}
}

func newReviewTextEvidence(surface, providerObjectID, sourceURL, authorLogin, content, section string) ReviewTextEvidence {
	identity := surface + ":" + providerObjectID + ":" + section
	return ReviewTextEvidence{EvidenceID: "te_" + digestBytes(identity)[:24], Surface: surface, ProviderObjectID: providerObjectID, SourceURL: sourceURL, AuthorLogin: authorLogin, Section: section, Content: content, ContentDigest: digestBytes(content)}
}
