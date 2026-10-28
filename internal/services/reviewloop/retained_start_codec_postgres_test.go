package reviewloop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// These are full codec envelopes installed through genuine T0, not SQL-shaped
// stand-ins. No case executes E1, creates a loop/pass, or sends a command.
func TestRetainedStartCodecJSONBPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		form              models.ReviewLoopSource
		present           bool
		mutateEnvelope    func(*retainedStartEnvelope)
		mutatePreparation func(*testing.T, *db.ReviewLoopStartPreparation)
		prepareCode       string
		restoreError      string
	}{
		{name: "manual nil optionals", form: models.ReviewLoopSourceManual},
		{name: "publication present optionals and complete command", form: models.ReviewLoopSourcePublication, present: true},
		{name: "automation present caller identity", form: models.ReviewLoopSourceAutomation, present: true},
		{name: "independent organization mismatch", mutateEnvelope: func(e *retainedStartEnvelope) { e.OrgID = uuid.New() }, restoreError: db.ErrReviewLoopStartIdentity.Error()},
		{name: "independent session mismatch", mutateEnvelope: func(e *retainedStartEnvelope) { e.SessionID = uuid.New() }, restoreError: db.ErrReviewLoopStartIdentity.Error()},
		{name: "independent loop mismatch", mutateEnvelope: func(e *retainedStartEnvelope) { e.LoopID = uuid.New() }, restoreError: db.ErrReviewLoopStartIdentity.Error()},
		{name: "independent first pass mismatch", mutateEnvelope: func(e *retainedStartEnvelope) { e.PassID = uuid.New() }, restoreError: db.ErrReviewLoopStartIdentity.Error()},
		{name: "independent thread mismatch", mutateEnvelope: func(e *retainedStartEnvelope) { e.Thread.ID = uuid.New() }, restoreError: db.ErrReviewLoopStartIdentity.Error()},
		{name: "independent caller kind mismatch", mutateEnvelope: func(e *retainedStartEnvelope) { e.Source = models.ReviewLoopSourceAutomation }, restoreError: db.ErrReviewLoopStartIdentity.Error()},
		{name: "independent automation caller mismatch", form: models.ReviewLoopSourceAutomation, mutateEnvelope: func(e *retainedStartEnvelope) { id := uuid.New(); e.AutomationRunID = &id }, restoreError: db.ErrReviewLoopStartIdentity.Error()},
		{name: "independent publication caller mismatch", form: models.ReviewLoopSourcePublication, mutateEnvelope: func(e *retainedStartEnvelope) { id := uuid.New(); e.ChangesetID = &id }, restoreError: db.ErrReviewLoopStartIdentity.Error()},
		{name: "unsupported envelope version", mutateEnvelope: func(e *retainedStartEnvelope) { e.Version = 2 }, restoreError: db.ErrReviewLoopStartIdentity.Error()},
		{name: "first pass index mismatch", mutateEnvelope: func(e *retainedStartEnvelope) { e.PassIndex = 2 }, restoreError: db.ErrReviewLoopStartIdentity.Error()},
		{name: "first pass status mismatch", mutateEnvelope: func(e *retainedStartEnvelope) { e.PassStatus = models.ReviewLoopPassStatusFixing }, restoreError: db.ErrReviewLoopStartIdentity.Error()},
		{name: "reviewer and thread agent mismatch", mutateEnvelope: func(e *retainedStartEnvelope) { e.Thread.AgentType = models.AgentTypeCodex }, restoreError: db.ErrReviewLoopStartIdentity.Error()},
		{name: "unsupported effective agent", mutateEnvelope: func(e *retainedStartEnvelope) {
			e.AgentType = "synthetic-unsupported"
			e.Thread.AgentType = e.AgentType
		}, restoreError: db.ErrReviewLoopStartIdentity.Error()},
		{name: "empty frozen checkpoint", mutateEnvelope: func(e *retainedStartEnvelope) { e.Checkpoint = "" }, restoreError: db.ErrReviewLoopStartIdentity.Error()},
		{name: "unsupported fix mode", mutateEnvelope: func(e *retainedStartEnvelope) { e.FixMode = "synthetic-unsupported" }, restoreError: db.ErrReviewLoopStartIdentity.Error()},
		{name: "invalid frozen pass budget", mutateEnvelope: func(e *retainedStartEnvelope) { e.MaxPasses = MaxReviewPasses + 1 }, restoreError: db.ErrReviewLoopStartIdentity.Error()},
		{name: "independent envelope digest mismatch", mutatePreparation: func(_ *testing.T, p *db.ReviewLoopStartPreparation) { p.EnvelopeDigest = strings.Repeat("a", 64) }, restoreError: db.ErrReviewLoopStartIdentity.Error()},
		{name: "independent startup digest mismatch", mutatePreparation: func(_ *testing.T, p *db.ReviewLoopStartPreparation) { p.StartupDigest = strings.Repeat("b", 64) }, restoreError: "caller send command semantic digest mismatch"},
		{name: "unknown frozen envelope field", mutatePreparation: func(t *testing.T, p *db.ReviewLoopStartPreparation) {
			var object map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(p.Envelope, &object), "read canonical envelope before one field corruption")
			object["synthetic_unknown"] = json.RawMessage(`true`)
			raw, err := json.Marshal(object)
			require.NoError(t, err, "encode unknown-field envelope without altering known fields")
			p.Envelope = raw
			digest := sha256.Sum256(append([]byte("143/review-loop-start-envelope/v1\x00"), raw...))
			p.EnvelopeDigest = hex.EncodeToString(digest[:])
		}, restoreError: db.ErrReviewLoopStartIdentity.Error()},
		{name: "frozen startup differs from independently retained command", mutateEnvelope: func(e *retainedStartEnvelope) {
			e.Startup = []byte(strings.Replace(string(e.Startup), "Synthetic frozen review", "Synthetic alternate review", 1))
		}, restoreError: db.ErrReviewLoopStartIdentity.Error()},
		{name: "startup command semantic content mismatch", mutatePreparation: func(t *testing.T, p *db.ReviewLoopStartPreparation) {
			var input map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(p.Input, &input), "read original command before isolated semantic corruption")
			input["message"] = json.RawMessage(`"Synthetic changed command"`)
			raw, err := json.Marshal(input)
			require.NoError(t, err, "preserve scope and shape while changing command content")
			p.Input = raw
		}, restoreError: "caller send command semantic digest mismatch"},
		{name: "startup command scope rejected by actual SQL guard", mutatePreparation: func(t *testing.T, p *db.ReviewLoopStartPreparation) {
			var input map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(p.Input, &input), "read original command before isolated thread corruption")
			rawID, err := json.Marshal(uuid.New())
			require.NoError(t, err, "encode a distinct synthetic command thread")
			input["thread_id"] = rawID
			raw, err := json.Marshal(input)
			require.NoError(t, err, "preserve command fields except independently bound thread")
			p.Input = raw
		}, prepareCode: "23514"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			pool := testpostgres.NewPool(t, ctx, "../../../migrations", 2)
			form := tt.form
			if form == "" {
				form = models.ReviewLoopSourceManual
			}
			envelope, preparation, command := newRetainedStartCodecFixture(t, ctx, pool, form, tt.present)
			if tt.mutateEnvelope != nil {
				tt.mutateEnvelope(&envelope)
			}
			var err error
			preparation.Envelope, preparation.EnvelopeDigest, err = encodeRetainedStart(envelope)
			require.NoError(t, err, "corruption cases must remain well-encoded, bounded JSON with a freshly computed envelope digest")
			if tt.mutatePreparation != nil {
				tt.mutatePreparation(t, &preparation)
			}
			transactions, err := db.NewThreadSendTransactions(pool)
			require.NoError(t, err, "use production bounded transaction ownership for T0 and the fresh recovery read")
			orgID := command.SendInput().OrgID
			err = transactions.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
				_, err := db.PrepareReviewLoopStartInTx(ctx, orgID, tx, preparation)
				return err
			})
			if tt.prepareCode != "" {
				var pgErr *pgconn.PgError
				require.ErrorAs(t, err, &pgErr, "actual schema must reject malformed independent command scope before any T0 commit")
				require.Equal(t, tt.prepareCode, pgErr.Code, "the real retained-input trigger must reject the intended scope corruption")
				requireRetainedCodecGraph(t, ctx, pool, orgID, [5]int{0, 0, 0, 0, 0})
				return
			}
			require.NoError(t, err, "semantically corrupt envelope remains SQL-shaped so restore must validate it independently")
			var stored db.ReviewLoopStartIntent
			var found bool
			err = transactions.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
				var err error
				stored, found, err = db.ReadReviewLoopStartInTx(ctx, orgID, preparation.Scope.SessionID, preparation.Scope.ID, tx)
				return err
			})
			require.NoError(t, err, "read actual committed PostgreSQL JSONB through the bounded recovery accessor")
			require.True(t, found, "fresh recovery must find the committed original identity")
			require.NotEqual(t, preparation.Envelope, stored.Preparation().Envelope, "test must traverse PostgreSQL JSONB presentation rather than decode the original compact bytes")
			restored, restoredCommand, err := restoreRetainedStart(stored)
			if tt.restoreError != "" {
				require.EqualError(t, err, tt.restoreError, "restoration must reject the isolated semantic or independent-identity corruption")
				runtime := &retainedStartTestRuntime{pool: pool}
				_, recoverErr := NewService(db.NewSessionReviewLoopStore(pool), runtime, WithStartTransactions(transactions)).RecoverStart(ctx, orgID, preparation.Scope.SessionID, preparation.Scope.ID)
				require.EqualError(t, recoverErr, tt.restoreError, "malformed retained preparation remains a visible recovery error without a fresh command")
				var state string
				var rescheduled bool
				require.NoError(t, pool.QueryRow(ctx, `SELECT state,due_at=updated_at+interval '5 seconds' AND due_at>created_at+interval '4 seconds'
 FROM review_loop_start_intents WHERE org_id=$1 AND session_id=$2 AND id=$3`, orgID, preparation.Scope.SessionID, preparation.Scope.ID).Scan(&state, &rescheduled), "read exact prepared metadata after failed recovery")
				require.Equal(t, "prepared", state, "retry scheduling never infers creation or send abort from malformed evidence")
				require.True(t, rescheduled, "one malformed oldest original must yield its next check so later prepared originals can progress")
				require.Equal(t, [2]int{0, 0}, [2]int{runtime.creates, runtime.sends}, "malformed preparation cannot create a replacement thread or send")
			} else {
				require.NoError(t, err, "valid complete frozen envelope must survive actual PostgreSQL JSONB presentation")
				canonical, digest, err := encodeRetainedStart(restored)
				require.NoError(t, err, "restored envelope must preserve the full canonical representation")
				require.Equal(t, []byte(preparation.Envelope), canonical, "every frozen field, optional presence, collection order and integer must round-trip exactly")
				require.Equal(t, preparation.EnvelopeDigest, digest, "JSONB whitespace and field order cannot alter the original envelope digest")
				expectedLoop := models.SessionReviewLoop{
					ID: envelope.LoopID, OrgID: envelope.OrgID, SessionID: envelope.SessionID, ThreadID: &envelope.Thread.ID,
					Source: envelope.Source, AutomationRunID: envelope.AutomationRunID, ChangesetID: envelope.ChangesetID,
					WorkspaceRevision: envelope.WorkspaceRevision, DesiredHeadSHA: envelope.DesiredHeadSHA,
					AgentType: envelope.AgentType, MaxPasses: envelope.MaxPasses, FixMode: envelope.FixMode,
					ReviewRequired: envelope.ReviewRequired, StartedByUserID: envelope.StartedByUserID,
					Status: models.ReviewLoopStatusRunning, LoopStartCheckpointKey: &envelope.Checkpoint,
					LatestCheckpointKey: &envelope.Checkpoint,
				}
				require.Equal(t, expectedLoop, restored.loop(), "loop construction must independently copy every frozen insert-time field")
				require.Equal(t, command.SendInput(), restoredCommand.SendInput(), "all effective command inputs and optional presence must round-trip without rerendering")
				expectedCommand, err := command.Encode()
				require.NoError(t, err, "encode original complete command")
				actualCommand, err := restoredCommand.Encode()
				require.NoError(t, err, "encode actual restored command")
				require.Equal(t, expectedCommand, actualCommand, "restored codec must preserve exact canonical command bytes")
				require.Equal(t, command.Digest(), restoredCommand.Digest(), "the complete original command digest must survive storage")
				var kinds [2]string
				var lengths [2]int
				require.NoError(t, pool.QueryRow(ctx, `SELECT pg_typeof(envelope_digest)::text,pg_typeof(startup_digest)::text,octet_length(envelope_digest),octet_length(startup_digest) FROM review_loop_start_intents WHERE org_id=$1 AND id=$2`, orgID, preparation.Scope.ID).Scan(&kinds[0], &kinds[1], &lengths[0], &lengths[1]), "inspect actual stored digest types and widths")
				require.Equal(t, [2]string{"text", "text"}, kinds, "both retained digests use the approved TEXT representation")
				require.Equal(t, [2]int{64, 64}, lengths, "both retained digests preserve exactly sixty-four ASCII hexadecimal bytes")
			}
			requireRetainedCodecGraph(t, ctx, pool, orgID, [5]int{1, 0, 0, 0, 1})
		})
	}
}

func newRetainedStartCodecFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, source models.ReviewLoopSource, present bool) (retainedStartEnvelope, db.ReviewLoopStartPreparation, threadsvc.CallerSendCommand) {
	t.Helper()
	orgID, session, publication := retainedPublicationFixture(t, ctx, pool)
	loopID, passID, threadID := uuid.New(), uuid.New(), uuid.New()
	scope := db.ReviewLoopStartScope{ID: loopID, SessionID: session.ID, Source: source}
	envelope := retainedStartEnvelope{Version: 1, OrgID: orgID, SessionID: session.ID, LoopID: loopID, PassID: passID,
		PassIndex: 1, PassStatus: models.ReviewLoopPassStatusReviewing, Source: source, AgentType: models.AgentTypeClaudeCode,
		MaxPasses: 3, FixMode: models.ReviewLoopFixModeMinimal, Checkpoint: "synthetic/codec-checkpoint",
		Thread: retainedStartThread{ID: threadID, AgentType: models.AgentTypeClaudeCode, Label: "Synthetic retained reviewer"}}
	input := threadsvc.SendMessageInput{OrgID: orgID, SessionID: session.ID, ThreadID: threadID, Message: "Synthetic frozen review\nPreserve Unicode: λ and exact whitespace.  "}
	if source == models.ReviewLoopSourcePublication {
		scope.ChangesetID, envelope.ChangesetID = &publication.ChangesetID, &publication.ChangesetID
		revision, head := int64(9223372036854775807), "synthetic-frozen-head"
		envelope.WorkspaceRevision, envelope.DesiredHeadSHA, envelope.ReviewRequired = &revision, &head, true
		input.ChangesetID = &publication.ChangesetID
	}
	if source == models.ReviewLoopSourceAutomation {
		automationID, runID := uuid.New(), uuid.New()
		_, err := pool.Exec(ctx, `INSERT INTO automations(id,org_id,name,goal) VALUES($1,$2,'Synthetic codec automation','Preserve original startup')`, automationID, orgID)
		require.NoError(t, err, "create legal owned automation caller")
		_, err = pool.Exec(ctx, `INSERT INTO automation_runs(id,org_id,automation_id,goal_snapshot,session_id,triggered_by) VALUES($1,$2,$3,'Synthetic codec original',$4,'manual')`, runID, orgID, automationID, session.ID)
		require.NoError(t, err, "create legal exact-session automation run")
		scope.AutomationRunID, envelope.AutomationRunID = &runID, &runID
	}
	if present {
		userID, model, continuation := uuid.New(), "synthetic-model-v1", ""
		_, err := pool.Exec(ctx, `INSERT INTO users(id,org_id,email,name,role) VALUES($1,$2,$3,'Synthetic codec user','member')`, userID, orgID, userID.String()+"@example.invalid")
		require.NoError(t, err, "create a real owned user for present optional command identity")
		envelope.StartedByUserID, envelope.Thread.Model = &userID, &model
		input.UserID, input.ContinuationDedupeKeyOverride = &userID, &continuation
		input.Images = []string{"data:image/png;base64,c3ludGhldGlj"}
		input.References = models.SessionInputReferences{{Kind: models.SessionInputReferenceKindFile, Token: "@synthetic.go", Path: "synthetic.go", Display: "Synthetic file"}}
		input.Commands = models.SessionInputCommands{{Kind: "command", AgentType: models.AgentTypeClaudeCode, Name: "review", Token: "/review", Display: "/review", Arguments: "Synthetic frozen arguments", Source: models.SessionInputCommandSourceBuiltin}}
		input.PlanMode = true
	}
	command, err := threadsvc.NewCallerSendCommand(threadsvc.CallerSendReviewLoopInitialSend, loopID, input)
	require.NoError(t, err, "construct semantically valid actual startup command before any intended corruption")
	raw, err := command.Encode()
	require.NoError(t, err, "encode complete actual startup command")
	envelope.Startup = raw
	preparation := db.ReviewLoopStartPreparation{Scope: scope, PassID: passID, ThreadID: threadID, Input: raw, StartupDigest: command.Digest()}
	return envelope, preparation, command
}

func requireRetainedCodecGraph(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgID uuid.UUID, expected [5]int) {
	t.Helper()
	var graph [5]int
	require.NoError(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM review_loop_start_intents WHERE org_id=$1),(SELECT count(*) FROM session_review_loops WHERE org_id=$1),(SELECT count(*) FROM session_review_loop_passes WHERE org_id=$1),(SELECT count(*) FROM session_messages WHERE org_id=$1),(SELECT count(*) FROM jobs WHERE org_id=$1)`, orgID).Scan(&graph[0], &graph[1], &graph[2], &graph[3], &graph[4]), "inspect the complete retained creation graph after storage and decoding")
	require.Equal(t, expected, graph, "codec tests may install only the original T0 and wake, never a thread command, loop or pass")
}
