import type { OperationalStatePresentation } from "./operational-state";
import type { PRCreationState, PRPushState, PullRequestStatus, Session, SessionStatus } from "./types";

export type SessionDisplayStatusKind = "session" | "pull_request" | "pr_creation" | "pr_push";

export type SessionDisplayStatus = OperationalStatePresentation & {
  kind: SessionDisplayStatusKind;
};

const sessionStatusConfig: Record<SessionStatus, OperationalStatePresentation> = {
  pending: { label: "Starting", tone: "neutral", activity: "indeterminate", attention: "none" },
  running: { label: "Running", tone: "primary", activity: "breathing", attention: "none" },
  idle: { label: "Ready to continue", tone: "neutral", activity: "none", attention: "informational" },
  awaiting_input: { label: "Waiting for you", tone: "warning", activity: "none", attention: "action_required" },
  needs_human_guidance: { label: "Needs guidance", tone: "attention", activity: "none", attention: "blocking" },
  completed: { label: "Completed", tone: "success", activity: "none", attention: "none" },
  pr_created: { label: "PR created", tone: "success", activity: "none", attention: "none" },
  failed: { label: "Failed", tone: "destructive", activity: "none", attention: "blocking" },
  cancelled: { label: "Cancelled", tone: "neutral", activity: "none", attention: "none" },
  skipped: { label: "Skipped", tone: "neutral", activity: "none", attention: "none" },
};

const prActionStatus: Omit<OperationalStatePresentation, "label"> = {
  tone: "primary",
  activity: "indeterminate",
  attention: "none",
};

function isInFlightState(state?: PRCreationState | PRPushState): boolean {
  return state === "queued" || state === "pushing";
}

export function deriveSessionStatusPresentation(status: SessionStatus): OperationalStatePresentation {
  return sessionStatusConfig[status] ?? sessionStatusConfig.pending;
}

export function deriveSessionDisplayStatus(
  session: Pick<Session, "status" | "pr_creation_state" | "pr_push_state">,
  prStatus?: PullRequestStatus | null,
): SessionDisplayStatus {
  if (session.status === "pr_created" && prStatus === "merged") {
    return {
      kind: "pull_request",
      label: "PR merged",
      tone: "success",
      activity: "none",
      attention: "none",
    };
  }

  if (session.status === "pr_created" && prStatus === "closed") {
    return {
      kind: "pull_request",
      label: "PR closed",
      tone: "neutral",
      activity: "none",
      attention: "none",
    };
  }

  if (isInFlightState(session.pr_push_state)) {
    return {
      kind: "pr_push",
      label: "Pushing changes",
      ...prActionStatus,
    };
  }

  if (isInFlightState(session.pr_creation_state)) {
    return {
      kind: "pr_creation",
      label: "Creating PR",
      ...prActionStatus,
    };
  }

  const config = deriveSessionStatusPresentation(session.status);
  return {
    kind: "session",
    ...config,
  };
}
