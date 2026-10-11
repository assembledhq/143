package preview

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type previewGitHubReceiptFixture struct {
	fail   string
	calls  []string
	bodies []string
}

func (f *previewGitHubReceiptFixture) record(kind string, values ...any) error {
	f.calls = append(f.calls, kind+fmt.Sprint(values...))
	if f.fail == kind {
		return errors.New("provider unavailable")
	}
	return nil
}
func (f *previewGitHubReceiptFixture) CreateIssueComment(_ context.Context, owner, repo string, n int, body string) (int64, error) {
	f.bodies = append(f.bodies, body)
	return 42, f.record("create-comment", owner, "/", repo, "/", n)
}
func (f *previewGitHubReceiptFixture) UpdateIssueComment(_ context.Context, owner, repo string, id int64, body string) error {
	f.bodies = append(f.bodies, body)
	return f.record("update-comment", owner, "/", repo, "/", id)
}
func (f *previewGitHubReceiptFixture) CreateDeployment(_ context.Context, owner, repo, ref, env string) (int64, error) {
	return 87, f.record("deployment", owner, "/", repo, "/", ref, "/", env)
}
func (f *previewGitHubReceiptFixture) UpdateDeploymentStatus(_ context.Context, owner, repo string, id int64, state, target string) error {
	return f.record("deployment-status", owner, "/", repo, "/", id, "/", state, "/", target)
}
func (f *previewGitHubReceiptFixture) CreateCommitStatus(_ context.Context, owner, repo, sha, state, kind, target, description string) error {
	return f.record("commit-status", owner, "/", repo, "/", sha, "/", state, "/", kind, "/", target, "/", description)
}

func TestPRPreviewPublicationLifecycle(t *testing.T) {
	t.Parallel()
	pool := newPreviewLifecyclePool(t)
	tests := []struct {
		name, event                     string
		want                            models.PRPreviewStatus
		header                          string
		commit, description, deployment string
	}{
		{"opened", "open", models.PRPreviewStatusNeverStarted, "### 🔍 Preview available for this PR", "", "", ""},
		{"starting", "start", models.PRPreviewStatusRunning, "### ✅ Preview is live", "pending", "Preview is starting...", "pending"},
		{"ready", "ready", models.PRPreviewStatusRunning, "### ✅ Preview is live", "success", "Preview is live", "success"},
		{"stopped", "stop", models.PRPreviewStatusStopped, "### 🔍 Preview available for this PR", "failure", "Preview stopped", "inactive"},
		{"closed", "close", models.PRPreviewStatusClosed, "### 🚫 PR closed", "", "", ""},
		{"merged", "merge", models.PRPreviewStatusMerged, "### 🎉 PR merged", "", "", ""},
		{"agent changed files", "agent", models.PRPreviewStatusNeverStarted, "### ✅ Preview is live", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			org, repo, integration, session, previewID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name,settings) VALUES($1,$2,'{}')`, org, "pr-preview-"+org.String())
			require.NoError(t, err, "seed isolated publication tenant")
			_, err = pool.Exec(ctx, `INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, integration, org)
			require.NoError(t, err, "seed repository integration")
			_, err = pool.Exec(ctx, `INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id,status,settings) VALUES($1,$2,$3,$4,'example/app','https://example.test/app.git',7,'active','{}')`, repo, org, integration, time.Now().UnixNano())
			require.NoError(t, err, "seed isolated preview repository")
			store := db.NewPreviewStore(pool)
			gh := &previewGitHubReceiptFixture{}
			service := NewPRPreviewIntegration(store, gh, zerolog.Nop(), "https://app.example.test")
			// Open a state without an instance FK; startup cases populate a real preview below.
			require.NoError(t, service.OnPROpened(ctx, org, repo, 9, "example", "app", session), "initial publication must store its provider receipt")
			state, err := store.GetPRPreviewState(ctx, org, repo, 9)
			require.NoError(t, err, "read exact tenant publication state")
			gh.calls = nil
			gh.bodies = nil
			instance := &models.PreviewInstance{ID: previewID, BaseCommitSHA: "abc123"}
			if tt.event == "start" {
				// LastPreviewInstanceID is an FK, so startup uses a real target and instance.
				user := uuid.New()
				_, err = pool.Exec(ctx, `INSERT INTO users(id,org_id,email,name,role) VALUES($1,$2,$3,'Tester','member')`, user, org, user.String()+"@example.test")
				require.NoError(t, err, "seed preview owner")
				target := &models.PreviewTarget{OrgID: org, RepositoryID: repo, Branch: "feature", CommitSHA: "abc123", PreviewConfigName: "app", SourceType: models.PreviewSourceTypeManual, CreatedByUserID: user}
				require.NoError(t, store.CreatePreviewTarget(ctx, target), "seed preview target")
				instance = &models.PreviewInstance{PreviewTargetID: &target.ID, OrgID: org, UserID: user, Name: "app", Provider: ProviderDocker, Status: models.PreviewStatusStarting, ExpiresAt: time.Now().Add(time.Hour), LastPath: "/", BaseCommitSHA: "abc123"}
				require.NoError(t, store.CreateBranchPreviewInstance(ctx, instance), "seed actual reserved preview")
			}
			link := "https://app.example.test/sessions/" + session.String() + "?preview=1"
			switch tt.event {
			case "open":
				err = service.OnPROpened(ctx, org, repo, 9, "example", "app", session)
			case "start":
				err = service.OnPreviewStarted(ctx, state, instance, "example", "app", link)
			case "ready":
				err = service.OnPreviewReady(ctx, state, instance, "example", "app", link, "https://images.example.test/ready.png")
			case "stop":
				stopped := time.Now().Add(-2 * time.Minute)
				instance.StoppedAt = &stopped
				state.LastScreenshotBlobPath = "https://images.example.test/previous.png"
				err = service.OnPreviewStopped(ctx, state, instance, "example", "app", link)
			case "close", "merge":
				err = service.OnPRClosed(ctx, org, repo, 9, "example", "app", tt.event == "merge")
			case "agent":
				err = service.OnAgentChanges(ctx, state, "example", "app", link, 1, "https://images.example.test/changes.png")
			}
			require.NoError(t, err, "publication event should persist exact state and receipt")
			fresh, err := store.GetPRPreviewState(ctx, org, repo, 9)
			require.NoError(t, err, "reload publication state")
			require.Equal(t, tt.want, fresh.Status, "event must persist intended lifecycle status")
			require.Equal(t, int64(42), *fresh.GitHubCommentID, "updates must retain the original provider comment receipt")
			if tt.event == "start" {
				require.Equal(t, &instance.ID, fresh.LastPreviewInstanceID, "startup must bind exact reserved instance")
			}
			expected := []string{"update-commentexample/app/42"}
			if tt.event == "open" {
				expected = []string{"create-commentexample/app/9"}
			}
			if tt.commit != "" {
				expected = append(expected, "commit-statusexample/app/abc123/"+tt.commit+"/preview/143/"+link+"/"+tt.description, "deploymentexample/app/abc123/preview", "deployment-statusexample/app/87/"+tt.deployment+"/"+link)
			}
			require.Equal(t, expected, gh.calls, "GitHub surfaces must target exact owner, repository, revision and receipt in order")
			require.Len(t, gh.bodies, 1, "event must publish one comment body")
			require.Contains(t, gh.bodies[0], commentHeader+"\n"+tt.header, "comment must retain recognizable surface and correct lifecycle headline")
			if tt.event != "close" && tt.event != "merge" {
				require.Contains(t, gh.bodies[0], link, "preview link must route to exact session")
			}
			_, err = store.GetPRPreviewState(ctx, uuid.New(), repo, 9)
			require.Error(t, err, "another tenant must not read a publication receipt")
		})
	}
}

func TestPRPreviewPublicationFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, event, providerFailure string
		dbFailure, missingComment    bool
		wantError                    string
	}{
		{name: "open provider failure", event: "open", providerFailure: "create-comment", wantError: "create PR comment"},
		{name: "open persistence failure", event: "open", dbFailure: true, wantError: "upsert PR preview state"},
		{name: "closed state lookup failure", event: "close", dbFailure: true, wantError: "get PR preview state"},
		{name: "ready persistence failure", event: "ready", dbFailure: true, wantError: "upsert PR preview state"},
		{name: "create missing comment failure", event: "comment", missingComment: true, providerFailure: "create-comment", wantError: "create PR comment"},
		{name: "update existing comment failure", event: "comment", providerFailure: "update-comment", wantError: "provider unavailable"},
		{name: "persist newly created receipt failure is logged", event: "comment", missingComment: true, dbFailure: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated persistence fixture")
			defer mock.Close()
			gh := &previewGitHubReceiptFixture{fail: tt.providerFailure}
			service := NewPRPreviewIntegration(db.NewPreviewStore(mock), gh, zerolog.Nop(), "")
			id := int64(42)
			state := &models.PRPreviewState{OrgID: uuid.New(), RepoID: uuid.New(), PRNumber: 9, GitHubCommentID: &id}
			if tt.missingComment {
				state.GitHubCommentID = nil
			}
			if tt.dbFailure {
				pattern := "INSERT INTO pr_preview_state"
				if tt.event == "close" {
					pattern = "SELECT .* FROM pr_preview_state"
				}
				args := []any{pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()}
				if tt.event != "close" {
					for len(args) < 12 {
						args = append(args, pgxmock.AnyArg())
					}
				}
				mock.ExpectQuery(pattern).WithArgs(args...).WillReturnError(errors.New("database unavailable"))
			}
			switch tt.event {
			case "open":
				err = service.OnPROpened(context.Background(), state.OrgID, state.RepoID, 9, "example", "app", uuid.New())
			case "close":
				err = service.OnPRClosed(context.Background(), state.OrgID, state.RepoID, 9, "example", "app", true)
			case "ready":
				err = service.OnPreviewReady(context.Background(), state, &models.PreviewInstance{}, "example", "app", "https://app.example.test", "")
			case "comment":
				err = service.updateComment(context.Background(), "example", "app", state, "exact body")
			}
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError, "failure must preserve actionable stage")
			} else {
				require.NoError(t, err, "best-effort receipt persistence must not falsely claim provider publication failed")
				require.Equal(t, int64(42), *state.GitHubCommentID, "new provider receipt must remain available to caller")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "persistence failure must not continue into unexpected database writes")
		})
	}
}

func TestPRPreviewCommentFormatting(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		elapsed  time.Duration
		expected string
	}{{"seconds", 10 * time.Second, "just now"}, {"one minute", 90 * time.Second, "1 minute ago"}, {"minutes", 5 * time.Minute, "5 minutes ago"}, {"one hour", 90 * time.Minute, "1 hour ago"}, {"hours", 5 * time.Hour, "5 hours ago"}, {"one day", 25 * time.Hour, "1 day ago"}, {"days", 49 * time.Hour, "2 days ago"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, formatRelativeTime(time.Now().Add(-tt.elapsed)), "relative timestamp must use correct unit and plural")
		})
	}
	require.Equal(t, "https://example.test/a%29", sanitizeMarkdownURL("https://example.test/a)"), "Markdown delimiters in destinations must be escaped")
	require.Equal(t, "", sanitizeMarkdownURL("https://example.test/%zz"), "malformed URL must not produce a broken link")
	require.Equal(t, "Oct 1, 2026 12:00 UTC", formatTimestamp(time.Date(2026, 10, 1, 14, 0, 0, 0, time.FixedZone("test", 7200))), "absolute timestamp must be expressed in UTC")
	service := NewPRPreviewIntegration(nil, nil, zerolog.Nop(), "")
	id := uuid.New()
	require.Equal(t, "https://app.143.dev/sessions/"+id.String()+"?preview=1", service.buildSessionURL(id), "default app URL must include session and preview selector")
	screenshot := "https://images.example.test/final.png"
	body := service.buildClosedComment(true, &screenshot)
	require.Equal(t, strings.Join([]string{commentHeader, "### 🎉 PR merged", "", "Preview session has ended.", "", "**Final screenshot:**", "", "![Final preview screenshot](https://images.example.test/final.png)", "", "---", "*Powered by [143](https://143.dev)*", ""}, "\n"), body, "terminal comment must retain exact final screenshot and completed state")
}
