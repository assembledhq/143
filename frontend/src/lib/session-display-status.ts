import { prMergedAccent } from "./pr-status-styles";
import { workingSet } from "./session-status-groups";
import type { PRCreationState, PRPushState, Session, SessionStatus } from "./types";

export type SessionDisplayStatusKind = "session" | "pr_creation" | "pr_push";

export type SessionDisplayStatus = {
  kind: SessionDisplayStatusKind;
  label: string;
  dotClass: string;
  textClass: string;
  bgClass: string;
  animated: boolean;
};

const sessionStatusConfig: Record<SessionStatus, Omit<SessionDisplayStatus, "kind" | "animated">> = {
  pending: { dotClass: "bg-muted-foreground/50", textClass: "text-muted-foreground", bgClass: "bg-muted", label: "Pending" },
  running: { dotClass: "bg-primary", textClass: "text-primary", bgClass: "bg-primary/10", label: "Running" },
  idle: { dotClass: "bg-primary", textClass: "text-primary", bgClass: "bg-primary/10", label: "Idle" },
  awaiting_input: { dotClass: "bg-warning", textClass: "text-warning", bgClass: "bg-warning/10", label: "Awaiting input" },
  needs_human_guidance: { dotClass: "bg-attention", textClass: "text-attention", bgClass: "bg-attention/10", label: "Needs guidance" },
  completed: { dotClass: "bg-success", textClass: "text-success", bgClass: "bg-success/10", label: "Completed" },
  pr_created: { dotClass: prMergedAccent.dot, textClass: prMergedAccent.text, bgClass: prMergedAccent.bg, label: "PR created" },
  failed: { dotClass: "bg-destructive", textClass: "text-destructive", bgClass: "bg-destructive/10", label: "Failed" },
  cancelled: { dotClass: "bg-muted-foreground/50", textClass: "text-muted-foreground", bgClass: "bg-muted", label: "Cancelled" },
  skipped: { dotClass: "bg-muted-foreground/30", textClass: "text-muted-foreground", bgClass: "bg-muted", label: "Skipped" },
};

const prActionStatus = {
  dotClass: "bg-primary",
  textClass: "text-primary",
  bgClass: "bg-primary/10",
};

function isInFlightState(state?: PRCreationState | PRPushState): boolean {
  return state === "queued" || state === "pushing";
}

export function deriveSessionDisplayStatus(session: Pick<Session, "status" | "pr_creation_state" | "pr_push_state">): SessionDisplayStatus {
  if (isInFlightState(session.pr_push_state)) {
    return {
      kind: "pr_push",
      label: "Pushing changes",
      animated: true,
      ...prActionStatus,
    };
  }

  if (isInFlightState(session.pr_creation_state)) {
    return {
      kind: "pr_creation",
      label: "Creating PR",
      animated: true,
      ...prActionStatus,
    };
  }

  const config = sessionStatusConfig[session.status] ?? sessionStatusConfig.pending;
  return {
    kind: "session",
    animated: workingSet.has(session.status),
    ...config,
  };
}
