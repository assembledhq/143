export type PRSnapshotState = "expired" | "not_captured" | "unavailable";

export const SNAPSHOT_EXPIRED_PR_MESSAGE =
  "This session snapshot expired before a PR could be created. Send a new message to rebuild the sandbox, then create the PR again.";
export const SNAPSHOT_NOT_CAPTURED_PR_MESSAGE =
  "This session finished without saving a reusable checkpoint for PR creation. Send a new message to rebuild the sandbox, then create the PR again.";
export const SNAPSHOT_UNAVAILABLE_PR_MESSAGE =
  "This session had a saved checkpoint, but it is no longer available in storage. Send a new message to rebuild the sandbox, then create the PR again.";

export function classifyPRSnapshotState({
  sessionSnapshotKey,
  sessionSandboxState,
  serverMessage,
  localCode,
  allowImplicitMissingSnapshot = false,
}: {
  sessionSnapshotKey?: string | null;
  sessionSandboxState?: string | null;
  serverMessage?: string | null;
  localCode?: string;
  allowImplicitMissingSnapshot?: boolean;
}): PRSnapshotState | null {
  if (localCode === "SNAPSHOT_EXPIRED") return "expired";
  if (localCode === "SNAPSHOT_NOT_CAPTURED") return "not_captured";
  if (localCode === "SNAPSHOT_UNAVAILABLE") return "unavailable";
  if (serverMessage === SNAPSHOT_EXPIRED_PR_MESSAGE) return "expired";
  if (serverMessage === SNAPSHOT_NOT_CAPTURED_PR_MESSAGE) return "not_captured";
  if (serverMessage === SNAPSHOT_UNAVAILABLE_PR_MESSAGE) return "unavailable";
  if (/^session state expired\b/i.test(serverMessage || "")) return "unavailable";
  if (!sessionSnapshotKey) {
    if (!allowImplicitMissingSnapshot) return null;
    return sessionSandboxState === "destroyed" ? "expired" : "not_captured";
  }
  return null;
}

export function snapshotPRMessage(state: PRSnapshotState | null, message?: string | null): string {
  if (message && !/^session state expired\b/i.test(message)) {
    return message;
  }
  switch (state) {
    case "expired":
      return SNAPSHOT_EXPIRED_PR_MESSAGE;
    case "not_captured":
      return SNAPSHOT_NOT_CAPTURED_PR_MESSAGE;
    case "unavailable":
      return SNAPSHOT_UNAVAILABLE_PR_MESSAGE;
    default:
      return SNAPSHOT_UNAVAILABLE_PR_MESSAGE;
  }
}

export function prErrorTitle(snapshotState: PRSnapshotState | null, errorCode?: string): string {
  if (snapshotState === "expired" || errorCode === "SNAPSHOT_EXPIRED") {
    return "Session snapshot expired";
  }
  if (snapshotState === "not_captured" || errorCode === "SNAPSHOT_NOT_CAPTURED") {
    return "No reusable checkpoint saved";
  }
  if (snapshotState === "unavailable" || errorCode === "SNAPSHOT_UNAVAILABLE") {
    return "Saved checkpoint unavailable";
  }
  if (errorCode === "PR_RESUME_EXPIRED") {
    return "Couldn't resume PR creation";
  }
  return "Couldn't create the PR";
}
