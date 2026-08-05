import type { OperationalStatePresentation, OperationalTone } from "@/lib/operational-state";
import type { SessionThread } from "@/lib/types";

// Presentation rules shared by the desktop tab strip and the mobile actions
// sheet. Keeping them here stops a thread from reading as healthy on one
// surface while it reads as troubled on the other.

export function isActiveThreadStatus(status: string): boolean {
  return status === "pending" || status === "running" || status === "awaiting_input";
}

export function isThreadUnread(thread: SessionThread, viewedThreadIds: ReadonlySet<string>): boolean {
  return !viewedThreadIds.has(thread.id);
}

export function threadNeedsAttention(thread: SessionThread): boolean {
  if (thread.status === "awaiting_input" || thread.status === "failed") {
    return true;
  }
  return (
    !isActiveThreadStatus(thread.status) &&
    !!(thread.failure_explanation || thread.failure_category)
  );
}

/**
 * A settled thread can still carry a failure its status alone doesn't express.
 * Fold that into the one dot rather than trailing a second dot after the label.
 */
export function threadIndicatorTone(
  presentation: OperationalStatePresentation,
  needsAttention: boolean,
): OperationalTone {
  if (!needsAttention) {
    return presentation.tone;
  }
  // Statuses that already read as trouble (failed, awaiting input) keep their
  // own tone; only the reassuring ones get escalated.
  return presentation.tone === "neutral" || presentation.tone === "success"
    ? "warning"
    : presentation.tone;
}

/**
 * Suffixes a thread's accessible name so unread/attention survive the
 * aria-hidden dot. Each surface appends it where its own name comes from:
 * the desktop tab builds its name from content (sr-only child), while the
 * mobile row carries an explicit aria-label.
 */
export function threadStateSuffix(needsAttention: boolean, isUnread: boolean): string {
  return `${needsAttention ? " (needs attention)" : ""}${isUnread ? " (unread)" : ""}`;
}

/**
 * Whether a thread's label should read as prominent. Unread threads, the lane
 * the user is on, and lanes that are still working stay bright; settled lanes
 * the user has already seen recede. This mirrors the session sidebar, which
 * keeps working rows bright even once they have been read.
 *
 * Each surface maps the result onto its own base colour — the desktop strip
 * brightens above a dimmed tab colour, the mobile sheet mutes below a bright
 * one — but the rule itself lives here so the two cannot drift apart.
 */
export function isThreadLabelProminent(
  thread: SessionThread,
  { isUnread, isActive }: { isUnread: boolean; isActive: boolean },
): boolean {
  return isUnread || isActive || isActiveThreadStatus(thread.status);
}

export function canArchiveThread(thread: SessionThread, threadCount: number): boolean {
  return threadCount > 1 && !isActiveThreadStatus(thread.status);
}
