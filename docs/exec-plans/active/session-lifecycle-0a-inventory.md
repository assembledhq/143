# Session lifecycle 0a: root ownership and tracing appendix

Planning assignment only. See [the 0a delivery plan](session-lifecycle-0a.md) for dependencies, scope and exits. This mapping grants no implementation, publication, release or activation authority. Primary ownership is accountability for converting or retiring the whole named orchestration root; it is not permission to implement other packages' canonical predicates.

Source: repository baseline `0323c7e8e7b71149cd85f23add4fc136edd79467`; inspected equivalent-content checkout `6381af14e9fe68f542366a0c2c1afc708465372c`. Inventory version 1; SHA-256 `143cb9bd5ae327160eab9fea281acbbb8ede66f624d4824957366bd205718bdc`.

Exact scoped counts: 255 distinct reviewed unfenced roots with `canonical_target`; 353 distinct unfenced syntax-only candidates; 608 total. This does not reclassify the other 56 inventory entries or excuse their compatibility obligations. The separate 123-site job-status inventory belongs solely to A.2; references here do not split that authority.

## Ownership rules

- Each reviewed root below has exactly one primary caller/increment owner, even if its `canonical_target` names several shared operations. Whole-root completion requires every nested/shared branch's call-through evidence, not only the primary branch.
- Shared primitives are implemented once by the owner in the operation table. A caller owner wires them in its existing concrete transaction and retains all transaction/lock/effect obligations; ownership never follows the first word in `canonical_target` automatically.
- A.0 freezes common admission/execution schema and retention/deletion before a producer. A.2's core solely installs shared dispatch/consumer schema (including required job/runtime references/indexes) and status/check/index/lookup/collision policy before A.1 consumes it. Its complete 123-site status-policy proof gates E's first parked row; remaining real consumer joins follow family conversion. B.2 implements the first stage and strict defer result of one AuthorizeRuntimeStartInTx; E extends that SAME operation for final authorization/every Execute.
- C.0 freezes descriptor/upload/certificate/target-observation interfaces. Build D.1r registration before C.1p, then C.1w and D.1c publication callers; D.2 consumes the certificate transitions. E.1's actual verification receipt precedes C.2. G.1's protocol core precedes the first A.1 router. Retain joined caller exits, not circular whole-package build prerequisites.
- F.1 owns the shared typed multi-mode recovery operation; its public pending-only command is one adapter, not the definition of all modes. B.2 owns live continuation adapters; F.2 owns failed-only and physical-recovery adapters. F.3 implements/tests offline normalization before release; only H executes it after floor/drain. G.0 audits tracing, never unassigned semantic work.
- B.1p/o/q/a/r, B.3c/d/t and C.1p/w/v are finite review tranches inside the existing increment, named by module responsibility; they are not additional safety contracts or a promised number of PRs.
- Legacy compatibility wrappers may be retired instead of converted when every real caller is covered and tests prove that removal; residual wrappers must delegate the same canonical operation and cannot keep active writes through old SQL.
- New discovered roots inherit their semantic family, receive an explicit ID/owner and real caller evidence before publication. No row is complete merely because it has an owner.

## Increment legend and exact counts

| Owner / review tranche | Responsibility | Reviewed roots | Syntax candidates to trace |
| --- | --- | ---: | ---: |
| A.0 | Shared admission/execution schema, deletion/retention and immutable-binding freeze (new roots; no existing reviewed root assigned) | 0 | 0 |
| A.1 | Ordinary thread-lane admission consuming A.2 schema/core, plus old compensations/private scope prerequisites | 11 | 9 |
| A.2 | Sole shared dispatch/consumer schema and status core before A.1; complete 123-site policy proof before first parked row; full consumer joins at family exits | 0 | 0 |
| A.3 | Exact answer/denial and request-keyed approved-grant operations | 15 | 8 |
| A.4 | Session/review-comment/integration/initial admission adapters | 6 | 36 |
| A.5 | Review/automation/repair/feedback admission owners | 6 | 50 |
| A.6 | Queue drain, inbox replay and pending-admission reconciliation | 3 | 5 |
| B.1p | B.1 shared session/thread row-persistence and canonical settlement implementation tranche | 30 | 1 |
| B.1o | B.1 ordinary runtime/activity-phase/reaper settlement tranche | 14 | 12 |
| B.1q | Full worker/executor reclaim roots, their ordered-unit conversion and dead-letter failure tranche; reclaim closure consumes B.2 continuation fragment | 9 | 2 |
| B.1a | B.1 automation settlement and outer-owner facade tranche | 13 | 16 |
| B.1r | B.1 review/publication settlement and scheduling-owner tranche | 17 | 40 |
| B.2 | Single first-stage AuthorizeRuntimeStartInTx plus seed/live/ack/defer, recheck Claim, live continuation fragment and retry/runtime adapter composition | 30 | 30 |
| B.3c | B.3 strict end/cancel-intent and cancellation-delivery tranche | 12 | 25 |
| B.3d | B.3 retained review/automation/repair owner/reservation tranche | 7 | 23 |
| B.3t | B.3 topology/create/archive/unarchive orchestration tranche | 6 | 11 |
| C.0 | Shared descriptor/upload/certificate/RetentionAuthority and exact-target ResumabilityReceipt interface freeze | 0 | 0 |
| C.2 | Selected context revalidation and typed readiness outcomes after C.1p policy and E.1 verification | 0 | 0 |
| C.1p | C.1 request producer/final-pause/checkpoint/readiness publication tranche | 9 | 2 |
| C.1w | C.1 shared workspace revision/holder/materialization/physical-operation authority tranche | 30 | 14 |
| C.1v | C.1 preview reservation/runtime-revision/physical-operation tranche | 10 | 38 |
| D.1r | New upload registration/receipt and exclusive cleanup-kind routing core, before C.1p | 0 | 0 |
| D.1c | Save/Set/Promote/Clear and publication caller closure after C.1p/w | 11 | 8 |
| D.2 | Current-reference expiry/archive/merged-PR cleanup and availability tombstone | 4 | 5 |
| D.3 | Bounded registered superseded/orphan exact-key cleanup and deletion settlement | 1 | 0 |
| E.1 | Same start primitive final authorization/every Execute and exact provider/target verification | 0 | 5 |
| E.2 | Durable parking, retry/abandon and crash control (new operations) | 0 | 0 |
| E.3 | Reachable start-control surfaces and tested provider/build/image routing (new surfaces) | 0 | 0 |
| F.1 | Sole multi-mode RecoveryAdmission primitive and pending-only public recovery action | 0 | 0 |
| F.2 | Recovery physical reservation/acknowledgement and existing failed-only retry adapter fencing | 6 | 5 |
| F.3 | Offline legacy exact-provenance normalization/resolution only; H executes after floor | 0 | 0 |
| G.0 | Syntax tracing audit and closure ledger (audit owner, never the semantic implementation dump) | 0 | 0 |
| G.1 | Early cutover-observer protocol core, then minimum-writer guards and claimant compatibility | 5 | 3 |
| G.2 | Required constructors and positively identified residual heartbeat/maintenance/operator wiring | 0 | 5 |
| H.0 / H | New activation controller and assembled assurance; separate authorized release/normalization/activation (no existing semantic root assigned) | 0 | 0 |
| **Total** | Inventory scope above | **255** | **353** |

## Shared-operation implementation owner

| Shared operation | Sole implementing owner / extension contract |
| --- | --- |
| `AdmitMessageInTx` | A.1 |
| `HasCompetingInputConsumerInTx` | A.2 |
| `AdmitAnswerInTx` | A.3 |
| `AdmitReviewTurnInTx` | A.5 |
| `AdmitAutomationTurnInTx` | A.5 |
| `AdmitRepairTurnInTx` | A.5 |
| `AdmitFeedbackTurnInTx` | A.5 |
| `AppendApprovedGrantInTx` | A.3 |
| `AuthorizeRuntimeStartInTx` | B.2 first stage; E extends the SAME primitive for final authorization |
| `DeferRuntimeStartInTx` | B.2 |
| `MarkProviderLaunchInTx` | E.1 |
| `RecordStartPreparationOutcomeInTx` | E.2 |
| `RetryUnlaunchedStartInTx` / `AbandonUnlaunchedIntentInTx` | E.2, with E.3 surfaces |
| `SupersedeUnlaunchedIntentInTx` | B.3c; B.3t and F compose it with their exact current transition authority |
| `ClaimSeedInTx` | B.2 |
| `ClaimLiveInputInTx` | B.2 |
| `AcknowledgeInputInTx` | B.2 |
| `ProjectTurnInTx` | B.1p shared implementation; B.1o/q/a/r own caller tranches |
| `SettleLostAttemptInTx` | B.1p shared implementation; B.1o/q/a/r own caller tranches |
| `ProjectAdmissionFailureInTx` | B.1q (with shared B.1p persistence) |
| `EndSessionInTx` | B.3c |
| `EndThreadInTx` | B.3c |
| `RecordCancelIntentInTx` | B.3c |
| `ChangeReviewOwnerInTx` | B.3d |
| `ChangeAutomationOwnerInTx` | B.3d |
| `ChangeRepairReservationInTx` | B.3d |
| `ArchiveThreadInTx` | B.3t |
| `CreateThreadInTx` | B.3t |
| `PublishCheckpointInTx` | C.1p via C.0 descriptor/receipt interface |
| `MutateWorkspaceRevisionInTx` | C.1w via C.0 descriptor/reservation interface |
| `ActivateRequestReadinessInTx` | C.1p; F action and H atomic activation remain prerequisites |
| `AdvanceInputWorkspaceCompatibilityInTx` | C.1p via C.0 certificate contract |
| `RevalidateInputContextInTx` | C.2 |
| `ExpireWorkspaceArtifactsInTx` | D.2 via C.0 RetentionAuthority/availability contract |
| `RegisterSnapshotUploadInTx` | D.1r; D.1c callers consume this receipt with C.1p/w |
| `TombstoneSnapshotObjectInTx` | D.3 |
| `SettleSnapshotDeletionInTx` | D.3 |
| `AdmitInputRecoveryInTx` | F.1 owns the one sealed multi-mode implementation; B.2 live continuation and F.2 failed-only adapters compose the applicable branch with distinct eligibility/consent, never another predicate |
| `ReserveWorkspaceRecoveryInTx` | F.2 |
| `SettleWorkspaceRecoveryInTx` | F.2 |

Recovery mode ownership is explicit: F.1 implements the shared durable checks and
validated mode dispatch; its public RecoverPendingInput adapter alone is pending-
only. B.2 supplies live continuation prior-execution/input/attempt proof, F.2
supplies failed-only checkpoint/start-over and physical recovery proof, and F.1
supplies pending-request acknowledgement/consent. All modes retain their own
contract eligibility and exact identity. Accepted-undelivered answer/denial retry
is E's start-control command, not one of these pending-recovery adapters. F.3 is
offline normalization, never a live execution prerequisite deferred past the floor.

All root rows retain the source inventory's full `canonical_target`, authority, migration and fence outcome. The abbreviated operation column is navigation only and does not waive any of those fields.

## Reviewed roots: exact primary-owner assignment

### A.1: 11 reviewed roots

| Exact inventory root | Primary caller owner | Required shared call-through / special completion rule |
| --- | --- | --- |
| `internal/db/session_lifecycle_fences.go:lockLifecycleScopeInTx` | A.1 | A.1 owns reuse in first canonical admission; B.3 separately proves stronger exclusion. No mutation authority inferred. |
| `internal/db/session_lifecycle_lane_claim.go:verifyLifecycleLaneClaimInTx` | A.1 | A.1 owns reuse in first complete claim composition; B.3 release must compose complete authority/drain checks, never elevate receipt equality. |
| `internal/db/session_thread_store.go:SessionThreadStore.ClaimForResumeInSession` | A.1 | AdmitMessageInTx, AdmitAnswerInTx |
| `internal/db/session_thread_store.go:SessionThreadStore.claimForSession` | A.1 | AdmitMessageInTx |
| `internal/db/session_thread_store.go:SessionThreadStore.ClaimIdle` | A.1 | AdmitMessageInTx |
| `internal/db/session_thread_store.go:SessionThreadStore.ClaimIdleForSession` | A.1 | AdmitMessageInTx, AdmitAnswerInTx |
| `internal/services/thread/service.go:Service.claimSessionForSend` | A.1 | AdmitMessageInTx |
| `internal/services/thread/service.go:Service.claimThreadForSend` | A.1 | AdmitMessageInTx |
| `internal/services/thread/service.go:Service.releaseThread` | A.1 | A.1 removes old unreceipted compensation via admission atomicity; retained exact compensation calls B.1, not a copied status reset. |
| `internal/services/thread/service.go:Service.revertAfterSendFailure` | A.1 | A.1 removes old unreceipted compensation via admission atomicity; retained exact compensation calls B.1. |
| `internal/services/thread/service.go:Service.SendMessage` | A.1 | A.1 owns the real thread caller; A.3 owns its answer operation, A.2 its complete consumer/status contract, B.2 its execution handoff. |

### A.3: 15 reviewed roots

| Exact inventory root | Primary caller owner | Required shared call-through / special completion rule |
| --- | --- | --- |
| `internal/api/handlers/sessions.go:SessionHandler.AnswerQuestion` | A.3 | Retain the separate legacy answer-only command; no provider identity, grant or continuation is fabricated. Only an actually resuming path composes canonical answer admission. |
| `internal/db/agent_capabilities.go:AgentCapabilityPolicyStore.AppendApprovedSessionGrant` | A.3 | AppendApprovedGrantInTx |
| `internal/db/agent_capabilities.go:AgentCapabilityPolicyStore.AppendApprovedSessionGrantInTx` | A.3 | AppendApprovedGrantInTx |
| `internal/db/session_human_input_requests.go:SessionHumanInputRequestStore.AnswerLatestPendingFreeTextBySession` | A.3 | AdmitAnswerInTx |
| `internal/db/session_human_input_requests.go:SessionHumanInputRequestStore.AnswerLatestPendingFreeTextByThread` | A.3 | AdmitAnswerInTx |
| `internal/db/session_human_input_requests.go:SessionHumanInputRequestStore.AnswerPending` | A.3 | AdmitAnswerInTx |
| `internal/db/session_human_input_requests.go:SessionHumanInputRequestStore.CancelPending` | A.3 | AdmitAnswerInTx |
| `internal/services/agentcapabilities/service.go:Service.ApplyApprovedGrant` | A.3 | AppendApprovedGrantInTx |
| `internal/services/humaninput/db_repository.go:dbTx.AnswerRequest` | A.3 | AdmitAnswerInTx |
| `internal/services/humaninput/db_repository.go:dbTx.AppendApprovedCapabilityGrant` | A.3 | AppendApprovedGrantInTx |
| `internal/services/humaninput/db_repository.go:dbTx.CancelRequest` | A.3 | AdmitAnswerInTx |
| `internal/services/humaninput/db_repository.go:dbTx.ClaimSessionForResume` | A.3 | AdmitAnswerInTx |
| `internal/services/humaninput/db_repository.go:dbTx.ClaimThreadForResume` | A.3 | AdmitAnswerInTx |
| `internal/services/humaninput/service.go:Service.Answer` | A.3 | AdmitAnswerInTx, AppendApprovedGrantInTx |
| `internal/services/humaninput/service.go:Service.applyApprovedCapabilityGrant` | A.3 | AdmitAnswerInTx, AppendApprovedGrantInTx |

### A.4: 6 reviewed roots

| Exact inventory root | Primary caller owner | Required shared call-through / special completion rule |
| --- | --- | --- |
| `internal/api/handlers/session_review_comments.go:SessionReviewCommentHandler.SendToAgent` | A.4 | AdmitMessageInTx, SettleLostAttemptInTx |
| `internal/api/handlers/sessions.go:SessionHandler.SendMessage` | A.4 | A.4 owns the handler; exact answer branch calls A.3, never a second answer predicate. |
| `internal/db/session_store.go:SessionStore.ClaimForResume` | A.4 | A.4 owns retirement/delegation of this legacy session wrapper; A.4 threadless/sibling characterization gate remains mandatory. |
| `internal/db/session_store.go:SessionStore.ClaimIdle` | A.4 | A.4 owns retirement/delegation of this legacy session wrapper. A.1 cannot keep using it as a second parent claim for threaded admission. |
| `internal/services/linear/create_path.go:Service.MarkLinearPrepareFailedWithError` | A.4 | SettleLostAttemptInTx |
| `internal/worker/handlers_linear_agent_prompted.go:handleLinearAgentPrompted` | A.4 | AdmitMessageInTx, SettleLostAttemptInTx |

### A.5: 6 reviewed roots

| Exact inventory root | Primary caller owner | Required shared call-through / special completion rule |
| --- | --- | --- |
| `internal/db/automation_dispatch.go:SessionStore.ClaimForAutomationTurn` | A.5 | AdmitAutomationTurnInTx |
| `internal/db/automation_dispatch.go:SessionThreadStore.ClaimPrimaryForAutomationTurn` | A.5 | AdmitAutomationTurnInTx |
| `internal/db/pull_request_feedback.go:PullRequestFeedbackStore.QueueContinuation` | A.5 | AdmitFeedbackTurnInTx |
| `internal/services/codereview/scheduling.go:scheduledSessionStore.Create` | A.5 | AdmitReviewTurnInTx |
| `internal/services/github/pr_health_service.go:PRService.resumeRepairSession` | A.5 | A.5 owns repair admission orchestration; B.3d owns reservation and C.1w workspace mutation. |
| `internal/worker/code_review_handler.go:ensureCodeReviewOrchestratorThread` | A.5 | AdmitReviewTurnInTx, ProjectTurnInTx |

### A.6: 3 reviewed roots

| Exact inventory root | Primary caller owner | Required shared call-through / special completion rule |
| --- | --- | --- |
| `internal/db/session_thread_store.go:SessionThreadStore.ClaimNextQueuedForSession` | A.6 | AdmitMessageInTx |
| `internal/services/agent/orchestrator.go:Orchestrator.admitNextQueuedThread` | A.6 | AdmitMessageInTx, SettleLostAttemptInTx |
| `internal/worker/handlers.go:answerQueuedHumanInputForContinue` | A.6 | A.6 owns retirement of worker latest-pending selection and exact drain/retry binding; mutation calls the one A.3 answer operation. |

### B.1p: 30 reviewed roots

| Exact inventory root | Primary caller owner | Required shared call-through / special completion rule |
| --- | --- | --- |
| `internal/db/session_result_persistence.go:SessionStore.UpdateResultAndCompleteActivityPhaseInTx` | B.1p | ProjectTurnInTx, SettleLostAttemptInTx |
| `internal/db/session_result_persistence.go:SessionStore.UpdateResultInTx` | B.1p | ProjectTurnInTx, SettleLostAttemptInTx |
| `internal/db/session_result_persistence.go:SessionStore.UpdateStatusAndCompleteActivityPhaseInTx` | B.1p | ProjectTurnInTx, SettleLostAttemptInTx |
| `internal/db/session_result_persistence.go:SessionStore.UpdateStatusInTx` | B.1p | ProjectTurnInTx, SettleLostAttemptInTx |
| `internal/db/session_result_persistence.go:SessionStore.UpdateTurnCompleteAndCompleteActivityPhaseInTx` | B.1p | ProjectTurnInTx, SettleLostAttemptInTx |
| `internal/db/session_result_persistence.go:SessionStore.UpdateTurnCompleteInTx` | B.1p | ProjectTurnInTx, SettleLostAttemptInTx |
| `internal/db/session_store.go:SessionStore.MarkRunningWithSandboxState` | B.1p | B.1p owns atomic sibling projection; shared current availability calls C.1w, never a second start permit. |
| `internal/db/session_store.go:SessionStore.UpdateRecoveryState` | B.1p | ProjectTurnInTx |
| `internal/db/session_store.go:SessionStore.UpdateResult` | B.1p | ProjectTurnInTx, SettleLostAttemptInTx |
| `internal/db/session_store.go:SessionStore.UpdateResultAndCompleteActivityPhase` | B.1p | ProjectTurnInTx, SettleLostAttemptInTx |
| `internal/db/session_store.go:SessionStore.updateResultRow` | B.1p | ProjectTurnInTx |
| `internal/db/session_store.go:SessionStore.UpdateStatusAndCompleteActivityPhase` | B.1p | ProjectTurnInTx, SettleLostAttemptInTx |
| `internal/db/session_store.go:SessionStore.updateStatusRow` | B.1p | ProjectTurnInTx |
| `internal/db/session_store.go:SessionStore.UpdateTurnComplete` | B.1p | ProjectTurnInTx, SettleLostAttemptInTx |
| `internal/db/session_store.go:SessionStore.UpdateTurnCompleteAndCompleteActivityPhase` | B.1p | ProjectTurnInTx, SettleLostAttemptInTx |
| `internal/db/session_store.go:SessionStore.updateTurnCompleteRow` | B.1p | ProjectTurnInTx |
| `internal/db/session_thread_persistence.go:SessionThreadStore.CompleteTurnInTx` | B.1p | ProjectTurnInTx |
| `internal/db/session_thread_persistence.go:SessionThreadStore.UpdateResultInTx` | B.1p | ProjectTurnInTx |
| `internal/db/session_thread_persistence.go:SessionThreadStore.UpdateStatusInTx` | B.1p | ProjectTurnInTx |
| `internal/db/session_thread_persistence.go:SessionThreadStore.UpdateTurnCompleteInTx` | B.1p | ProjectTurnInTx |
| `internal/db/session_thread_store.go:SessionThreadStore.CompleteTurn` | B.1p | ProjectTurnInTx |
| `internal/db/session_thread_store.go:SessionThreadStore.completeTurnRow` | B.1p | ProjectTurnInTx |
| `internal/db/session_thread_store.go:SessionThreadStore.FailRunningBySession` | B.1p | SettleLostAttemptInTx, ProjectTurnInTx |
| `internal/db/session_thread_store.go:SessionThreadStore.RecordRecoveryMetadata` | B.1p | ProjectTurnInTx |
| `internal/db/session_thread_store.go:SessionThreadStore.UpdateResult` | B.1p | ProjectTurnInTx |
| `internal/db/session_thread_store.go:SessionThreadStore.updateResultRow` | B.1p | ProjectTurnInTx |
| `internal/db/session_thread_store.go:SessionThreadStore.UpdateStatus` | B.1p | ProjectTurnInTx |
| `internal/db/session_thread_store.go:SessionThreadStore.updateStatusRow` | B.1p | ProjectTurnInTx |
| `internal/db/session_thread_store.go:SessionThreadStore.UpdateTurnComplete` | B.1p | ProjectTurnInTx |
| `internal/db/session_thread_store.go:SessionThreadStore.updateTurnCompleteRow` | B.1p | ProjectTurnInTx |

### B.1o: 14 reviewed roots

| Exact inventory root | Primary caller owner | Required shared call-through / special completion rule |
| --- | --- | --- |
| `internal/api/handlers/sessions.go:SessionHandler.createManual` | B.1o | SettleLostAttemptInTx |
| `internal/services/agent/activity_phase_execution.go:activityPhaseExecution.persistSessionResultAndComplete` | B.1o | ProjectTurnInTx |
| `internal/services/agent/activity_phase_execution.go:activityPhaseExecution.persistSessionStatusAndComplete` | B.1o | ProjectTurnInTx |
| `internal/services/agent/activity_phase_execution.go:activityPhaseExecution.persistTurnCompleteAndComplete` | B.1o | ProjectTurnInTx, SettleLostAttemptInTx |
| `internal/services/agent/orchestrator.go:Orchestrator.failHumanInputPause` | B.1o | ProjectTurnInTx, SettleLostAttemptInTx |
| `internal/services/agent/orchestrator.go:Orchestrator.failNonPrimaryThreadWithResult` | B.1o | ProjectTurnInTx |
| `internal/services/agent/orchestrator.go:Orchestrator.failRunWithResultAndPhaseTerminal` | B.1o | ProjectTurnInTx, SettleLostAttemptInTx |
| `internal/services/agent/orchestrator.go:Orchestrator.finishWorkerOwnershipFailure` | B.1o | SettleLostAttemptInTx |
| `internal/services/agent/orchestrator.go:Orchestrator.handleCancelledSession` | B.1o | PublishCheckpointInTx, ProjectTurnInTx |
| `internal/services/agent/orchestrator.go:Orchestrator.handleSystemInterruptedSession` | B.1o | PublishCheckpointInTx, ProjectTurnInTx, SettleLostAttemptInTx |
| `internal/services/agent/orchestrator.go:Orchestrator.stopWorkspaceWaitIfThreadCancelled` | B.1o | ProjectTurnInTx |
| `internal/services/agent/orchestrator.go:Orchestrator.updatePrimaryThreadTerminal` | B.1o | ProjectTurnInTx |
| `internal/services/agent/reaper.go:SessionReaper.reap` | B.1o | B.1o owns orchestration/terminal settlement; snapshot/availability branches call D.2/C.1w. This root is not fully converted until every branch is covered. |
| `internal/services/agent/thread_runtime_delivery.go:keepSessionRunningIfSiblingRuntimesActive` | B.1o | ProjectTurnInTx, MutateWorkspaceRevisionInTx |

### B.1q: 9 reviewed roots

| Exact inventory root | Primary caller owner | Required shared call-through / special completion rule |
| --- | --- | --- |
| `internal/db/jobs.go:JobStore.ReclaimLostRunningJobs` | B.1q | Own full reclaim/ordered-unit conversion; compose SettleLostAttemptInTx/ProjectTurnInTx and B.2's continuation fragment through F.1's applicable mode before whole-root closure. |
| `internal/db/jobs.go:JobStore.ReclaimLostRunningSessionJobsForSession` | B.1q | Own full reclaim/ordered-unit conversion; compose SettleLostAttemptInTx/ProjectTurnInTx and B.2's continuation fragment through F.1's applicable mode before whole-root closure. |
| `internal/db/session_executor_store.go:SessionExecutorStore.ReclaimLost` | B.1q | Own full reclaim/ordered-unit conversion; compose SettleLostAttemptInTx/ProjectTurnInTx and B.2's continuation fragment through F.1's applicable mode before whole-root closure. |
| `internal/db/session_executor_store.go:SessionExecutorStore.ReclaimLostForSession` | B.1q | Own full reclaim/ordered-unit conversion; compose SettleLostAttemptInTx/ProjectTurnInTx and B.2's continuation fragment through F.1's applicable mode before whole-root closure. |
| `internal/worker/handlers.go:registerConcurrencyLimitDeadLetter` | B.1q | ProjectAdmissionFailureInTx, SettleLostAttemptInTx |
| `internal/worker/handlers.go:registerSandboxCapacityDeadLetter` | B.1q | ProjectAdmissionFailureInTx, SettleLostAttemptInTx |
| `internal/worker/handlers.go:registerSessionCapacityDeadLetter` | B.1q | ProjectAdmissionFailureInTx, SettleLostAttemptInTx |
| `internal/worker/handlers.go:registerStaleSandboxDeadLetter` | B.1q | ProjectAdmissionFailureInTx, SettleLostAttemptInTx |
| `internal/worker/handlers.go:registerSystemInterruptDeadLetter` | B.1q | ProjectAdmissionFailureInTx, SettleLostAttemptInTx |

### B.1a: 13 reviewed roots

| Exact inventory root | Primary caller owner | Required shared call-through / special completion rule |
| --- | --- | --- |
| `internal/db/automation_completion.go:AutomationRunStore.CompleteFromMarker` | B.1a | ProjectTurnInTx, SettleLostAttemptInTx |
| `internal/db/automation_completion.go:AutomationRunStore.FailRetriesExhausted` | B.1a | ProjectTurnInTx, SettleLostAttemptInTx |
| `internal/db/automation_turns.go:AutomationRunStore.CompleteExecutingPreflight` | B.1a | ProjectTurnInTx, SettleLostAttemptInTx |
| `internal/db/automation_turns.go:AutomationRunStore.EndInterruptedAttempt` | B.1a | ProjectTurnInTx, SettleLostAttemptInTx |
| `internal/db/automation_turns.go:AutomationRunStore.setThreadTurnForAttempt` | B.1a | ProjectTurnInTx, SettleLostAttemptInTx |
| `internal/services/agent/automation_publication_intent.go:Orchestrator.persistRunResult` | B.1a | ProjectTurnInTx |
| `internal/services/agent/automation_turn.go:Orchestrator.endAutomationAttempt` | B.1a | ProjectTurnInTx |
| `internal/services/agent/automation_turn.go:Orchestrator.endCancelledAutomationTurn` | B.1a | ProjectTurnInTx, SettleLostAttemptInTx, PublishCheckpointInTx |
| `internal/services/automations/turn_attempt_persistence.go:newTurnAttemptSessions` | B.1a | ProjectTurnInTx |
| `internal/services/automations/turn_attempt_persistence.go:turnAttemptSessions.UpdateResult` | B.1a | ProjectTurnInTx |
| `internal/services/automations/turn_attempt_persistence.go:turnAttemptSessions.UpdateStatus` | B.1a | ProjectTurnInTx |
| `internal/services/automations/turn_attempt_persistence.go:turnAttemptSessions.UpdateTurnComplete` | B.1a | ProjectTurnInTx |
| `internal/services/automations/turn_store.go:TurnStore.EndAttempt` | B.1a | ProjectTurnInTx |

### B.1r: 17 reviewed roots

| Exact inventory root | Primary caller owner | Required shared call-through / special completion rule |
| --- | --- | --- |
| `internal/db/code_review_schedule_assessment.go:CodeReviewScheduleStore.SettleAssessment` | B.1r | ProjectTurnInTx |
| `internal/db/code_review_schedule_assessment.go:CodeReviewScheduleStore.SettleAssessmentInTx` | B.1r | ProjectTurnInTx |
| `internal/db/code_review_schedule_assessment.go:settleAssessmentLockedPR` | B.1r | ProjectTurnInTx |
| `internal/db/code_review_scheduling.go:CodeReviewScheduleStore.WithLockedPR` | B.1r | B.1r owns this retained transaction wrapper; callback identity does not promote arbitrary lifecycle authority. Admissions still call A.5. |
| `internal/db/code_review_scheduling.go:CodeReviewScheduleStore.withLockedPRInTx` | B.1r | B.1r owns same-transaction wrapper composition; callback users keep separate canonical-operation obligations. |
| `internal/db/code_review_terminal_recovery.go:reconcileTerminalReviews` | B.1r | SettleLostAttemptInTx, ProjectTurnInTx |
| `internal/services/agent/orchestrator.go:Orchestrator.failCodeReviewThreadTurn` | B.1r | ProjectTurnInTx |
| `internal/services/codereview/service.go:Service.reconcileStrandedSession` | B.1r | ProjectTurnInTx, SettleLostAttemptInTx |
| `internal/services/codereview/service.go:Service.reconcileUnclaimedReviewSession` | B.1r | ProjectTurnInTx, SettleLostAttemptInTx |
| `internal/services/github/pr_health_service.go:PRService.completeReconciledSessionPublication` | B.1r | ProjectTurnInTx |
| `internal/services/github/pr.go:PRService.completePublicationLocalState` | B.1r | ProjectTurnInTx |
| `internal/services/github/pr.go:PRService.reconcileWebhookPullRequest` | B.1r | ProjectTurnInTx |
| `internal/worker/code_review_assessment.go:completeFullAssessment` | B.1r | ProjectTurnInTx |
| `internal/worker/code_review_assessment.go:reconcileCompletedFullAssessment` | B.1r | ProjectTurnInTx |
| `internal/worker/code_review_handler.go:reconcileCodeReviewSessionCompletion` | B.1r | ProjectTurnInTx |
| `internal/worker/code_review_handler.go:reconcileCodeReviewSessionFailureWithDetails` | B.1r | ProjectTurnInTx |
| `internal/worker/code_review_recheck_handler.go:publishCodeReviewRecheck` | B.1r | ProjectTurnInTx |

### B.2: 30 reviewed roots

| Exact inventory root | Primary caller owner | Required shared call-through / special completion rule |
| --- | --- | --- |
| `internal/db/continuation_recovery.go:continuationRecoveryQuery` | B.2 | Shared live continuation fragment, not ownership of B.1q's full reclaim statements/locking. Preserve characterized version-0 prior-execution semantics; active mode composes F.1's typed recovery operation where required, never generic StartDeferred payload rewrite. |
| `internal/db/continuation_recovery.go:ThreadInboxStore.InspectContinuationRecovery` | B.2 | Live continuation input/attempt proof for F.1's applicable recovery mode; not offline normalization or a second authority. |
| `internal/db/continuation_recovery.go:ThreadInboxStore.ListAppliedRecoveryMessageIDs` | B.2 | Exact prior-applied input proof for live continuation recovery; compose the same canonical mode without replaying superseded input. |
| `internal/db/jobs.go:JobStore.RetryWithLease` | B.2 | AuthorizeRuntimeStartInTx, SettleLostAttemptInTx |
| `internal/db/jobs.go:JobStore.RetryWithLeaseAndTarget` | B.2 | AuthorizeRuntimeStartInTx, SettleLostAttemptInTx |
| `internal/db/jobs.go:JobStore.RetryWithoutConsumingAttemptWithLease` | B.2 | B.2 owns exact retry/defer adapter; A.2 solely owns status policy and E parked-control semantics. |
| `internal/db/jobs.go:JobStore.RetryWithoutConsumingAttemptWithLeaseAndResetWindow` | B.2 | B.2 owns exact retry/defer adapter; A.2 solely owns status policy and E parked-control semantics. |
| `internal/db/jobs.go:JobStore.RetryWithoutConsumingAttemptWithLeaseAndTarget` | B.2 | B.2 owns exact retry/defer adapter; A.2 solely owns status policy and E parked-control semantics. |
| `internal/db/session_activity_phases.go:SessionActivityPhaseStore.acknowledgeInboxBatch` | B.2 | AcknowledgeInputInTx |
| `internal/db/session_activity_phases.go:SessionActivityPhaseStore.AcknowledgeInboxBatch` | B.2 | AcknowledgeInputInTx |
| `internal/db/session_activity_phases.go:SessionActivityPhaseStore.AcknowledgeInboxBatchAndStartPhase` | B.2 | AcknowledgeInputInTx |
| `internal/db/session_activity_phases.go:SessionActivityPhaseStore.acknowledgeInboxBatchInTx` | B.2 | AcknowledgeInputInTx |
| `internal/db/session_activity_phases.go:SessionActivityPhaseStore.AcknowledgeInboxBatchWithTransition` | B.2 | AcknowledgeInputInTx |
| `internal/db/session_store.go:SessionStore.BeginRuntime` | B.2 | AuthorizeRuntimeStartInTx |
| `internal/db/session_store.go:SessionStore.ResetAfterLostReuse` | B.2 | SettleLostAttemptInTx, AuthorizeRuntimeStartInTx |
| `internal/services/agent/activity_phase_execution.go:activityPhaseExecution.acknowledgeAndResume` | B.2 | AcknowledgeInputInTx |
| `internal/services/agent/activity_phases.go:ActivityPhaseService.AcknowledgeInboxBatch` | B.2 | AcknowledgeInputInTx |
| `internal/services/agent/activity_phases.go:ActivityPhaseService.AcknowledgeInboxBatchAndStartPhase` | B.2 | AcknowledgeInputInTx |
| `internal/services/agent/orchestrator.go:Orchestrator.abandonReuseForRetry` | B.2 | SettleLostAttemptInTx |
| `internal/services/agent/orchestrator.go:Orchestrator.beginRuntimeControl` | B.2 | AuthorizeRuntimeStartInTx, SettleLostAttemptInTx |
| `internal/services/agent/orchestrator.go:Orchestrator.cleanupContinueSessionStartupFailure` | B.2 | SettleLostAttemptInTx, MutateWorkspaceRevisionInTx |
| `internal/services/agent/orchestrator.go:Orchestrator.ContinueSession` | B.2 | B.2 owns start orchestration; E extends the same primitive final phase/every Execute; B.1 handles settlement and C.1w descriptor effects. |
| `internal/services/agent/orchestrator.go:Orchestrator.RunAgent` | B.2 | B.2 owns start orchestration; E extends the same AuthorizeRuntimeStartInTx final phase/every Execute; B.1 handles settlement and C.1w physical descriptor effects. |
| `internal/services/agent/review_setup_hold.go:Orchestrator.acquireReviewSetupHold` | B.2 | AuthorizeRuntimeStartInTx, MutateWorkspaceRevisionInTx, SettleLostAttemptInTx |
| `internal/services/agent/runtime.go:runtimeController.Begin` | B.2 | AuthorizeRuntimeStartInTx |
| `internal/services/agent/thread_runtime_delivery.go:Orchestrator.startThreadRuntimeControl` | B.2 | AuthorizeRuntimeStartInTx, ClaimSeedInTx, MutateWorkspaceRevisionInTx |
| `internal/services/agent/thread_runtime_delivery.go:threadRuntimeControl.Close` | B.2 | B.2 owns the ack/close orchestration; exact own terminal settlement calls B.1 and holder mutation C.1w. |
| `internal/worker/handlers.go:newContinueSessionHandler` | B.2 | B.2 owns worker continuation adapter; E final authorization, B.1 failures and A.3 exact persisted answer identity are composed. |
| `internal/worker/handlers.go:newRunAgentHandler` | B.2 | B.2 owns worker start adapter; E alone supplies final permit/launch CAS, before running/domain/external effects. |
| `internal/worker/session_executor_dispatch.go:DurableSessionExecutorDispatcher.Dispatch` | B.2 | AuthorizeRuntimeStartInTx, SettleLostAttemptInTx |

### B.3c: 12 reviewed roots

| Exact inventory root | Primary caller owner | Required shared call-through / special completion rule |
| --- | --- | --- |
| `internal/api/handlers/sessions.go:consumeSessionCancelRequestDetached` | B.3c | RecordCancelIntentInTx |
| `internal/api/handlers/sessions.go:SessionHandler.EndSession` | B.3c | EndSessionInTx |
| `internal/db/code_review_loop_guard.go:stopFullReviewFallbackLoop` | B.3c | RecordCancelIntentInTx |
| `internal/db/session_store.go:SessionStore.ConsumeCancelRequest` | B.3c | RecordCancelIntentInTx |
| `internal/db/session_store.go:SessionStore.MarkRuntimeStopRequested` | B.3c | B.3c owns current cancel-intent delivery shape; runtime-budget/recovery projections call B.1. Caller-supplied reason is not authority. |
| `internal/db/session_store.go:SessionStore.RequestCancel` | B.3c | RecordCancelIntentInTx |
| `internal/db/session_thread_store.go:SessionThreadStore.MarkCancelRequested` | B.3c | RecordCancelIntentInTx |
| `internal/db/session_thread_store.go:SessionThreadStore.MarkCancelRequestedBySessions` | B.3c | RecordCancelIntentInTx |
| `internal/services/agent/orchestrator.go:Orchestrator.honorPendingCancelRequest` | B.3c | RecordCancelIntentInTx |
| `internal/services/thread/service.go:Service.EndThread` | B.3c | EndThreadInTx |
| `internal/worker/handlers.go:finalizeCancelledThreadBeforeContinuation` | B.3c | ProjectTurnInTx, SettleLostAttemptInTx |
| `internal/worker/handlers.go:newCancelSessionHandler` | B.3c | RecordCancelIntentInTx |

### B.3d: 7 reviewed roots

| Exact inventory root | Primary caller owner | Required shared call-through / special completion rule |
| --- | --- | --- |
| `internal/db/automation_completion.go:AutomationTargetStore.ReleaseStrandedOwnership` | B.3d | ChangeAutomationOwnerInTx |
| `internal/db/automation_targets.go:AutomationTargetStore.InsertGeneration` | B.3d | ChangeAutomationOwnerInTx |
| `internal/db/automation_targets.go:AutomationTargetStore.RetireGeneration` | B.3d | ChangeAutomationOwnerInTx |
| `internal/db/automation_turns.go:applyPendingOwnershipRelease` | B.3d | ChangeAutomationOwnerInTx |
| `internal/db/pull_request_health.go:PullRequestStore.CreateRepairRun` | B.3d | ChangeRepairReservationInTx, AdmitRepairTurnInTx |
| `internal/db/pull_request_health.go:PullRequestStore.DeactivateRepairRun` | B.3d | ChangeRepairReservationInTx |
| `internal/db/pull_request_health.go:PullRequestStore.upsertHealthSummary` | B.3d | ChangeRepairReservationInTx |

### B.3t: 6 reviewed roots

| Exact inventory root | Primary caller owner | Required shared call-through / special completion rule |
| --- | --- | --- |
| `internal/api/handlers/sessions.go:SessionHandler.ArchiveSession` | B.3t | B.3t owns archive authorization/orchestration; D.2 owns current-key expiry/deletion intent, C.1w its descriptor CAS. |
| `internal/db/session_store.go:SessionStore.Archive` | B.3t | Serialize the existing archive/visibility transition with parent/current owner and recovery reservation; retain user/system idempotence. Compose only the applicable exact supersession/expiry; never invent end/cancel or deletion authority. |
| `internal/db/session_store.go:SessionStore.ArchiveSystem` | B.3t | Serialize the existing archive/visibility transition with parent/current owner and recovery reservation; retain user/system idempotence. Compose only the applicable exact supersession/expiry; never invent end/cancel or deletion authority. |
| `internal/db/session_store.go:SessionStore.Unarchive` | B.3t | Serialize the existing archive/visibility transition with parent/current owner and recovery reservation; retain user/system idempotence. Compose only the applicable exact supersession/expiry; never invent end/cancel or deletion authority. |
| `internal/db/session_thread_store.go:SessionThreadStore.Archive` | B.3t | ArchiveThreadInTx |
| `internal/services/github/pr.go:PRService.maybeAutoArchiveSessionOnPRClose` | B.3t | B.3t owns archive policy/orchestration; D.2 owns exact current-reference cleanup, never blanket merged-PR authority. |

### C.1p: 9 reviewed roots

| Exact inventory root | Primary caller owner | Required shared call-through / special completion rule |
| --- | --- | --- |
| `internal/api/handlers/agent_capabilities.go:InternalAgentCapabilitiesHandler.Request` | C.1p | ProjectTurnInTx, ActivateRequestReadinessInTx |
| `internal/db/session_checkpoint_persistence.go:SessionStore.PublishCheckpointWithProvenanceInTx` | C.1p | PublishCheckpointInTx |
| `internal/db/session_checkpoint_persistence.go:SessionStore.publishCheckpointWithProvenanceRow` | C.1p | PublishCheckpointInTx |
| `internal/db/session_human_input_requests.go:SessionHumanInputRequestStore.Create` | C.1p | ActivateRequestReadinessInTx, ProjectTurnInTx |
| `internal/db/session_store.go:SessionStore.PublishCheckpoint` | C.1p | PublishCheckpointInTx |
| `internal/db/session_store.go:SessionStore.PublishCheckpointWithProvenance` | C.1p | PublishCheckpointInTx |
| `internal/db/session_store.go:SessionStore.UpdateSnapshotInfo` | C.1p | PublishCheckpointInTx |
| `internal/services/agent/orchestrator.go:Orchestrator.handleHumanInputPause` | C.1p | PublishCheckpointInTx, ProjectTurnInTx, ActivateRequestReadinessInTx |
| `internal/services/automations/turn_store.go:TurnStore.PublishCheckpointWithProvenance` | C.1p | PublishCheckpointInTx |

### C.1w: 30 reviewed roots

| Exact inventory root | Primary caller owner | Required shared call-through / special completion rule |
| --- | --- | --- |
| `internal/api/handlers/internal_preview.go:InternalPreviewHandler.RecyclePreview` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/api/handlers/sessions.go:SessionHandler.MaterializeChangeset` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/db/code_review_stale_container.go:SessionStore.finalizeCodeReviewContainer` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/db/code_review_workspace.go:CodeReviewWorkspaceStore.PublishPrepared` | C.1w | C.1w owns exact prepared container/holder attachment; retained review-owner transition calls B.3d. |
| `internal/db/code_review_workspace.go:CodeReviewWorkspaceStore.ReconcileMissing` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/db/session_changesets.go:SessionChangesetStore.AcceptSplit` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/db/session_changesets.go:SessionChangesetStore.AcquireLease` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/db/session_changesets.go:SessionChangesetStore.BeginMaterialization` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/db/session_changesets.go:SessionChangesetStore.CompleteMaterialization` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/db/session_changesets.go:SessionChangesetStore.RecordMaterializedDiff` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/db/session_sandbox_holder_store.go:SessionSandboxHolderStore.CreateActive` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/db/session_store.go:SessionStore.AcquireExistingTurnHold` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/db/session_store.go:SessionStore.AcquireTurnHold` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/db/session_store.go:SessionStore.BumpWorkspaceRevision` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/db/session_store.go:SessionStore.ClearContainerID` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/db/session_store.go:SessionStore.FinalizeContainerDestroy` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/db/session_store.go:SessionStore.PublishHydratedContainerID` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/db/session_store.go:SessionStore.ReleaseTurnHold` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/db/session_store.go:SessionStore.SetWorkerNodeIDForContainer` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/db/session_store.go:SessionStore.UpdateRevisionContext` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/db/session_store.go:SessionStore.UpdateSandboxState` | C.1w | C.1w owns shared descriptor CAS. Retention callers must first compose D.2 tombstone/notice/delete intent; generic setter is not retention authority. |
| `internal/db/session_store.go:SessionStore.UpdateWorkspaceSnapshot` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/db/session_store.go:SessionStore.writeDiffSnapshot` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/services/agent/automation_turn.go:Orchestrator.releaseInheritedContainer` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/services/agent/orchestrator.go:Orchestrator.MaterializeChangeset` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/services/github/pr_health_service.go:PRService.clearRepairRevisionContextIfIdle` | C.1w | C.1w owns physical/current revision-context mutation; B.3d owns exact repair reservation check/change. |
| `internal/worker/handlers.go:acquireOpenPRChangesetLease` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/worker/handlers.go:newMaterializeChangesetHandler` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/worker/handlers.go:newRestackChangesetsHandler` | C.1w | MutateWorkspaceRevisionInTx |
| `internal/worker/handlers.go:newVerifyChangesetSplitHandler` | C.1w | MutateWorkspaceRevisionInTx |

### C.1v: 10 reviewed roots

| Exact inventory root | Primary caller owner | Required shared call-through / special completion rule |
| --- | --- | --- |
| `internal/api/handlers/preview.go:PreviewHandler.resumeWarmSessionPreview` | C.1v | MutateWorkspaceRevisionInTx |
| `internal/api/handlers/preview.go:PreviewHandler.startPreviewLocal` | C.1v | MutateWorkspaceRevisionInTx |
| `internal/db/preview_store.go:PreviewStore.UpdatePreviewRuntimeWorkspaceRevision` | C.1v | MutateWorkspaceRevisionInTx |
| `internal/db/preview_store.go:PreviewStore.UpdatePreviewSourceWorkspaceRevision` | C.1v | MutateWorkspaceRevisionInTx |
| `internal/db/preview_store.go:PreviewStore.UpdatePreviewTargetSnapshotKey` | C.1v | MutateWorkspaceRevisionInTx |
| `internal/services/preview/manager.go:Manager.LaunchPreview` | C.1v | MutateWorkspaceRevisionInTx |
| `internal/services/preview/manager.go:Manager.ResumeStoppedWarmPreview` | C.1v | MutateWorkspaceRevisionInTx |
| `internal/services/preview/manager.go:Manager.StartPreview` | C.1v | MutateWorkspaceRevisionInTx |
| `internal/services/preview/start_runner.go:StartRunner.StartReservedPreview` | C.1v | MutateWorkspaceRevisionInTx |
| `internal/services/preview/start_runner.go:StartRunner.WarmSessionPreview` | C.1v | MutateWorkspaceRevisionInTx |

### D.1c: 11 reviewed roots

| Exact inventory root | Primary caller owner | Required shared call-through / special completion rule |
| --- | --- | --- |
| `internal/db/session_store.go:SessionStore.ClearPendingSnapshot` | D.1c | PublishCheckpointInTx |
| `internal/db/session_store.go:SessionStore.PromotePendingSnapshot` | D.1c | PublishCheckpointInTx, MutateWorkspaceRevisionInTx |
| `internal/db/session_store.go:SessionStore.ReapStrandedPendingSnapshots` | D.1c | MutateWorkspaceRevisionInTx |
| `internal/db/session_store.go:SessionStore.SetPendingSnapshotKey` | D.1c | PublishCheckpointInTx |
| `internal/db/session_thread_store.go:SessionThreadStore.SetBaseSnapshot` | D.1c | PublishCheckpointInTx |
| `internal/services/github/pr.go:PRService.CreateBranch` | D.1c | PublishCheckpointInTx, MutateWorkspaceRevisionInTx |
| `internal/services/github/pr.go:PRService.CreatePR` | D.1c | PublishCheckpointInTx, MutateWorkspaceRevisionInTx |
| `internal/services/github/pr.go:PRService.dispatchPostPRSnapshotUpload` | D.1c | PublishCheckpointInTx, MutateWorkspaceRevisionInTx |
| `internal/services/github/pr.go:PRService.PushChangesToPR` | D.1c | PublishCheckpointInTx, MutateWorkspaceRevisionInTx |
| `internal/worker/handlers.go:newPublishChangesetStackHandler` | D.1c | MutateWorkspaceRevisionInTx |
| `internal/worker/handlers.go:newPushPRChangesHandler` | D.1c | MutateWorkspaceRevisionInTx |

### D.2: 4 reviewed roots

| Exact inventory root | Primary caller owner | Required shared call-through / special completion rule |
| --- | --- | --- |
| `internal/db/session_store.go:SessionStore.ClearSnapshotKey` | D.2 | ExpireWorkspaceArtifactsInTx, MutateWorkspaceRevisionInTx |
| `internal/db/session_store.go:SessionStore.ListExpiredSnapshots` | D.2 | ExpireWorkspaceArtifactsInTx |
| `internal/services/github/pr.go:PRService.runMergedPullRequestFollowUps` | D.2 | ExpireWorkspaceArtifactsInTx, MutateWorkspaceRevisionInTx |
| `internal/services/storage/snapshot.go:CleanupSessionSnapshot` | D.2 | ExpireWorkspaceArtifactsInTx, MutateWorkspaceRevisionInTx |

### D.3: 1 reviewed roots

| Exact inventory root | Primary caller owner | Required shared call-through / special completion rule |
| --- | --- | --- |
| `internal/services/agent/orchestrator.go:Orchestrator.cleanupReviewBundle` | D.3 | D.3 owns this distinct existing orphan-artifact workflow, composing B.1's exact own-attempt settlement and D.1's fresh-key publication. Retain its writer/reference/unknown-I/O proof; no blanket SnapshotReaper permission or new object ledger. |

### F.2: 6 reviewed roots

| Exact inventory root | Primary caller owner | Required shared call-through / special completion rule |
| --- | --- | --- |
| `internal/api/handlers/sessions.go:SessionHandler.claimRetryThread` | F.2 | Retain failed-only retry semantics; compose exact admission/claim fences, never pending-only recovery eligibility. |
| `internal/api/handlers/sessions.go:SessionHandler.retrySessionFromCheckpoint` | F.2 | Retain typed checkpoint retry; compose exact admission/workspace reservation with no invented consent. |
| `internal/api/handlers/sessions.go:SessionHandler.retrySessionStartOver` | F.2 | Retain typed start-over eligibility; compose workspace reservation, B.2/E start and B.1 settlement. |
| `internal/api/handlers/sessions.go:SessionHandler.revertCheckpointRetry` | F.2 | SettleLostAttemptInTx |
| `internal/db/session_store.go:SessionStore.ResetForRetry` | F.2 | Retain typed reset policy and exact recovery/workspace authority; not a new pending-input API. |
| `internal/db/session_store.go:SessionStore.UndoResetForRetry` | F.2 | SettleLostAttemptInTx |

### G.1: 5 reviewed roots

| Exact inventory root | Primary caller owner | Required shared call-through / special completion rule |
| --- | --- | --- |
| `internal/db/session_lifecycle_cutover.go:observeLifecycleCutoverInTx` | G.1 | Early G.1 protocol core implements A.0's missing-active-provenance rule before A.1 routing; later guard integration retains this sole owner. No reused authority snapshot. |
| `internal/db/session_lifecycle_cutover.go:tryLockLifecycleCutoverExclusiveInTx` | G.1 | G.1 maintains the private protocol helper; H's separately gated activation owner is its consumer. Existing acquisition is not activation approval. |
| `internal/db/session_lifecycle_cutover.go:tryLockLifecycleCutoverInTx` | G.1 | Private cutover/scope/policy prerequisite; not a canonical mutation or authority |
| `internal/db/session_lifecycle_cutover.go:tryLockLifecycleCutoverSharedInTx` | G.1 | Private cutover/scope/policy prerequisite; not a canonical mutation or authority |
| `internal/db/session_lifecycle_transaction.go:requireLifecycleScopeTransactionInTx` | G.1 | Private cutover/scope/policy prerequisite; not a canonical mutation or authority |

## Other existing classifications: 56 compatibility audit assignments

These 29 fenced-specialist, 10 lifecycle-owned and 17 non-lifecycle-candidate
entries retain their source classifications. A legacy specialist fence is not the
new admission/end fence, and a key helper is not authority. The named family owns
required canonical composition and its existing contract; G.0 verifies each
positive non-lifecycle exclusion and relevant caller/boundary evidence. A required
conversion cannot be hidden by the prior classification. Record newly discovered
mutation roots explicitly and update the assignment, rather than exempting them.
The 255/353 legend above intentionally counts only the unfenced scope.

| Exact inventory entry | Existing classification | Compatibility responsibility |
| --- | --- | --- |
| `internal/db/automation_dispatch.go:AutomationTargetStore.SetAttemptedHead` | fenced-specialist | B.3d |
| `internal/db/code_review_recheck_dispatch.go:CodeReviewRecheckStore.Cancel` | fenced-specialist | B.1r |
| `internal/db/code_review_recheck_dispatch.go:CodeReviewRecheckStore.Claim` | fenced-specialist | B.2 |
| `internal/db/code_review_recheck_dispatch.go:CodeReviewRecheckStore.ClaimFullAssessmentOwner` | fenced-specialist | B.3d |
| `internal/db/code_review_recheck_dispatch.go:CodeReviewRecheckStore.Complete` | fenced-specialist | B.1r |
| `internal/db/code_review_recheck_dispatch.go:CodeReviewRecheckStore.Dispatch` | fenced-specialist | A.5 |
| `internal/db/code_review_recheck_dispatch.go:CodeReviewRecheckStore.Fail` | fenced-specialist | B.1r |
| `internal/db/code_review_recheck_dispatch.go:CodeReviewRecheckStore.FailTerminalJob` | fenced-specialist | B.1r |
| `internal/db/code_review_recheck_dispatch.go:CodeReviewRecheckStore.ReconcileDrainedTerminalTurn` | fenced-specialist | B.1r |
| `internal/db/code_review_recheck_dispatch.go:CodeReviewRecheckStore.ReconcileDrainedTerminalTurnInTx` | fenced-specialist | B.1r |
| `internal/db/code_review_recheck_dispatch.go:CodeReviewRecheckStore.RetireOwnerForCompletedReplacement` | fenced-specialist | B.3d |
| `internal/db/continuation_recovery.go:JobStore.RecordContinuationAttemptWithLease` | fenced-specialist | B.2 |
| `internal/db/jobs.go:ContinueSessionDedupeKey` | lifecycle-owned | A.2 |
| `internal/db/jobs.go:OpenPRDedupeKey` | lifecycle-owned | D.1c |
| `internal/db/jobs.go:RunAgentDedupeKey` | lifecycle-owned | A.2 |
| `internal/db/preview_store.go:PreviewStore.CreateSnapshot` | non-lifecycle-candidate | G.0 |
| `internal/db/preview_store.go:PreviewStore.DeleteOldestSnapshots` | non-lifecycle-candidate | G.0 |
| `internal/db/session_activity_seed.go:SessionActivityPhaseStore.StartSeedPhase` | fenced-specialist | B.2 |
| `internal/db/session_activity_seed.go:SessionActivityPhaseStore.StartSeedPhaseInTx` | fenced-specialist | B.2 |
| `internal/db/session_executor_store.go:SessionExecutorStore.CreateStarting` | fenced-specialist | B.2 |
| `internal/db/session_executor_store.go:SessionExecutorStore.MarkTerminalWithLease` | fenced-specialist | B.1q |
| `internal/db/session_review_workspace_recovery.go:SessionStore.RecoverIncompleteCodeReviewWorkspace` | fenced-specialist | F.2 |
| `internal/db/session_sandbox_holder_store.go:SessionSandboxHolderStore.ReleaseWithLease` | fenced-specialist | C.1w |
| `internal/db/session_store.go:createSessionRows` | lifecycle-owned | B.3t |
| `internal/db/session_thread_cancel.go:SessionThreadStore.MarkCancelRequestedForTurn` | lifecycle-owned | B.3c |
| `internal/db/session_thread_store.go:SessionThreadStore.Create` | lifecycle-owned | B.3t |
| `internal/db/session_thread_store.go:SessionThreadStore.CreateWithProvenance` | lifecycle-owned | B.3t |
| `internal/db/session_thread_workspace_failure.go:SessionThreadStore.FailWorkspaceWaitForDeadLetter` | fenced-specialist | B.1q |
| `internal/db/thread_inbox_store.go:ThreadInboxStore.ClaimDeliverableAfter` | fenced-specialist | B.2 |
| `internal/db/thread_inbox_store.go:ThreadInboxStore.RetryRecoverable` | fenced-specialist | A.6 |
| `internal/db/thread_runtime_store.go:ThreadRuntimeStore.CommitInboxDeliveryWithLease` | fenced-specialist | B.2 |
| `internal/db/thread_runtime_store.go:ThreadRuntimeStore.CreateStarting` | fenced-specialist | B.2 |
| `internal/db/thread_runtime_store.go:ThreadRuntimeStore.MarkTerminalWithLease` | fenced-specialist | B.1o |
| `internal/db/thread_runtime_store.go:ThreadRuntimeStore.ReclaimExpiredLeases` | fenced-specialist | B.1q |
| `internal/integration/fixtures.go:seedSession` | non-lifecycle-candidate | G.0 |
| `internal/services/agent/orchestrator.go:Orchestrator.CheckpointChangeset` | non-lifecycle-candidate | G.0 |
| `internal/services/agent/orchestrator.go:continueSessionDedupeKey` | lifecycle-owned | A.2 |
| `internal/services/agent/orchestrator.go:continueSessionDrainDedupeKey` | lifecycle-owned | A.2 |
| `internal/services/automations/target_dispatch.go:AutomationTurnDedupeKey` | lifecycle-owned | A.2 |
| `internal/services/automations/target_dispatch.go:TargetDispatcher.Dispatch` | fenced-specialist | A.5 |
| `internal/services/codereview/recheck_admission.go:Service.cancelForcedEvidenceAssessment` | fenced-specialist | B.3c |
| `internal/services/integration/slack_message_sender.go:InternalSlackMessageSender.SendMessage` | non-lifecycle-candidate | G.0 |
| `internal/services/integration/types.go:StubMessageSender.SendMessage` | non-lifecycle-candidate | G.0 |
| `internal/services/mcp/tools.go:ToolRegistry.CallTool` | non-lifecycle-candidate | G.0 |
| `internal/services/mcp/tools.go:ToolRegistry.callMessageSender` | non-lifecycle-candidate | G.0 |
| `internal/services/preview/start_runner.go:StartRunner.StartReservedBranchPreview` | non-lifecycle-candidate | G.0 |
| `internal/worker/session_executor_dispatch.go:DurableSessionExecutorDispatcher.markExecutorDispatchFailed` | fenced-specialist | B.1q |
| `internal/worker/session_executor_runtime.go:SessionExecutorRuntime.markExecutorTerminal` | fenced-specialist | B.1q |
| `migrations/000078_sessions_agent_type_amp_pi.down.sql:SQL` | non-lifecycle-candidate | G.0 |
| `migrations/000108_session_threads_idle_default.up.sql:SQL` | non-lifecycle-candidate | G.0 |
| `migrations/000113_session_threads_backfill_primary.down.sql:SQL` | non-lifecycle-candidate | G.0 |
| `migrations/000113_session_threads_backfill_primary.up.sql:SQL` | non-lifecycle-candidate | G.0 |
| `migrations/000114_session_worker_node_id_unstuck.up.sql:SQL` | non-lifecycle-candidate | G.0 |
| `migrations/000151_fail_running_threads_for_terminal_sessions.up.sql:SQL` | non-lifecycle-candidate | G.0 |
| `migrations/000192_slackbot_human_input_privacy.up.sql:SQL` | non-lifecycle-candidate | G.0 |
| `migrations/000194_pull_request_repair_head_identity.up.sql:SQL` | non-lifecycle-candidate | G.0 |

## Syntax-only candidates: bounded tracing assignment

G.0 is the tracing audit owner; the family owner printed for each exact candidate performs the trace before its first affected implementation/review gate. Each batch contains at most 15 candidates and must close all rows or record a concrete evidence-backed transfer to a named semantic owner. G.0 verifies set equality and closure; H cannot pass with any of the 353 untraced.

Tracing is read-only source work, not an assumption that the candidate mutates lifecycle state. For each candidate, record the direct callee chain, concrete writes/claims/status predicates and transaction/effect owner; link each real mutation to a reviewed or newly registered canonical root. If it is only a call-through, retain caller coverage. If genuinely non-lifecycle/observation, supply the exact positive reason and boundary test. If multi-domain, retain one orchestration owner with named shared call-throughs. Path-based assignment below is an initial tracing responsibility only, not an approved semantic classification.

### Trace A.1-T01: 9 candidates; family owner A.1; audit G.0

- `internal/api/handlers/internal_session_tabs.go:InternalSessionTabsHandler.SendMessage`
- `internal/api/handlers/session_threads.go:SessionThreadHandler.SendThreadMessage`
- `internal/db/thread_inbox_store.go:ThreadInboxStore.AppendForMessage`
- `internal/services/thread/service.go:Service.appendInboxForMessage`
- `internal/services/thread/service.go:Service.appendInboxForMessageWithStore`
- `internal/services/thread/service.go:Service.createMessageInTx`
- `internal/services/thread/service.go:Service.createQueuedMessage`
- `internal/services/thread/service.go:Service.createQueuedMessageAndInboxInTx`
- `internal/services/thread/service.go:Service.queueMessageWaitingForSlot`

### Trace A.3-T01: 8 candidates; family owner A.3; audit G.0

- `internal/api/handlers/sessions.go:SessionHandler.AnswerHumanInputRequest`
- `internal/api/handlers/sessions.go:SessionHandler.CancelHumanInputRequest`
- `internal/services/humaninput/db_repository.go:dbTx.EnqueueContinue`
- `internal/services/humaninput/service.go:claimForProviderResume`
- `internal/services/humaninput/service.go:Service.Cancel`
- `internal/worker/handlers.go:handleSlackHumanInputAnswer`
- `internal/worker/handlers.go:handleSlackHumanInputFreeformModal`
- `internal/worker/handlers.go:handleSlackHumanInputMultiModal`

### Trace A.4-T01: 15 candidates; family owner A.4; audit G.0

- `internal/api/handlers/eval.go:EvalHandler.Bootstrap`
- `internal/api/handlers/eval.go:EvalHandler.bootstrapSessionBacked`
- `internal/api/handlers/eval.go:EvalHandler.StartBatch`
- `internal/api/handlers/eval.go:EvalHandler.StartCompare`
- `internal/api/handlers/eval.go:EvalHandler.StartRun`
- `internal/api/handlers/internal_issues.go:InternalIssueHandler.Create`
- `internal/api/handlers/internal_issues.go:InternalIssueHandler.dispatchSession`
- `internal/api/handlers/pagerduty_integrations.go:PagerDutyIntegrationHandler.StartIncidentSession`
- `internal/api/handlers/pagerduty_webhooks.go:PagerDutyWebhookHandler.HandleStartSessionAction`
- `internal/api/handlers/sessions.go:SessionHandler.CreateExternal`
- `internal/api/handlers/sessions.go:SessionHandler.CreateManual`
- `internal/api/handlers/sessions.go:SessionHandler.TriggerFix`
- `internal/db/session_store.go:SessionStore.Create`
- `internal/db/session_store.go:SessionStore.CreateForIncident`
- `internal/db/session_store.go:SessionStore.CreateInTx`

### Trace A.4-T02: 15 candidates; family owner A.4; audit G.0

- `internal/services/pagerduty/session_start.go:SessionStarter.StartSession`
- `internal/worker/handlers_linear_agent_created.go:createAndAttachLinearAgentSession`
- `internal/worker/handlers_linear_agent_created.go:handleLinearAgentCreated`
- `internal/worker/handlers_linear_agent_created.go:reconcileLinearAgentCreated`
- `internal/worker/handlers_linear_agent_helpers.go:enqueueRunAgentForLinearAgent`
- `internal/worker/handlers_linear_agent_prompted.go:appendPromptedMessageAndEnqueueContinue`
- `internal/worker/handlers_linear_agent_prompted.go:continueLinearAgentEnqueueOpts`
- `internal/worker/handlers_linear_agent_prompted.go:enqueueContinueForLinearAgent`
- `internal/worker/handlers_linear_agent_prompted.go:enqueueContinueForLinearAgentInTx`
- `internal/worker/handlers_linear_agent.go:newLinearAgentEventHandler`
- `internal/worker/handlers.go:enqueueCompatRunAgent`
- `internal/worker/handlers.go:enqueueRunAgentForSession`
- `internal/worker/handlers.go:enqueueSlackSessionContinuationMessage`
- `internal/worker/handlers.go:enqueueSlackSessionContinuationPrompt`
- `internal/worker/handlers.go:enqueueSlackSessionContinuationPromptWithReferences`

### Trace A.4-T03: 6 candidates; family owner A.4; audit G.0

- `internal/worker/handlers.go:handleSlackSelectRepository`
- `internal/worker/handlers.go:handleSlackSpecializedSessionAction`
- `internal/worker/handlers.go:newLegacyRunEvalBootstrapCompatHandler`
- `internal/worker/handlers.go:newLegacyRunEvalCompatHandler`
- `internal/worker/handlers.go:newSlackHandleInteractionHandler`
- `internal/worker/handlers.go:newSlackStartOrContinueSessionHandler`

### Trace A.5-T01: 15 candidates; family owner A.5; audit G.0

- `internal/db/code_review_scheduling.go:CodeReviewScheduleStore.RepairMissingWakes`
- `internal/services/agent/review_inputs.go:Orchestrator.codeReviewInputsPreparationFinished`
- `internal/services/automations/goal_improvement.go:GoalImprovementService.ImproveDraft`
- `internal/services/automations/goal_improvement.go:GoalImprovementService.ImproveSaved`
- `internal/services/automations/goal_improvement.go:GoalImprovementService.startDeepImprovement`
- `internal/services/automations/goal_improvement.go:GoalImprovementService.startDeepImprovementFromInput`
- `internal/services/automations/goal_improvement.go:GoalImprovementService.startDraftDeepImprovement`
- `internal/services/codereview/capture_identity_recovery.go:Service.QueueCapturedIdentityReplacement`
- `internal/services/codereview/recheck_admission.go:Service.FallbackAssessmentToFull`
- `internal/services/codereview/recheck_admission.go:Service.RefreshUnsentEvidenceAssessment`
- `internal/services/codereview/recheck_admission.go:Service.requestAssessmentReview`
- `internal/services/codereview/recheck_admission.go:Service.requestFullAssessmentReview`
- `internal/services/codereview/recheck_admission.go:Service.requestLegacyAssessmentReview`
- `internal/services/codereview/scheduling.go:Service.deferCapacitySchedule`
- `internal/services/codereview/scheduling.go:Service.deferUnavailableSchedule`

### Trace A.5-T02: 15 candidates; family owner A.5; audit G.0

- `internal/services/codereview/scheduling.go:Service.ReconcileSchedule`
- `internal/services/codereview/scheduling.go:Service.refreshSchedule`
- `internal/services/codereview/scheduling.go:Service.RequestScheduledReview`
- `internal/services/codereview/scheduling.go:Service.scheduleReview`
- `internal/services/codereview/scheduling.go:Service.ValidateScheduledExecution`
- `internal/services/codereview/service.go:Service.ensureLatestRetrySource`
- `internal/services/codereview/service.go:Service.handleExplicitReviewRequest`
- `internal/services/codereview/service.go:Service.HandleReviewChanged`
- `internal/services/codereview/service.go:Service.HandleReviewMentioned`
- `internal/services/codereview/service.go:Service.HandleReviewRequested`
- `internal/services/codereview/service.go:Service.QueueReviewChanged`
- `internal/services/codereview/service.go:Service.retryReview`
- `internal/services/codereview/service.go:Service.RetryReview`
- `internal/services/codereview/service.go:Service.startReview`
- `internal/services/github/pr_auto_repair.go:PRService.MaybeStartAutoRepair`

### Trace A.5-T03: 15 candidates; family owner A.5; audit G.0

- `internal/services/github/pr_auto_repair.go:PRService.MaybeStartAutoRepairForPullRequest`
- `internal/services/github/pr_health_service.go:PRService.StartPullRequestRepair`
- `internal/services/reviewloop/service.go:automationOpenPRDedupeKey`
- `internal/services/reviewloop/service.go:RuntimeAdapter.SendMessage`
- `internal/services/reviewloop/service.go:Service.automationOpenPRTarget`
- `internal/services/reviewloop/service.go:Service.refreshPublicationEvidence`
- `internal/services/reviewloop/service.go:Service.sendPlain`
- `internal/services/reviewloop/service.go:Service.sendReview`
- `internal/services/reviewloop/service.go:Service.Start`
- `internal/services/reviewloop/service.go:Service.startLegacyFixPass`
- `internal/worker/code_review_assessment.go:resumeStagedFullAssessment`
- `internal/worker/code_review_handler.go:ensureCodeReviewReviewerThreads`
- `internal/worker/code_review_handler.go:newRunCodeReviewHandler`
- `internal/worker/code_review_handler.go:requestCodeReviewOrchestratorSynthesisRepair`
- `internal/worker/code_review_orchestrator_fallback.go:ensureCodeReviewFallbackOrchestratorThread`

### Trace A.5-T04: 5 candidates; family owner A.5; audit G.0

- `internal/worker/code_review_orchestrator_fallback.go:ensureCodeReviewFallbackThread`
- `internal/worker/code_review_recheck_handler.go:newRunCodeReviewRecheckHandler`
- `internal/worker/code_review_reviewer_fallback.go:ensureCodeReviewReviewerThread`
- `internal/worker/handlers.go:claimAutomationTurnAttempt`
- `internal/worker/handlers.go:newAutomationRunHandler`

### Trace A.6-T01: 5 candidates; family owner A.6; audit G.0

- `internal/api/handlers/session_threads.go:SessionThreadHandler.RetryInboxEntry`
- `internal/services/agent/orchestrator.go:Orchestrator.drainQueuedMessages`
- `internal/services/agent/orchestrator.go:Orchestrator.drainQueuedMessagesAfterProcessedID`
- `internal/services/thread/actions.go:Service.enqueueThreadContinuation`
- `internal/services/thread/actions.go:Service.RetryInboxEntry`

### Trace B.1p-T01: 1 candidates; family owner B.1p; audit G.0

- `internal/db/session_store.go:SessionStore.UpdateStatus`

### Trace B.1o-T01: 12 candidates; family owner B.1o; audit G.0

- `internal/services/agent/orchestrator.go:Orchestrator.failContinueSessionError`
- `internal/services/agent/orchestrator.go:Orchestrator.failRun`
- `internal/services/agent/orchestrator.go:Orchestrator.failRunWithCategory`
- `internal/services/agent/orchestrator.go:Orchestrator.failRunWithCategoryForThread`
- `internal/services/agent/orchestrator.go:Orchestrator.failRunWithCategoryForThreadAndActivityPhase`
- `internal/services/agent/orchestrator.go:Orchestrator.failRunWithResult`
- `internal/services/agent/orchestrator.go:Orchestrator.failRunWithResultAndActivityPhase`
- `internal/services/agent/orchestrator.go:Orchestrator.failTimedOutSession`
- `internal/services/agent/orchestrator.go:Orchestrator.handlePolicyStoppedSession`
- `internal/services/agent/orchestrator.go:Orchestrator.registerSandboxInfraFailure`
- `internal/services/agent/reaper.go:SessionReaper.Run`
- `internal/services/ownerloss/service.go:Service.RecoverLostOwner`

### Trace B.1q-T01: 2 candidates; family owner B.1q; audit G.0

- `internal/worker/session_executor_runtime.go:SessionExecutorRuntime.finishAttempt`
- `internal/worker/workspace_wait_failure.go:registerWorkspaceWaitDeadLetter`

### Trace B.1a-T01: 15 candidates; family owner B.1a; audit G.0

- `internal/db/automation_turns.go:AutomationRunStore.CompleteThreadTurnForAttempt`
- `internal/db/automation_turns.go:AutomationRunStore.FailThreadTurnForAttempt`
- `internal/services/agent/automation_turn.go:Orchestrator.completeAutomationThreadTurn`
- `internal/services/agent/automation_turn.go:Orchestrator.endClosedTargetTurn`
- `internal/services/agent/automation_turn.go:Orchestrator.endInterruptedAutomationTurn`
- `internal/services/agent/automation_turn.go:Orchestrator.failAutomationThreadTurn`
- `internal/services/agent/automation_turn.go:Orchestrator.failAutomationTurnRunSetup`
- `internal/services/agent/automation_turn.go:Orchestrator.failAutomationTurnSetup`
- `internal/services/automations/turn_completer.go:TurnCompleter.Complete`
- `internal/services/automations/turn_completer.go:TurnCompleter.FailRetriesExhausted`
- `internal/services/automations/turn_completer.go:TurnCompleter.RecoverAbandonedRun`
- `internal/services/automations/turn_completer.go:TurnCompleter.Sweep`
- `internal/services/automations/turn_store.go:TurnStore.CompletePreflight`
- `internal/services/automations/turn_store.go:TurnStore.CompleteThreadTurn`
- `internal/services/automations/turn_store.go:TurnStore.EndInterruptedAttempt`

### Trace B.1a-T02: 1 candidates; family owner B.1a; audit G.0

- `internal/services/automations/turn_store.go:TurnStore.FailThreadTurn`

### Trace B.1r-T01: 15 candidates; family owner B.1r; audit G.0

- `internal/db/code_review_ended_recovery.go:CodeReviewScheduleStore.ReconcileEndedFullReview`
- `internal/db/code_review_terminal_recovery.go:CodeReviewScheduleStore.ReconcileTerminalReviews`
- `internal/db/review_loops.go:finishPublicationReviewOn`
- `internal/db/review_loops.go:markLoopTerminalOn`
- `internal/db/review_loops.go:resumeParkedPublicationOn`
- `internal/db/review_loops.go:SessionReviewLoopStore.MarkLoopFailed`
- `internal/db/review_loops.go:SessionReviewLoopStore.MarkLoopFailedAndEnqueueOpenPR`
- `internal/db/review_loops.go:SessionReviewLoopStore.MarkLoopNeedsHumanDecision`
- `internal/db/review_loops.go:SessionReviewLoopStore.MarkPassClean`
- `internal/db/review_loops.go:SessionReviewLoopStore.MarkPassCleanAndEnqueueOpenPR`
- `internal/db/review_loops.go:SessionReviewLoopStore.MarkPassNeedsHumanDecision`
- `internal/db/review_loops.go:SessionReviewLoopStore.MarkPassNeedsHumanDecisionAndEnqueueOpenPR`
- `internal/db/review_loops.go:SessionReviewLoopStore.RestartStrandedPublicationLoop`
- `internal/db/session_automatic_publication.go:SessionStore.UpdateResultWithAutomaticPublication`
- `internal/services/github/pr_health_service.go:PRService.CompletePullRequestRepairRun`

### Trace B.1r-T02: 15 candidates; family owner B.1r; audit G.0

- `internal/services/github/pr_health_service.go:PRService.reconcileSessionPublication`
- `internal/services/github/pr_health_service.go:PRService.reconcileSessionPublications`
- `internal/services/reviewloop/service.go:Service.completeFixAndStartNextReview`
- `internal/services/reviewloop/service.go:Service.failLoop`
- `internal/services/reviewloop/service.go:Service.markPassClean`
- `internal/services/reviewloop/service.go:Service.markPassNeedsHumanDecision`
- `internal/services/reviewloop/service.go:Service.OnThreadTurnComplete`
- `internal/services/reviewloop/service.go:Service.OnThreadTurnCompleteWithChange`
- `internal/services/reviewloop/service.go:Service.OnThreadTurnFailed`
- `internal/worker/code_review_handler.go:completeCodeReviewAfterStableDeterministicFailure`
- `internal/worker/code_review_handler.go:failCodeReviewWithoutReviewerOutput`
- `internal/worker/code_review_handler.go:harvestCodeReviewOrchestratorResult`
- `internal/worker/code_review_handler.go:harvestCodeReviewReviewerResults`
- `internal/worker/code_review_handler.go:reconcileCodeReviewSessionFailure`
- `internal/worker/code_review_handler.go:reconcileCodeReviewSessionJobFailure`

### Trace B.1r-T03: 10 candidates; family owner B.1r; audit G.0

- `internal/worker/code_review_handler.go:reconcileCodeReviewSessionStale`
- `internal/worker/code_review_handler.go:reconcileCodeReviewSessionSuccess`
- `internal/worker/code_review_handler.go:registerCodeReviewDeadLetterReconciliation`
- `internal/worker/code_review_handler.go:revertCodeReviewReadOnlyThread`
- `internal/worker/code_review_publication_recovery.go:recoverStagedFullAssessment`
- `internal/worker/code_review_recheck_handler.go:failCodeReviewRecheck`
- `internal/worker/code_review_recheck_handler.go:registerCodeReviewRecheckDeadLetterReconciliation`
- `internal/worker/code_review_recheck_handler.go:resumeCodeReviewRecheckPublication`
- `internal/worker/code_review_recheck_handler.go:settleCodeReviewAssessment`
- `internal/worker/handlers.go:completeAutomationThreadTurn`

### Trace B.2-T01: 15 candidates; family owner B.2; audit G.0

- `internal/cluster/recovery.go:RecoveryLoop.runOnce`
- `internal/cluster/recovery.go:RecoveryLoop.Start`
- `internal/db/thread_inbox_store.go:ThreadInboxStore.ListAppliedMessageIDs`
- `internal/db/thread_inbox_store.go:ThreadInboxStore.ListDeliverableAfter`
- `internal/db/thread_inbox_store.go:ThreadInboxStore.MarkAckedForSeedMessages`
- `internal/db/thread_inbox_store.go:ThreadInboxStore.MarkDeliveredForEntry`
- `internal/db/thread_inbox_store.go:ThreadInboxStore.MarkDeliveringForMessages`
- `internal/db/thread_inbox_store.go:ThreadInboxStore.ResetUnsentDelivery`
- `internal/services/agent/activity_phase_execution.go:newSeedActivityPhaseExecution`
- `internal/services/agent/activity_phases.go:ActivityPhaseService.StartSeedPhase`
- `internal/services/agent/continuation_input.go:Orchestrator.recordContinuationAttempt`
- `internal/services/agent/continuation_input.go:Orchestrator.resolveContinuationInput`
- `internal/services/agent/continuation_input.go:Orchestrator.unappliedThreadMessages`
- `internal/services/agent/thread_runtime_delivery.go:Orchestrator.acknowledgeConfirmedThreadInboxInputs`
- `internal/services/agent/thread_runtime_delivery.go:Orchestrator.commitThreadInboxWatermark`

### Trace B.2-T02: 15 candidates; family owner B.2; audit G.0

- `internal/services/agent/orchestrator.go:Orchestrator.RecoverSession`
- `internal/services/agent/orchestrator.go:Orchestrator.RecoverSessionWithOptions`
- `internal/services/agent/thread_runtime_delivery.go:Orchestrator.DeliverThreadInbox`
- `internal/services/agent/thread_runtime_delivery.go:Orchestrator.flushConfirmedThreadInput`
- `internal/services/agent/thread_runtime_delivery.go:threadRuntimeControl.admitSeedWithoutPhase`
- `internal/services/agent/thread_runtime_delivery.go:threadRuntimeHandleAttacher.Attach`
- `internal/services/thread/service.go:Service.enqueueLiveThreadInboxDelivery`
- `internal/worker/handlers.go:newDeliverThreadInboxHandler`
- `internal/worker/session_executor_dispatch.go:maybeDispatchSessionExecutor`
- `internal/worker/session_executor_runtime.go:SessionExecutorRuntime.handlerFor`
- `internal/worker/session_executor_runtime.go:SessionExecutorRuntime.retryJob`
- `internal/worker/session_executor_runtime.go:SessionExecutorRuntime.Run`
- `internal/worker/worker.go:Worker.retryJob`
- `internal/worker/worker.go:Worker.retryJobWithDelay`
- `internal/worker/worker.go:Worker.retryJobWithDelayAt`

### Trace B.3c-T01: 15 candidates; family owner B.3c; audit G.0

- `internal/api/handlers/automation_goal_improvements.go:AutomationHandler.CancelGoalImprovement`
- `internal/api/handlers/internal_preview.go:InternalPreviewHandler.CancelSession`
- `internal/api/handlers/session_thread_actions.go:SessionThreadHandler.CancelThread`
- `internal/api/handlers/session_threads.go:SessionThreadHandler.EndThread`
- `internal/api/handlers/sessions.go:SessionHandler.CancelSession`
- `internal/api/handlers/sessions.go:SessionHandler.routeRemoteCancel`
- `internal/db/code_review_loop_guard.go:CodeReviewScheduleStore.StopFullReviewFallbackLoop`
- `internal/db/code_review_scheduling.go:RevokeScheduledCodeReviews`
- `internal/db/review_loops.go:SessionReviewLoopStore.CancelLoop`
- `internal/services/agent/orchestrator.go:Orchestrator.CancelSessionByID`
- `internal/services/agent/orchestrator.go:Orchestrator.CancelThreadByID`
- `internal/services/agent/orchestrator.go:Orchestrator.CancelThreadTurnByID`
- `internal/services/agent/runtime.go:runtimeController.RequestStop`
- `internal/services/agent/runtime.go:runtimeController.tick`
- `internal/services/automations/goal_improvement.go:GoalImprovementService.Cancel`

### Trace B.3c-T02: 10 candidates; family owner B.3c; audit G.0

- `internal/services/codereview/scheduling.go:Service.cancelStaleScheduledThreads`
- `internal/services/thread/actions.go:Service.CancelActiveThreads`
- `internal/services/thread/actions.go:Service.CancelThread`
- `internal/services/thread/actions.go:Service.CancelThreadTurn`
- `internal/worker/code_review_handler.go:cancelActiveCodeReviewThreads`
- `internal/worker/code_review_handler.go:cancelCodeReviewThread`
- `internal/worker/code_review_handler.go:codeReviewThreadCanceller.CancelThread`
- `internal/worker/code_review_publication_recovery.go:stopUnsentStagedReviewIfParentCancelled`
- `internal/worker/handlers.go:finalizeThreadCancellationIfRequested`
- `internal/worker/handlers.go:newCancelThreadHandler`

### Trace B.3d-T01: 15 candidates; family owner B.3d; audit G.0

- `internal/db/automation_lifecycle.go:AutomationTargetStore.ApplyPullRequestClosed`
- `internal/db/automation_lifecycle.go:AutomationTargetStore.RetireTerminalTarget`
- `internal/db/automation_targets.go:AutomationTargetStore.RetireActiveGenerationsForAutomation`
- `internal/db/pull_request_health.go:PullRequestStore.UpsertHealthSummary`
- `internal/db/pull_request_health.go:PullRequestStore.UpsertProjectedHealthSummary`
- `internal/services/automations/turn_completer.go:TurnCompleter.releaseStrandedOwnership`
- `internal/services/automations/turn_completer.go:TurnCompleter.retireGeneration`
- `internal/services/automations/turn_store.go:TurnStore.RetireGeneration`
- `internal/services/github/pr_health_budget.go:PRService.syncAdmittedPullRequestState`
- `internal/services/github/pr_health_budget.go:PRService.syncBudgetedPullRequestState`
- `internal/services/github/pr_health_service.go:PRService.getPullRequestHealth`
- `internal/services/github/pr_health_service.go:PRService.GetPullRequestHealth`
- `internal/services/github/pr_health_service.go:PRService.ReconcilePullRequestState`
- `internal/services/github/pr_health_service.go:PRService.SyncCodeReviewPullRequestState`
- `internal/services/github/pr_health_service.go:PRService.syncPullRequestState`

### Trace B.3d-T02: 8 candidates; family owner B.3d; audit G.0

- `internal/services/github/pr_health_service.go:PRService.SyncPullRequestState`
- `internal/services/github/pr_health_service.go:PRService.syncResolvedPullRequestState`
- `internal/services/github/pr_health_service.go:PRService.syncResolvedPullRequestStateCapturing`
- `internal/services/github/pr.go:PRService.applyClosedPRTransition`
- `internal/services/github/pr.go:PRService.HandlePullRequestEvent`
- `internal/worker/code_review_handler.go:supersedeCodeReviewForCapturedIdentity`
- `internal/worker/code_review_handler.go:supersedeCodeReviewForChangedHead`
- `internal/worker/code_review_workspace.go:recoverEndedCodeReviewParent`

### Trace B.3t-T01: 11 candidates; family owner B.3t; audit G.0

- `internal/api/handlers/internal_session_tabs.go:InternalSessionTabsHandler.Create`
- `internal/api/handlers/session_threads.go:SessionThreadHandler.ArchiveThread`
- `internal/api/handlers/session_threads.go:SessionThreadHandler.CreateThread`
- `internal/api/handlers/session_threads.go:SessionThreadHandler.UpdateThread`
- `internal/api/handlers/sessions.go:SessionHandler.UnarchiveSession`
- `internal/services/reviewloop/service.go:RuntimeAdapter.CreateThread`
- `internal/services/thread/service.go:Service.ArchiveThread`
- `internal/services/thread/service.go:Service.createThread`
- `internal/services/thread/service.go:Service.CreateThread`
- `internal/services/thread/service.go:Service.UpdateThread`
- `internal/worker/thread_actions.go:newForkSessionThreadHandler`

### Trace C.1p-T01: 2 candidates; family owner C.1p; audit G.0

- `internal/services/agent/automation_turn.go:Orchestrator.publishAutomationCheckpoint`
- `internal/services/agent/orchestrator.go:Orchestrator.publishBootstrapCheckpoint`

### Trace C.1w-T01: 14 candidates; family owner C.1w; audit G.0

- `internal/api/handlers/branch_previews.go:BranchPreviewHandler.Restart`
- `internal/api/handlers/branch_previews.go:BranchPreviewHandler.restartSessionPreviewInstance`
- `internal/api/handlers/branch_previews.go:BranchPreviewHandler.StartLatest`
- `internal/api/handlers/internal_preview.go:InternalPreviewHandler.StopActivePreviewForSession`
- `internal/api/handlers/sessions.go:SessionHandler.AcceptChangesetSplit`
- `internal/db/code_review_stale_container.go:SessionStore.FinalizeStaleTerminalCodeReviewContainer`
- `internal/db/session_store.go:SessionStore.FinalizeIdleCodeReviewContainer`
- `internal/services/agent/orchestrator.go:Orchestrator.diagnoseAcquireHoldRaceLoss`
- `internal/services/agent/orchestrator.go:Orchestrator.releaseTurnHoldAfterReview`
- `internal/services/agent/orchestrator.go:Orchestrator.setupFreshSandbox`
- `internal/services/agent/orchestrator.go:Orchestrator.setupFreshSandboxForThread`
- `internal/services/automations/turn_store.go:TurnStore.ReleaseTurnHold`
- `internal/worker/code_review_workspace.go:newPrepareCodeReviewWorkspaceHandler`
- `internal/worker/handlers.go:newSessionPreviewWarmBuildHandler`

### Trace C.1v-T01: 15 candidates; family owner C.1v; audit G.0

- `internal/api/handlers/preview.go:PreviewHandler.acquireSandbox`
- `internal/api/handlers/preview.go:PreviewHandler.clearDeadLiveSessionPreviewOwner`
- `internal/api/handlers/preview.go:PreviewHandler.ensurePreview`
- `internal/api/handlers/preview.go:PreviewHandler.EnsurePreview`
- `internal/api/handlers/preview.go:PreviewHandler.executePreviewRecycleUpdate`
- `internal/api/handlers/preview.go:PreviewHandler.RestartPreview`
- `internal/api/handlers/preview.go:PreviewHandler.RestartSessionPreview`
- `internal/api/handlers/preview.go:PreviewHandler.StartPreview`
- `internal/api/handlers/preview.go:PreviewHandler.startPreviewFromRequest`
- `internal/api/handlers/preview.go:PreviewHandler.UpdatePreview`
- `internal/services/preview/hmr_watcher.go:HMRWatcher.captureAgentChange`
- `internal/services/preview/hmr_watcher.go:HMRWatcher.captureAndStore`
- `internal/services/preview/hmr_watcher.go:HMRWatcher.captureBaseline`
- `internal/services/preview/hmr_watcher.go:HMRWatcher.enforceSnapshotLimit`
- `internal/services/preview/hmr_watcher.go:HMRWatcher.OnWebSocketMessage`

### Trace C.1v-T02: 15 candidates; family owner C.1v; audit G.0

- `internal/services/preview/hmr_watcher.go:HMRWatcher.StartWatching`
- `internal/services/preview/manager.go:Manager.AbortReservation`
- `internal/services/preview/manager.go:Manager.recyclePreview`
- `internal/services/preview/manager.go:Manager.RecyclePreview`
- `internal/services/preview/manager.go:Manager.RecyclePreviewWithConfig`
- `internal/services/preview/manager.go:Manager.RecyclePreviewWithConfigAndRevision`
- `internal/services/preview/manager.go:Manager.releasePreviewHoldAfterRecycleFailure`
- `internal/services/preview/manager.go:Manager.reservePreview`
- `internal/services/preview/manager.go:Manager.ReservePreview`
- `internal/services/preview/manager.go:Manager.ReservePreviewForWorkerInTx`
- `internal/services/preview/manager.go:Manager.softRestartPreview`
- `internal/services/preview/manager.go:Manager.SoftRestartPreview`
- `internal/services/preview/manager.go:Manager.SoftRestartPreviewWithRevision`
- `internal/services/preview/manager.go:Manager.StopActivePreviewForSession`
- `internal/services/preview/manager.go:Manager.StopPreview`

### Trace C.1v-T03: 8 candidates; family owner C.1v; audit G.0

- `internal/services/preview/manager.go:Manager.StopPreviewWithReason`
- `internal/services/preview/runtime_revision_stamper.go:DBPreviewRuntimeRevisionStamper.StampPreviewRuntimeRevision`
- `internal/services/preview/snapshot_cache.go:SnapshotCache.CreateSnapshot`
- `internal/services/preview/start_runner.go:StartRunner.acquireSandbox`
- `internal/services/preview/start_runner.go:StartRunner.clearStalePreviewContainer`
- `internal/services/preview/start_runner.go:StartRunner.createBranchPreviewStartupCache`
- `internal/services/preview/start_runner.go:StartRunner.createSessionPreviewStartupCache`
- `internal/services/preview/start_runner.go:StartRunner.PrewarmPreviewCaches`

### Trace D.1c-T01: 8 candidates; family owner D.1c; audit G.0

- `internal/api/handlers/sessions.go:SessionHandler.CreatePR`
- `internal/api/handlers/sessions.go:SessionHandler.PublishChangeset`
- `internal/cluster/scheduler.go:Scheduler.reapStrandedPendingSnapshots`
- `internal/db/jobs.go:JobStore.QueueChangesetPRCreation`
- `internal/services/github/pr_health_service.go:PRService.resumeSessionPublicationCreation`
- `internal/services/github/pr.go:PRService.checkpointExistingPullRequestPublication`
- `internal/worker/handlers.go:enqueuePRPushReconciliation`
- `internal/worker/handlers.go:newOpenPRHandler`

### Trace D.2-T01: 5 candidates; family owner D.2; audit G.0

- `internal/services/github/pr_merge_when_ready.go:PRService.currentMergeWhenReadyHealth`
- `internal/services/github/pr_merge_when_ready.go:PRService.ProcessMergeWhenReady`
- `internal/services/github/pr_merge_when_ready.go:PRService.QueueMergeWhenReady`
- `internal/services/github/pr_merge.go:PRService.mergePullRequest`
- `internal/services/github/pr_merge.go:PRService.MergePullRequest`

### Trace E.1-T01: 5 candidates; family owner E.1; audit G.0

- `internal/services/agent/orchestrator.go:Orchestrator.ensureClaudeCodeAuth`
- `internal/services/agent/orchestrator.go:Orchestrator.ensureCodexAuth`
- `internal/services/agent/orchestrator.go:Orchestrator.prepareAgentAuthForRetry`
- `internal/services/agent/orchestrator.go:Orchestrator.retrySessionOnCredentialRateLimit`
- `internal/services/agent/runtime.go:runtimeController.Run`

### Trace F.2-T01: 5 candidates; family owner F.2; audit G.0

- `internal/api/handlers/session_thread_actions.go:SessionThreadHandler.RevertThread`
- `internal/api/handlers/sessions.go:SessionHandler.RetrySession`
- `internal/services/agent/orchestrator.go:Orchestrator.RevertThread`
- `internal/worker/handlers.go:handleSlackMissingContextModal`
- `internal/worker/thread_actions.go:newRevertSessionThreadHandler`

### Trace G.1-T01: 3 candidates; family owner G.1; audit G.0

- `internal/cluster/scheduler.go:Scheduler.runOnce`
- `internal/worker/worker.go:isLongRunningSessionJob`
- `internal/worker/worker.go:Worker.poll`

### Trace G.2-T01: 5 candidates; family owner G.2; audit G.0

- `internal/cluster/scheduler.go:Scheduler.Start`
- `internal/worker/handlers.go:RegisterHandlers`
- `internal/worker/session_executor_runtime.go:SessionExecutorRuntime.heartbeatLoop`
- `internal/worker/session_executor_runtime.go:SessionExecutorRuntime.startOwnershipLoops`
- `internal/worker/worker.go:Worker.Start`

## Assignment decisions

The following choices define primary accountability, not new runtime semantics:

1. A.4 owns legacy session-claim wrapper retirement/delegation; A.1 implements the one new ordinary admission engine and removes threaded dependence on a second parent claim.
2. B.1o owns monolithic reaper orchestration. Its snapshot/availability branches must call D.2/C.1w before the whole root closes; a function split can expose those review boundaries.
3. C.1w owns generic availability/descriptor CAS. D.2 owns retention tombstone/notice/delete intent; a generic setter is never retention authority.
4. C.1w owns prepared review-container/holder attachment and composes B.3d's exact review-owner change.
5. B.3t owns archive authorization/orchestration and calls D.2 for exact current-reference expiry. Successful archive or merged PR never grants blanket deletion authority.
6. D.3 owns existing cleanupReviewBundle as a distinct exact own-artifact workflow. It composes B.1 settlement and D.1 fresh-key requirements while retaining authoritative persistence/reference/unknown-I/O checks; it adds no provider-private export, general object ledger or blanket snapshot-resource deletion authority.
7. B.3c owns mixed runtime-stop orchestration; budget/recovery projections call B.1. A supplied reason enum cannot serve as authority.
8. B.1r owns retained PR transaction-wrapper composition. Actual admission callbacks remain A.5; a callback or wrapper cannot promote arbitrary lifecycle authority.
9. B.2 owns the shared live continuation fragment, retry adapters and automatic interrupted-worker recovery; B.1q owns full reclaim statements/lock-order conversion and waits for that fragment before whole-root closure. F.1 implements the one typed recovery operation they compose. F.2 keeps failed-only checkpoint/start-over eligibility; F.3 is offline normalization only.
10. D.1r's new registration/receipt core precedes C.1p. The eleven existing publication roots and eight syntax candidates close in D.1c only after C.1p/w are implemented, never at registration-core completion.

## Mechanical reconciliation signal for adoption

Before adopting or updating this table, compare its reviewed IDs as a set with inventory entries whose classification is unfenced-needs-conversion and canonical_target is present; compare all candidate IDs with the complementary unfenced set. Require exact equality, no duplicates, one primary owner per row, and count 255/353 only for this pinned baseline. Source changes require refreshed inventory fingerprints and explicit owner transfer/addition, not preservation of stale counts. No code/tests or runtime operations were executed to create this planning ledger.
