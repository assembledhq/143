package preview

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func newPreviewLifecyclePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL for durable preview startup tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, raw)
	require.NoError(t, err, "connect disposable database server")
	t.Cleanup(func() { require.NoError(t, admin.Close(ctx), "close lifecycle admin connection") })
	name := "preview_lifecycle_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, "CREATE DATABASE "+name)
	require.NoError(t, err, "create isolated preview database")
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
		require.NoError(t, err, "remove isolated preview database")
	})
	parsed, err := url.Parse(raw)
	require.NoError(t, err, "parse disposable database URL")
	parsed.Path = "/" + name
	source, err := filepath.Abs("../../../migrations")
	require.NoError(t, err, "resolve full migration chain")
	migration, err := migrate.New("file://"+source, parsed.String())
	require.NoError(t, err, "initialize isolated migrations")
	require.NoError(t, migration.Up(), "preview lifecycle must exercise current complete schema")
	sourceErr, dbErr := migration.Close()
	require.NoError(t, sourceErr, "close migration source")
	require.NoError(t, dbErr, "close migration connection")
	pool, err := pgxpool.New(ctx, parsed.String())
	require.NoError(t, err, "open isolated preview pool")
	t.Cleanup(pool.Close)
	return pool
}

type branchLifecycleSandbox struct {
	fakeStartRunnerSandboxProvider
	stage     string
	created   []agent.SandboxConfig
	cloned    []string
	commands  []string
	destroyed []string
}

func (f *branchLifecycleSandbox) Create(_ context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
	f.created = append(f.created, cfg)
	if f.stage == "create" {
		return nil, errors.New("sandbox creation failed")
	}
	return &agent.Sandbox{ID: "owned-branch-container", Provider: "docker", HomeDir: cfg.HomeDir, WorkDir: cfg.WorkDir, OrgID: cfg.OrgID, SessionID: cfg.SessionID, Purpose: cfg.Purpose}, nil
}
func (f *branchLifecycleSandbox) CloneRepo(_ context.Context, _ *agent.Sandbox, cloneURL, branch, token string) error {
	f.cloned = []string{cloneURL, branch, token}
	if f.stage == "clone" {
		return errors.New("git clone failed")
	}
	return nil
}
func (f *branchLifecycleSandbox) Exec(_ context.Context, _ *agent.Sandbox, command string, _ io.Writer, _ io.Writer) (int, error) {
	f.commands = append(f.commands, command)
	if f.stage == "checkout" {
		return 1, nil
	}
	return 0, nil
}
func (f *branchLifecycleSandbox) Destroy(_ context.Context, sb *agent.Sandbox) error {
	f.destroyed = append(f.destroyed, sb.ID)
	return nil
}
func (f *branchLifecycleSandbox) IsAlive(context.Context, *agent.Sandbox) (bool, error) {
	return true, nil
}

type branchLifecycleGitHub struct {
	stage         string
	installations []int64
}

func (f *branchLifecycleGitHub) GetInstallationToken(_ context.Context, id int64) (string, error) {
	f.installations = append(f.installations, id)
	if f.stage == "token" {
		return "", errors.New("installation token unavailable")
	}
	return "temporary-clone-token", nil
}

func TestStartRunnerBranchDurableLifecycle(t *testing.T) {
	t.Parallel()
	pool := newPreviewLifecyclePool(t)
	tests := []struct {
		name, stage, wantError string
		wantStatus             models.PreviewStatus
		destroy                bool
	}{
		{name: "pinned branch becomes ready", wantStatus: models.PreviewStatusReady},
		{name: "warm candidate stops after ready", stage: "stop", wantStatus: models.PreviewStatusStopped, destroy: true},
		{name: "foreign org cannot start reservation", stage: "tenant", wantError: "get reserved branch", wantStatus: models.PreviewStatusStarting},
		{name: "mismatched target fails before sandbox", stage: "target", wantError: "target mismatch", wantStatus: models.PreviewStatusFailed},
		{name: "changed target commit fails before sandbox", stage: "commit", wantError: "payload mismatch", wantStatus: models.PreviewStatusFailed},
		{name: "invalid commit fails before shell", stage: "sha", wantError: "invalid commit sha", wantStatus: models.PreviewStatusFailed},
		{name: "disconnected repo fails before sandbox", stage: "repo", wantError: "disconnected", wantStatus: models.PreviewStatusFailed},
		{name: "missing GitHub stops startup", stage: "github", wantError: "github is not configured", wantStatus: models.PreviewStatusFailed},
		{name: "sandbox failure preserves terminal state", stage: "create", wantError: "create sandbox", wantStatus: models.PreviewStatusFailed},
		{name: "token failure destroys new sandbox", stage: "token", wantError: "get github token", wantStatus: models.PreviewStatusFailed, destroy: true},
		{name: "clone failure destroys new sandbox", stage: "clone", wantError: "clone repository", wantStatus: models.PreviewStatusFailed, destroy: true},
		{name: "checkout failure destroys new sandbox", stage: "checkout", wantError: "checkout commit failed", wantStatus: models.PreviewStatusFailed, destroy: true},
		{name: "missing config destroys new sandbox", stage: "config", wantError: "PREVIEW_NO_CONFIG", wantStatus: models.PreviewStatusFailed, destroy: true},
		{name: "runtime failure destroys new sandbox", stage: "launch", wantError: "PREVIEW_START_FAILED", wantStatus: models.PreviewStatusFailed, destroy: true},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Each parallel case owns its org, user, repository, worker and provider.
			// Every production read/write targets that tenant; no global cleanup runs.
			ctx := context.Background()
			org, user, repo := uuid.New(), uuid.New(), uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO organizations (id,name,settings) VALUES ($1,$2,'{}')`, org, "preview-test-"+org.String())
			require.NoError(t, err, "seed independent org")
			_, err = pool.Exec(ctx, `INSERT INTO users (id,org_id,email,name,role) VALUES ($1,$2,$3,'Preview tester','member')`, user, org, user.String()+"@example.test")
			require.NoError(t, err, "seed independent preview owner")
			integration := uuid.New()
			_, err = pool.Exec(ctx, `INSERT INTO integrations (id,org_id,provider) VALUES ($1,$2,'github')`, integration, org)
			require.NoError(t, err, "seed repository integration")
			_, err = pool.Exec(ctx, `INSERT INTO repositories (id,org_id,integration_id,github_id,full_name,default_branch,clone_url,installation_id,status,settings) VALUES ($1,$2,$3,$4,'example/app','main','https://example.test/app.git',7,'active','{}')`, repo, org, integration, index+1)
			require.NoError(t, err, "seed branch repository")
			store := db.NewPreviewStore(pool)
			sandbox := &branchLifecycleSandbox{stage: tt.stage}
			gh := &branchLifecycleGitHub{stage: tt.stage}
			provider := &mockProvider{startHandle: &PreviewHandle{Handle: "runtime", PrimaryPort: 3000}, statusSnap: &PreviewStatusSnapshot{}}
			if tt.stage == "launch" {
				provider.startErr = errors.New("runtime launch failed")
			}
			manager := NewManager(ManagerConfig{Store: store, Provider: provider, SandboxProvider: sandbox, Logger: zerolog.Nop()})
			cfg := reservationPlaceholderPreviewConfig()
			cfg.Name = "branch-app"
			target := &models.PreviewTarget{OrgID: org, RepositoryID: repo, Branch: "feature", CommitSHA: strings.Repeat("a", 40), PreviewConfigName: "branch-app", SourceType: models.PreviewSourceTypeManual, CreatedByUserID: user}
			if tt.stage == "sha" {
				target.CommitSHA = "not-a-sha"
			}
			require.NoError(t, store.CreatePreviewTarget(ctx, target), "create pinned target with current schema")
			tx, err := pool.Begin(ctx)
			require.NoError(t, err, "begin branch reservation")
			reservation, err := manager.ReserveBranchPreviewForWorkerInTx(ctx, tx, StartPreviewInput{OrgID: org, UserID: user, PreviewTargetID: target.ID, Config: cfg, RepositoryID: repo, BaseCommitSHA: target.CommitSHA}, "worker-"+org.String(), "")
			if err != nil {
				rollbackErr := tx.Rollback(ctx)
				require.NoError(t, rollbackErr, "rollback failed fixture reservation")
			}
			require.NoError(t, err, "reserve durable branch preview")
			require.NoError(t, tx.Commit(ctx), "commit startup reservation before worker receives job")
			if tt.stage == "repo" {
				_, err := pool.Exec(ctx, `UPDATE repositories SET status='disconnected' WHERE org_id=$1 AND id=$2`, org, repo)
				require.NoError(t, err, "disconnect test repo after reservation")
			}
			runner := NewStartRunner(StartRunnerConfig{Manager: manager, Previews: store, Repositories: db.NewRepositoryStore(pool), SandboxProvider: sandbox, GitHub: gh, Logger: zerolog.Nop()})
			if tt.stage == "github" {
				runner.github = nil
			}
			payload := StartBranchPreviewJobPayload{OrgID: org, UserID: user, PreviewID: reservation.ID, PreviewTargetID: target.ID, RepositoryID: repo, CommitSHA: target.CommitSHA, Config: cfg, StopAfterReady: tt.stage == "stop"}
			switch tt.stage {
			case "tenant":
				payload.OrgID = uuid.New()
			case "target":
				payload.PreviewTargetID = uuid.New()
			case "commit":
				payload.CommitSHA = strings.Repeat("b", 40)
			case "config":
				payload.Config = nil
			}
			err = runner.StartReservedBranchPreview(ctx, payload)
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError, "failed startup must preserve actionable stage")
			} else {
				require.NoError(t, err, "valid pinned startup should reach durable ready or warm state")
			}
			actual, err := store.GetPreviewInstance(ctx, org, reservation.ID)
			require.NoError(t, err, "reload exact durable preview")
			require.Equal(t, tt.wantStatus, actual.Status, "worker must publish exact terminal or ready status")
			expectedDestroyed := []string(nil)
			if tt.destroy {
				expectedDestroyed = []string{"owned-branch-container"}
			}
			require.Equal(t, expectedDestroyed, sandbox.destroyed, "cleanup must retire only the owned sandbox after failure or an unretained warm candidate")
			_, err = store.GetPreviewInstance(ctx, uuid.New(), reservation.ID)
			require.ErrorIs(t, err, pgx.ErrNoRows, "other tenants must not read preview state")
			if tt.wantError == "" {
				require.Equal(t, []string{"https://example.test/app.git", "feature", "temporary-clone-token"}, sandbox.cloned, "worker must clone exact branch with installation token")
				require.Equal(t, []string{"git checkout --detach " + target.CommitSHA}, sandbox.commands, "runtime must start from pinned commit rather than current branch head")
				require.Equal(t, []int64{7}, gh.installations, "startup must use repository's installation")
				require.Len(t, sandbox.created, 1, "successful startup must own one dedicated sandbox")
				require.Equal(t, org.String(), sandbox.created[0].OrgID, "sandbox ownership must retain exact tenant")
				require.Equal(t, reservation.ID.String(), sandbox.created[0].SessionID, "standalone sandbox must be bound to reservation identity")
				require.Equal(t, "branch_preview", sandbox.created[0].Purpose, "standalone previews must use isolated purpose")
				require.Equal(t, cfg, provider.startConfig, "runtime must receive exact validated config")
				refreshed, err := store.GetPreviewTarget(ctx, org, target.ID)
				require.NoError(t, err, "reload resolved target metadata")
				require.Equal(t, computeConfigDigest(cfg), refreshed.ResolvedConfigDigest, "worker must persist resolved config before launch")
			}
		})
	}
}

// Compile-time contract checks keep the fixtures aligned with production ports.
var _ agent.SandboxProvider = (*branchLifecycleSandbox)(nil)
var _ branchPreviewGitHub = (*branchLifecycleGitHub)(nil)

func TestStartRunnerOptionalSnapshotCache(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                   string
		manager                bool
		explicit, managerCache bool
		expected               string
	}{
		{name: "no manager or cache"},
		{name: "manager without cache is disabled", manager: true},
		{name: "manager cache inherited", manager: true, managerCache: true, expected: "manager"},
		{name: "explicit cache without manager", explicit: true, expected: "explicit"},
		{name: "explicit cache takes precedence", manager: true, explicit: true, managerCache: true, expected: "explicit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			explicit, inherited := &SnapshotCache{}, &SnapshotCache{}
			cfg := StartRunnerConfig{}
			if tt.explicit {
				cfg.SnapshotCache = explicit
			}
			if tt.manager {
				cfg.Manager = &Manager{}
				if tt.managerCache {
					cfg.Manager.snapshotCache = inherited
				}
			}
			runner := NewStartRunner(cfg)
			switch tt.expected {
			case "explicit":
				require.Same(t, explicit, runner.snapshotCache, "explicit cache must take precedence")
			case "manager":
				require.Same(t, inherited, runner.snapshotCache, "configured manager cache must be inherited")
			default:
				require.Nil(t, runner.snapshotCache, "absent cache must remain a nil interface so optional startup does not dereference a typed nil")
			}
		})
	}
}

func TestStartRunnerSessionDurableLifecycle(t *testing.T) {
	t.Parallel()
	pool := newPreviewLifecyclePool(t)
	tests := []struct {
		name, stage, wantError      string
		wantStatus                  models.PreviewStatus
		destroy, keepHold, turnHold bool
	}{
		{name: "existing sandbox becomes ready", wantStatus: models.PreviewStatusReady, keepHold: true},
		{name: "completed turn sandbox remains reusable", stage: "snapshotted", wantStatus: models.PreviewStatusReady, keepHold: true},
		{name: "revision mismatch aborts reservation", stage: "revision", wantError: "workspace revision mismatch", wantStatus: models.PreviewStatusFailed},
		{name: "foreign tenant cannot claim reservation", stage: "tenant", wantError: "get reserved preview", wantStatus: models.PreviewStatusStarting, keepHold: true},
		{name: "missing session aborts reservation", stage: "missing", wantError: "get session", wantStatus: models.PreviewStatusFailed},
		{name: "provisioning turn remains retryable", stage: "busy", wantError: "SANDBOX_BUSY", wantStatus: models.PreviewStatusStarting, keepHold: true, turnHold: true},
		{name: "expired snapshot becomes terminal", stage: "destroyed", wantError: "SNAPSHOT_EXPIRED", wantStatus: models.PreviewStatusFailed},
		{name: "missing config retires unheld container", stage: "config", wantError: "PREVIEW_NO_CONFIG", wantStatus: models.PreviewStatusFailed, destroy: true},
		{name: "missing config preserves active turn container", stage: "config", wantError: "PREVIEW_NO_CONFIG", wantStatus: models.PreviewStatusFailed, turnHold: true},
		{name: "launch failure retires unheld container", stage: "launch", wantError: "PREVIEW_START_FAILED", wantStatus: models.PreviewStatusFailed, destroy: true},
		{name: "launch failure preserves active turn container", stage: "launch", wantError: "PREVIEW_START_FAILED", wantStatus: models.PreviewStatusFailed, turnHold: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			org, user := uuid.New(), uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO organizations (id,name,settings) VALUES ($1,$2,'{}')`, org, "preview-test-"+org.String())
			require.NoError(t, err, "seed isolated session tenant")
			_, err = pool.Exec(ctx, `INSERT INTO users (id,org_id,email,name,role) VALUES ($1,$2,$3,'Preview tester','member')`, user, org, user.String()+"@example.test")
			require.NoError(t, err, "seed isolated session owner")
			sessions := db.NewSessionStore(pool)
			session := &models.Session{OrgID: org, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusIdle, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
			require.NoError(t, sessions.Create(ctx, session), "create session with primary thread and changeset")
			state := models.SandboxStateRunning
			if tt.stage == "busy" {
				state = models.SandboxStateNone
			}
			if tt.stage == "snapshotted" {
				state = models.SandboxStateSnapshotted
			}
			if tt.stage == "destroyed" {
				state = models.SandboxStateDestroyed
			}
			_, err = pool.Exec(ctx, `UPDATE sessions SET container_id='session-container', sandbox_state=$1, turn_holding_container=$2, workspace_revision=7 WHERE org_id=$3 AND id=$4`, state, tt.turnHold, org, session.ID)
			require.NoError(t, err, "seed exact live sandbox ownership")
			store := db.NewPreviewStore(pool)
			sandbox := &branchLifecycleSandbox{}
			provider := &mockProvider{startHandle: &PreviewHandle{Handle: "runtime", PrimaryPort: 3000}, statusSnap: &PreviewStatusSnapshot{}}
			if tt.stage == "launch" {
				provider.startErr = errors.New("runtime launch failed")
			}
			manager := NewManager(ManagerConfig{Store: store, SessionStore: sessions, Provider: provider, SandboxProvider: sandbox, WorkerNodeID: "worker-" + org.String(), Logger: zerolog.Nop()})
			cfg := reservationPlaceholderPreviewConfig()
			updatedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			reservation, err := manager.ReservePreview(ctx, StartPreviewInput{OrgID: org, UserID: user, SessionID: session.ID, Config: cfg, WorkspaceRevision: 7, WorkspaceRevisionUpdatedAt: updatedAt})
			require.NoError(t, err, "reserve preview and acquire durable hold")
			runner := NewStartRunner(StartRunnerConfig{Manager: manager, Sessions: sessions, Previews: store, SandboxProvider: sandbox, Logger: zerolog.Nop()})
			payload := StartPreviewJobPayload{OrgID: org, UserID: user, SessionID: session.ID, PreviewID: reservation.ID, Config: cfg, WorkspaceRevision: 7, WorkspaceRevisionUpdatedAt: updatedAt}
			switch tt.stage {
			case "tenant":
				payload.OrgID = uuid.New()
			case "revision":
				payload.WorkspaceRevision = 8
			case "missing":
				payload.SessionID = uuid.New()
			case "config":
				payload.Config = nil
			}
			err = runner.StartReservedPreview(ctx, payload)
			if tt.wantError == "" {
				require.NoError(t, err, "existing workspace must launch without hydrating another container")
			} else {
				require.ErrorContains(t, err, tt.wantError, "startup must preserve the correct retryable or terminal error")
			}
			actual, err := store.GetPreviewInstance(ctx, org, reservation.ID)
			require.NoError(t, err, "reload durable reservation")
			require.Equal(t, tt.wantStatus, actual.Status, "startup outcome must be persisted")
			require.Equal(t, tt.keepHold, actual.PreviewHoldingContainer, "only retryable or active previews may retain a container hold")
			fresh, err := sessions.GetByID(ctx, org, session.ID)
			require.NoError(t, err, "reload session ownership after startup")
			require.Equal(t, tt.turnHold, fresh.TurnHoldingContainer, "preview failure must never release a turn's independent hold")
			var destroyed []string
			if tt.destroy {
				destroyed = []string{"session-container"}
			}
			require.Equal(t, destroyed, sandbox.destroyed, "failed preview may destroy only an otherwise unheld session container")
			if tt.destroy {
				require.Nil(t, fresh.ContainerID, "destroyed sandbox identity must be cleared durably")
			} else {
				require.Equal(t, func() *string { v := "session-container"; return &v }(), fresh.ContainerID, "retained sandbox identity must remain available to its owner")
			}
		})
	}
}

func TestSessionPreviewWarmBuildDurableLifecycle(t *testing.T) {
	t.Parallel()
	pool := newPreviewLifecyclePool(t)
	tests := []struct{ name, stage, status, errorText string }{
		{name: "warm result is stopped and published", status: "succeeded"},
		{name: "new revision supersedes queued warm build", stage: "revision", status: "skipped_superseded"},
		{name: "missing snapshot fails before reservation", stage: "snapshot", status: "failed", errorText: "session snapshot is unavailable"},
		{name: "active user preview wins", stage: "active", status: "skipped_user_started"},
		{name: "invalid workspace config retires reservation", stage: "config", status: "failed", errorText: "read workspace config"},
		{name: "launch failure stays terminal", stage: "launch", status: "failed", errorText: "PREVIEW_START_FAILED"},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			org, user, repo, integration := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO organizations (id,name,settings) VALUES ($1,$2,'{}')`, org, "warm-test-"+org.String())
			require.NoError(t, err, "create isolated warm-build tenant")
			_, err = pool.Exec(ctx, `INSERT INTO users (id,org_id,email,name,role) VALUES ($1,$2,$3,'Warm tester','member')`, user, org, user.String()+"@example.test")
			require.NoError(t, err, "create warm-build owner")
			_, err = pool.Exec(ctx, `INSERT INTO integrations (id,org_id,provider) VALUES ($1,$2,'github')`, integration, org)
			require.NoError(t, err, "create repository integration")
			_, err = pool.Exec(ctx, `INSERT INTO repositories (id,org_id,integration_id,github_id,full_name,default_branch,clone_url,installation_id,status,settings) VALUES ($1,$2,$3,$4,'example/app','main','https://example.test/app.git',7,'active','{}')`, repo, org, integration, index+1)
			require.NoError(t, err, "create warm-build repository")
			sessions, store := db.NewSessionStore(pool), db.NewPreviewStore(pool)
			session := &models.Session{OrgID: org, RepositoryID: &repo, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusIdle, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
			require.NoError(t, sessions.Create(ctx, session), "create session with current schema and primary thread")
			_, err = pool.Exec(ctx, `UPDATE sessions SET container_id='warm-container', snapshot_key='snapshot', sandbox_state='running', turn_holding_container=true, workspace_revision=7 WHERE org_id=$1 AND id=$2`, org, session.ID)
			require.NoError(t, err, "seed independently held reusable workspace")
			run, err := store.UpsertSessionPreviewPrewarmRun(ctx, &models.SessionPreviewPrewarmRun{OrgID: org, RepositoryID: repo, SessionID: session.ID, WorkspaceRevision: 7, ConfigDigest: "queued-digest", Mode: models.PreviewSessionPrewarmModeSmart, Decision: models.PreviewSpeculativeDecisionWarmCandidate, Status: "queued", CapacitySnapshot: []byte(`{}`)})
			require.NoError(t, err, "persist queued warm-build identity")
			sandbox := &branchLifecycleSandbox{}
			provider := &mockProvider{startHandle: &PreviewHandle{Handle: "warm-runtime", PrimaryPort: 3000}, statusSnap: &PreviewStatusSnapshot{}}
			if tt.stage == "launch" {
				provider.startErr = errors.New("runtime launch failed")
			}
			manager := NewManager(ManagerConfig{Store: store, SessionStore: sessions, Provider: provider, SandboxProvider: sandbox, WorkerNodeID: "worker-" + org.String(), Logger: zerolog.Nop()})
			reader := startRunnerFileReader{content: `{"preview":{"name":"warm-app","primary":"web","services":{"web":{"command":["npm","start"],"port":3000,"ready":{"http_path":"/"}}}}}`}
			if tt.stage == "config" {
				reader.content = "{"
			}
			if tt.stage == "revision" {
				_, err = pool.Exec(ctx, `UPDATE sessions SET workspace_revision=8 WHERE org_id=$1 AND id=$2`, org, session.ID)
				require.NoError(t, err, "advance workspace before queued warm build starts")
			}
			if tt.stage == "snapshot" {
				_, err = pool.Exec(ctx, `UPDATE sessions SET snapshot_key=NULL WHERE org_id=$1 AND id=$2`, org, session.ID)
				require.NoError(t, err, "remove unavailable warm-build snapshot")
			}
			var userPreviewID uuid.UUID
			if tt.stage == "active" {
				reservation, err := manager.ReservePreview(ctx, StartPreviewInput{OrgID: org, UserID: user, SessionID: session.ID, Config: reservationPlaceholderPreviewConfig(), WorkspaceRevision: 7})
				require.NoError(t, err, "reserve user-requested preview before speculative work")
				userPreviewID = reservation.ID
			}
			runner := NewStartRunner(StartRunnerConfig{Manager: manager, Sessions: sessions, Previews: store, SandboxProvider: sandbox, FileReader: reader, Logger: zerolog.Nop()})
			err = runner.WarmSessionPreview(ctx, SessionPreviewWarmBuildJobPayload{OrgID: org, UserID: user, RepositoryID: repo, SessionID: session.ID, WorkspaceRevision: 7, ConfigDigest: "queued-digest", Reason: "test"})
			if tt.errorText == "" {
				require.NoError(t, err, "valid or superseded warm work must settle without a retry")
			} else {
				require.ErrorContains(t, err, tt.errorText, "failed warm work must retain its actionable error")
			}
			actual, err := store.GetLatestSessionPreviewPrewarmRun(ctx, org, session.ID, models.PreviewSpeculativeDecisionWarmCandidate)
			require.NoError(t, err, "reload tenant-scoped durable warm result")
			require.Equal(t, run.ID, actual.ID, "warm result must update the original queued identity")
			require.Equal(t, tt.status, actual.Status, "warm result must record success, supersession or failure precisely")
			require.NotNil(t, actual.CompletedAt, "every settled warm run must have a durable completion timestamp")
			fresh, err := sessions.GetByID(ctx, org, session.ID)
			require.NoError(t, err, "reload independent session ownership")
			require.True(t, fresh.TurnHoldingContainer, "speculative work must preserve the session turn's independent hold")
			require.Empty(t, sandbox.destroyed, "speculative work must never destroy a turn-held workspace")
			if tt.status == "succeeded" {
				require.NotNil(t, actual.PreviewID, "successful warm result must publish its exact preview identity")
				require.NotNil(t, actual.PreviewGroupID, "successful warm result must publish its warm-group identity")
				instance, err := store.GetPreviewInstance(ctx, org, *actual.PreviewID)
				require.NoError(t, err, "reload published warm preview")
				require.Equal(t, models.PreviewStatusStopped, instance.Status, "warm build must stop live services before publishing reusable result")
			} else if tt.stage == "active" {
				instance, err := store.GetActivePreviewForSession(ctx, org, session.ID)
				require.NoError(t, err, "user preview must remain active")
				require.Equal(t, userPreviewID, instance.ID, "speculative skip must preserve the user's exact reservation")
			}
		})
	}
}
