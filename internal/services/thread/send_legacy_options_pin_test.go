package thread

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestLegacySendOptionsContinuationEnvelope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                      string
		withExtras, emptyOverride bool
		container, worker         string
	}{
		{name: "default continuation"},
		{name: "all optional payload and routing fields", withExtras: true, container: "synthetic-container", worker: "synthetic-worker"},
		{name: "empty override preserves lane key", emptyOverride: true},
		{name: "worker without container is not affinity", worker: "synthetic-worker"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, sessionID, lane, changeset, holder, request := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			input := SendMessageInput{OrgID: org, SessionID: sessionID, ThreadID: lane}
			thread := models.SessionThread{ID: lane, OrgID: org, SessionID: sessionID}
			session := models.Session{}
			if tt.container != "" {
				session.ContainerID = &tt.container
			}
			if tt.worker != "" {
				session.WorkerNodeID = &tt.worker
			}
			key := "continue_session:" + lane.String()
			payload := map[string]string{"org_id": org.String(), "session_id": sessionID.String(), "thread_id": lane.String()}
			var answer *models.HumanInputRequest
			var target *string
			if tt.withExtras {
				key = "synthetic-explicit-key"
				input.ContinuationDedupeKeyOverride = &key
				input.ChangesetID, input.PostSuccessAction, input.PostSuccessAuthorMode = &changeset, "synthetic-action", "synthetic-author-mode"
				answer = &models.HumanInputRequest{ID: request}
				payload["changeset_id"], payload["changeset_lease_holder_id"] = changeset.String(), holder.String()
				payload["post_success_action"], payload["post_success_author_mode"] = input.PostSuccessAction, input.PostSuccessAuthorMode
				payload["human_input_request_id"] = request.String()
				target = &tt.worker
			}
			if tt.emptyOverride {
				empty := ""
				input.ContinuationDedupeKeyOverride = &empty
			}
			expected := db.EnqueueOpts{Queue: "agent", JobType: "continue_session", Payload: payload, Priority: 5, DedupeKey: &key, TargetNodeID: target}
			require.Equal(t, expected, legacyContinuationOpts(input, &holder, thread, session, answer), "the actual helper must retain the entire legacy payload, concrete queue/type/priority/target and unset scheduling defaults")
		})
	}
}

func TestLegacySendOptionsLiveEnvelope(t *testing.T) {
	t.Parallel()
	syntheticFailure := errors.New("synthetic runtime reader failure")
	tests := []struct {
		name             string
		readerConfigured bool
		owner            string
		readErr, wantErr error
	}{
		{name: "runtime reader absent"},
		{name: "runtime is absent", readerConfigured: true, readErr: pgx.ErrNoRows},
		{name: "runtime has no owner", readerConfigured: true},
		{name: "runtime owner supplies target", readerConfigured: true, owner: "synthetic-runtime-owner"},
		{name: "runtime reader failure returns zero options", readerConfigured: true, readErr: syntheticFailure, wantErr: syntheticFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, sessionID, lane := uuid.New(), uuid.New(), uuid.New()
			ctx := context.Background()
			tx := &struct{ pgx.Tx }{}
			reader := &legacySendOptionsRuntimeReader{t: t, ctx: ctx, tx: tx, org: org, lane: lane, runtime: models.ThreadRuntime{OwnerNodeID: tt.owner}, err: tt.readErr}
			service := &Service{send: SendDependencies{Legacy: reader}}
			if tt.readerConfigured {
				// Only the presence of this optional legacy dependency is checked;
				// the actual observation must use the supplied-transaction port.
				service.runtimeOwnerStore = db.NewThreadRuntimeStore(nil)
			}
			actual, err := service.legacyLiveDeliveryOptsInTx(ctx, tx, SendMessageInput{OrgID: org, SessionID: sessionID, ThreadID: lane})
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr, "a runtime observation failure must remain a command failure, never a partial envelope")
				require.Equal(t, db.EnqueueOpts{}, actual, "failed live routing must expose no partially usable job options")
			} else {
				require.NoError(t, err, "complete or absent optional runtime routing must preserve the legacy live envelope")
				key := "deliver_thread_inbox:" + lane.String()
				var target *string
				if tt.owner != "" {
					target = &tt.owner
				}
				expected := db.EnqueueOpts{Queue: "agent", JobType: "deliver_thread_inbox", Payload: map[string]string{"org_id": org.String(), "session_id": sessionID.String(), "thread_id": lane.String()}, Priority: 7, DedupeKey: &key, TargetNodeID: target}
				require.Equal(t, expected, actual, "the actual helper must retain the complete live payload, queue/type/priority/target and unset scheduling defaults")
			}
			wantReads := 0
			if tt.readerConfigured {
				wantReads = 1
			}
			require.Equal(t, wantReads, reader.calls, "runtime discovery must use exactly the optional transaction-bound reader and no receiver-pool query")
		})
	}
}

type legacySendOptionsRuntimeReader struct {
	LegacySendPersistence
	t         *testing.T
	ctx       context.Context
	tx        pgx.Tx
	org, lane uuid.UUID
	runtime   models.ThreadRuntime
	err       error
	calls     int
}

func (p *legacySendOptionsRuntimeReader) GetActiveThreadRuntimeInTx(ctx context.Context, tx pgx.Tx, org, lane uuid.UUID) (models.ThreadRuntime, error) {
	p.t.Helper()
	p.calls++
	require.Equal(p.t, p.ctx, ctx, "the live routing reader must retain the supplied callback context")
	require.Same(p.t, p.tx, tx, "the live routing reader must retain the supplied transaction identity")
	require.Equal(p.t, []uuid.UUID{p.org, p.lane}, []uuid.UUID{org, lane}, "runtime routing must retain the explicit tenant and lane")
	return p.runtime, p.err
}

// These review-trigger pins cover the complete option-builder bodies because
// the syntax inventory does not hash every payload, queue, priority or target
// expression. They do not make either helper an admission or execution owner.
func TestLegacySendOptionsBodyPins(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, fingerprint string
	}{
		{name: "legacyContinuationOpts", fingerprint: "dfee9e0bed1cde331c83b4600dc00c129df092ab067e349fdf427c569862cafd"},
		{name: "legacyLiveDeliveryOptsInTx", fingerprint: "88b3a815ebf4564d81260eb46cee4b182ba9ad0a2af365b7c38629cecddd1330"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := legacySendOptionsSourceBody(t, tt.name)
			require.Equal(t, tt.fingerprint, legacySendOptionsFingerprint(body), "a complete legacy option-builder body change requires explicit payload, routing and compatibility review rather than an automatic pin refresh")
		})
	}
}

func TestLegacySendOptionsBodyPinsDetectEnvelopeDrift(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, function, before, after string
	}{
		{name: "continuation queue", function: "legacyContinuationOpts", before: `Queue: "agent"`, after: `Queue: "other"`},
		{name: "continuation job type", function: "legacyContinuationOpts", before: `JobType: "continue_session"`, after: `JobType: "other"`},
		{name: "continuation payload", function: "legacyContinuationOpts", before: `"session_id": thread.SessionID.String()`, after: `"session_id": input.ThreadID.String()`},
		{name: "continuation priority", function: "legacyContinuationOpts", before: `Priority: 5`, after: `Priority: 6`},
		{name: "continuation target", function: "legacyContinuationOpts", before: `TargetNodeID: models.SessionWorkerTarget(&session)`, after: `TargetNodeID: nil`},
		{name: "live delivery queue", function: "legacyLiveDeliveryOptsInTx", before: `Queue: deliverThreadInboxQueue`, after: `Queue: "other"`},
		{name: "live delivery job type", function: "legacyLiveDeliveryOptsInTx", before: `JobType: deliverThreadInboxJobType`, after: `JobType: "other"`},
		{name: "live delivery payload", function: "legacyLiveDeliveryOptsInTx", before: `"session_id": input.SessionID.String()`, after: `"session_id": input.ThreadID.String()`},
		{name: "live delivery priority", function: "legacyLiveDeliveryOptsInTx", before: `Priority: 7`, after: `Priority: 8`},
		{name: "live delivery target", function: "legacyLiveDeliveryOptsInTx", before: `TargetNodeID: target`, after: `TargetNodeID: nil`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := legacySendOptionsSourceBody(t, tt.function)
			changed := strings.Replace(body, tt.before, tt.after, 1)
			require.NotEqual(t, body, changed, "calibration must change the actual option-builder source rather than an unrelated parallel fixture")
			_, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", "package thread\nfunc calibrated() "+changed, 0)
			require.NoError(t, err, "the drift calibration must retain a syntactically valid function body")
			require.NotEqual(t, legacySendOptionsFingerprint(body), legacySendOptionsFingerprint(changed), "payload, queue, job type, priority and target changes must invalidate the complete reviewed builder pin")
		})
	}
}

func legacySendOptionsSourceBody(t *testing.T, name string) string {
	t.Helper()
	root, err := os.OpenRoot(".")
	require.NoError(t, err, "open only the current thread package as the source-read boundary")
	t.Cleanup(func() { require.NoError(t, root.Close(), "close the bounded legacy option source reader") })
	source, err := root.ReadFile("send_legacy_atomic.go")
	require.NoError(t, err, "read the actual legacy option builders through the fixed package root")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "send_legacy_atomic.go", source, 0)
	require.NoError(t, err, "parse the real legacy option builders without source comments")
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != name {
			continue
		}
		var body bytes.Buffer
		require.NoError(t, format.Node(&body, fset, function.Body), "canonicalize the complete actual option-builder body with its parsed source positions")
		return body.String()
	}
	t.Fatalf("the explicitly reviewed legacy option builder %q must remain present", name)
	return ""
}

func legacySendOptionsFingerprint(body string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
}
