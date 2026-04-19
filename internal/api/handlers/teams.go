package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
)

// orgTeamStore is the interface the org teams handler depends on.
type orgTeamStore interface {
	Create(ctx context.Context, team *models.Team) error
	Update(ctx context.Context, orgID, teamID uuid.UUID, name, slug string, description *string) error
	Delete(ctx context.Context, orgID, teamID uuid.UUID) error
	GetByID(ctx context.Context, orgID, teamID uuid.UUID) (models.Team, error)
	ListByOrg(ctx context.Context, orgID uuid.UUID) ([]models.Team, error)
	ListByUser(ctx context.Context, orgID, userID uuid.UUID) ([]models.Team, error)
	AddMember(ctx context.Context, orgID, teamID, userID uuid.UUID, role string) error
	RemoveMember(ctx context.Context, orgID, teamID, userID uuid.UUID) error
	ListMembers(ctx context.Context, orgID, teamID uuid.UUID) ([]models.User, error)
	SyncFromGitHub(ctx context.Context, orgID uuid.UUID, ghTeams []db.GitHubTeamSync) error
}

// orgTeamUserStore verifies a user belongs to the org.
type orgTeamUserStore interface {
	GetByID(ctx context.Context, orgID, userID uuid.UUID) (models.User, error)
}

// orgTeamIntegrationStore is needed to look up the GitHub integration for sync.
type orgTeamIntegrationStore interface {
	ListByOrgAndProvider(ctx context.Context, orgID uuid.UUID, provider string) ([]models.Integration, error)
}

// orgTeamRepoStore is needed to derive the GitHub org name from repos.
type orgTeamRepoStore interface {
	ListByOrg(ctx context.Context, orgID uuid.UUID) ([]models.Repository, error)
}

// OrgTeamHandler serves the /api/v1/teams/* endpoints.
type OrgTeamHandler struct {
	teams        orgTeamStore
	users        orgTeamUserStore
	integrations orgTeamIntegrationStore
	repos        orgTeamRepoStore
	githubSvc    *ghservice.Service
	audit        *db.AuditEmitter
}

// NewOrgTeamHandler creates a new org teams handler.
func NewOrgTeamHandler(teams orgTeamStore, users orgTeamUserStore) *OrgTeamHandler {
	return &OrgTeamHandler{teams: teams, users: users}
}

// SetAuditEmitter injects the audit emitter.
func (h *OrgTeamHandler) SetAuditEmitter(audit *db.AuditEmitter) { h.audit = audit }

// SetGitHubSync injects the dependencies needed for GitHub team sync.
func (h *OrgTeamHandler) SetGitHubSync(ghSvc *ghservice.Service, integrations orgTeamIntegrationStore, repos orgTeamRepoStore) {
	h.githubSvc = ghSvc
	h.integrations = integrations
	h.repos = repos
}

var slugRE = regexp.MustCompile(`[^a-z0-9-]+`)

func toSlug(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = slugRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "team"
	}
	return s
}

type teamBody struct {
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	Description *string `json:"description"`
}

// decodeTeamBody decodes and validates a team create/update request body.
// Returns false (and writes an error response) when validation fails.
func decodeTeamBody(w http.ResponseWriter, r *http.Request) (teamBody, bool) {
	var body teamBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return body, false
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "name is required")
		return body, false
	}
	if body.Slug == "" {
		body.Slug = toSlug(body.Name)
	} else {
		body.Slug = toSlug(body.Slug)
	}
	return body, true
}

// List returns all teams in the org.
func (h *OrgTeamHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	teams, err := h.teams.ListByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list teams", err)
		return
	}
	if teams == nil {
		teams = []models.Team{}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.Team]{Data: teams})
}

// ListMine returns teams the current user belongs to.
func (h *OrgTeamHandler) ListMine(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())

	teams, err := h.teams.ListByUser(r.Context(), orgID, user.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list user teams", err)
		return
	}
	if teams == nil {
		teams = []models.Team{}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.Team]{Data: teams})
}

// Create creates a new team.
func (h *OrgTeamHandler) Create(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	body, ok := decodeTeamBody(w, r)
	if !ok {
		return
	}

	team := &models.Team{
		OrgID:       orgID,
		Name:        body.Name,
		Slug:        body.Slug,
		Description: body.Description,
	}

	if err := h.teams.Create(r.Context(), team); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeError(w, r, http.StatusConflict, "SLUG_EXISTS", "a team with this slug already exists")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create team", err)
		return
	}

	teamIDStr := team.ID.String()
	details, _ := json.Marshal(map[string]string{"name": team.Name, "slug": team.Slug})
	emitUserAudit(h.audit, r, models.AuditActionOrgTeamCreated, models.AuditResourceOrgTeam, &teamIDStr, details)

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.Team]{Data: *team})
}

// Get returns a single team with its members.
func (h *OrgTeamHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	teamID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid team ID")
		return
	}

	team, err := h.teams.GetByID(r.Context(), orgID, teamID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "team not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "GET_FAILED", "failed to get team", err)
		return
	}

	members, err := h.teams.ListMembers(r.Context(), orgID, teamID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_MEMBERS_FAILED", "failed to list members", err)
		return
	}
	if members == nil {
		members = []models.User{}
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.TeamWithMembers]{
		Data: models.TeamWithMembers{Team: team, Members: members},
	})
}

// Update modifies a team's name, slug, and description.
func (h *OrgTeamHandler) Update(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	teamID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid team ID")
		return
	}

	body, ok := decodeTeamBody(w, r)
	if !ok {
		return
	}

	if err := h.teams.Update(r.Context(), orgID, teamID, body.Name, body.Slug, body.Description); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "team not found")
			return
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeError(w, r, http.StatusConflict, "SLUG_EXISTS", "a team with this slug already exists")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update team", err)
		return
	}

	teamIDStr := teamID.String()
	details, _ := json.Marshal(map[string]string{"name": body.Name, "slug": body.Slug})
	emitUserAudit(h.audit, r, models.AuditActionOrgTeamUpdated, models.AuditResourceOrgTeam, &teamIDStr, details)

	team, err := h.teams.GetByID(r.Context(), orgID, teamID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "GET_FAILED", "failed to get updated team", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Team]{Data: team})
}

// Delete removes a team.
func (h *OrgTeamHandler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	teamID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid team ID")
		return
	}

	if err := h.teams.Delete(r.Context(), orgID, teamID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "team not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "DELETE_FAILED", "failed to delete team", err)
		return
	}

	teamIDStr := teamID.String()
	emitUserAudit(h.audit, r, models.AuditActionOrgTeamDeleted, models.AuditResourceOrgTeam, &teamIDStr, nil)

	w.WriteHeader(http.StatusNoContent)
}

// AddMember adds a user to a team.
func (h *OrgTeamHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	teamID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid team ID")
		return
	}

	var body struct {
		UserID string `json:"user_id"`
		Role   string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	userID, err := uuid.Parse(body.UserID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_USER_ID", "invalid user_id")
		return
	}

	if body.Role == "" {
		body.Role = models.TeamRoleMember
	}
	if body.Role != models.TeamRoleMember && body.Role != models.TeamRoleLead {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", fmt.Sprintf("role must be '%s' or '%s'", models.TeamRoleMember, models.TeamRoleLead))
		return
	}

	// Verify team exists in this org.
	if _, err := h.teams.GetByID(r.Context(), orgID, teamID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "team not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "GET_FAILED", "failed to verify team", err)
		return
	}

	// Verify the target user belongs to the same org.
	if _, err := h.users.GetByID(r.Context(), orgID, userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "USER_NOT_FOUND", "user not found in this organization")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "GET_USER_FAILED", "failed to verify user", err)
		return
	}

	if err := h.teams.AddMember(r.Context(), orgID, teamID, userID, body.Role); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "team or user not found in this organization")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "ADD_MEMBER_FAILED", "failed to add member", err)
		return
	}

	teamIDStr := teamID.String()
	details, _ := json.Marshal(map[string]string{"user_id": userID.String(), "role": body.Role})
	emitUserAudit(h.audit, r, models.AuditActionOrgTeamMemberAdded, models.AuditResourceOrgTeam, &teamIDStr, details)

	w.WriteHeader(http.StatusNoContent)
}

// RemoveMember removes a user from a team.
func (h *OrgTeamHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	teamID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid team ID")
		return
	}

	userID, err := uuid.Parse(chi.URLParam(r, "userId"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_USER_ID", "invalid user ID")
		return
	}

	// Verify team exists in this org.
	if _, err := h.teams.GetByID(r.Context(), orgID, teamID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "team not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "GET_FAILED", "failed to verify team", err)
		return
	}

	if err := h.teams.RemoveMember(r.Context(), orgID, teamID, userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "MEMBER_NOT_FOUND", "user is not a member of this team")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "REMOVE_MEMBER_FAILED", "failed to remove member", err)
		return
	}

	teamIDStr := teamID.String()
	details, _ := json.Marshal(map[string]string{"user_id": userID.String()})
	emitUserAudit(h.audit, r, models.AuditActionOrgTeamMemberRemoved, models.AuditResourceOrgTeam, &teamIDStr, details)

	w.WriteHeader(http.StatusNoContent)
}

// SyncGitHub syncs teams and memberships from GitHub.
func (h *OrgTeamHandler) SyncGitHub(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgID := middleware.OrgIDFromContext(ctx)

	// Read the optional request body up front so it's available later.
	// An empty body is allowed (we fall back to deriving org from repos), but a
	// malformed body is rejected so the caller knows the org field was ignored.
	var reqBody struct {
		Org string `json:"org"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	if h.githubSvc == nil || h.integrations == nil || h.repos == nil {
		writeError(w, r, http.StatusServiceUnavailable, "GITHUB_NOT_CONFIGURED", "GitHub App is not configured")
		return
	}

	// Find GitHub integration for this org. Filter to active integrations so a
	// stale/revoked integration ahead of an active one doesn't leak bad creds.
	integrations, err := h.integrations.ListByOrgAndProvider(ctx, orgID, string(models.IntegrationProviderGitHub))
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_INTEGRATIONS_FAILED", "failed to list integrations", err)
		return
	}
	var integration *models.Integration
	for i := range integrations {
		if integrations[i].Status == models.IntegrationStatusActive {
			integration = &integrations[i]
			break
		}
	}
	if integration == nil {
		writeError(w, r, http.StatusNotFound, "NO_GITHUB_INTEGRATION", "no active GitHub integration found")
		return
	}

	var config struct {
		InstallationID int64 `json:"installation_id"`
	}
	if integration.Config != nil {
		if err := json.Unmarshal(integration.Config, &config); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_CONFIG", "failed to parse integration config")
			return
		}
	}
	if config.InstallationID == 0 {
		writeError(w, r, http.StatusBadRequest, "NO_INSTALLATION", "GitHub integration has no installation ID")
		return
	}

	token, err := h.githubSvc.GetInstallationToken(ctx, config.InstallationID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "TOKEN_FAILED", "failed to get GitHub installation token", err)
		return
	}

	// Derive GitHub org name from repos, falling back to the request body.
	// Pick the owner that appears most often across the org's repos so we're
	// deterministic when repos span multiple GitHub orgs; ties break
	// alphabetically to keep the choice stable across calls.
	githubOrg := reqBody.Org
	if githubOrg == "" {
		repos, repoErr := h.repos.ListByOrg(ctx, orgID)
		if repoErr == nil {
			counts := map[string]int{}
			for _, repo := range repos {
				if parts := strings.SplitN(repo.FullName, "/", 2); len(parts) == 2 && parts[0] != "" {
					counts[parts[0]]++
				}
			}
			maxCount := 0
			for _, n := range counts {
				if n > maxCount {
					maxCount = n
				}
			}
			tied := make([]string, 0, len(counts))
			for owner, n := range counts {
				if n == maxCount {
					tied = append(tied, owner)
				}
			}
			sort.Strings(tied)
			if len(tied) > 0 {
				githubOrg = tied[0]
			}
		}
	}

	if githubOrg == "" {
		writeError(w, r, http.StatusBadRequest, "NO_GITHUB_ORG", "could not determine GitHub org name — pass {\"org\": \"...\"} in request body")
		return
	}

	// Fetch teams from GitHub.
	ghTeams, err := h.githubSvc.ListOrgTeams(ctx, token, githubOrg)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "GITHUB_TEAMS_FAILED", "failed to fetch GitHub teams", err)
		return
	}

	// Each slot holds a non-nil *db.GitHubTeamSync on success, nil on failure.
	// A nil pointer is an explicit "skipped" marker rather than relying on an
	// uninitialised zero-value struct.
	results := make([]*db.GitHubTeamSync, len(ghTeams))
	skippedSlugs := make([]string, len(ghTeams))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 10)
	for i, ght := range ghTeams {
		wg.Add(1)
		go func(idx int, ght ghservice.GitHubTeam) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			members, memberErr := h.githubSvc.ListTeamMembers(ctx, token, githubOrg, ght.Slug)
			if memberErr != nil {
				zerolog.Ctx(ctx).Warn().Err(memberErr).Str("team", ght.Slug).Msg("failed to fetch team members, skipping")
				skippedSlugs[idx] = ght.Slug
				return
			}

			memberIDs := make([]int64, len(members))
			for j, m := range members {
				memberIDs[j] = m.ID
			}

			var descPtr *string
			if ght.Description != "" {
				desc := ght.Description
				descPtr = &desc
			}
			results[idx] = &db.GitHubTeamSync{
				GitHubTeamID:    ght.ID,
				GitHubTeamSlug:  ght.Slug,
				Name:            ght.Name,
				Description:     descPtr,
				MemberGitHubIDs: memberIDs,
			}
		}(i, ght)
	}
	wg.Wait()

	syncTeams := make([]db.GitHubTeamSync, 0, len(ghTeams))
	skipped := make([]string, 0)
	for i, res := range results {
		if res != nil {
			syncTeams = append(syncTeams, *res)
		} else if skippedSlugs[i] != "" {
			skipped = append(skipped, skippedSlugs[i])
		}
	}

	if err := h.teams.SyncFromGitHub(ctx, orgID, syncTeams); err != nil {
		writeError(w, r, http.StatusInternalServerError, "SYNC_FAILED", "failed to sync teams from GitHub", err)
		return
	}

	emitUserAudit(h.audit, r, models.AuditActionOrgTeamGitHubSynced, models.AuditResourceOrgTeam, nil, nil)

	// Return updated team list. `skipped_slugs` lists GitHub teams whose member
	// fetch failed and were excluded from this sync — non-empty means callers
	// should retry to pick up missing teams. Stale teams that no longer exist
	// on GitHub are intentionally left untouched; this sync only upserts.
	teams, err := h.teams.ListByOrg(ctx, orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "sync succeeded but failed to list teams", err)
		return
	}
	if teams == nil {
		teams = []models.Team{}
	}
	writeJSON(w, http.StatusOK, syncResponse{
		Data:         teams,
		Synced:       len(syncTeams),
		Skipped:      len(skipped),
		SkippedSlugs: skipped,
	})
}

type syncResponse struct {
	Data         []models.Team `json:"data"`
	Synced       int           `json:"synced"`
	Skipped      int           `json:"skipped"`
	SkippedSlugs []string      `json:"skipped_slugs"`
}
