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
  if (errorCode === "SNAPSHOT_PENDING") {
    return "Snapshot still saving";
  }
  if (errorCode === "SESSION_RUNNING") {
    return "Session still running";
  }
  if (errorCode === "SNAPSHOT_NOT_QUIESCENT") {
    return "Active tabs still running";
  }
  return "Couldn't create the PR";
}

export function formatPRCreationError(errorCode?: string, message?: string | null): string {
  const trimmedMessage = message?.trim();
  const useCoordinatorFallback = !trimmedMessage ||
    /^pull request publication request was rejected[.!]?$/i.test(trimmedMessage);

  if (useCoordinatorFallback && errorCode === "WORKSPACE_NOT_READY") {
    return "This workspace is not ready to publish a pull request. Review its status, then try again.";
  }
  if (useCoordinatorFallback && errorCode === "SESSION_NOT_PUBLICATION_ELIGIBLE") {
    return "This session cannot create a pull request in its current state.";
  }

  if (!trimmedMessage) {
    return "Something went wrong while creating the pull request. Try again.";
  }

  const sentenceCasedMessage = `${trimmedMessage.charAt(0).toLocaleUpperCase()}${trimmedMessage.slice(1)}`;
  return /[.!?]$/.test(sentenceCasedMessage)
    ? sentenceCasedMessage
    : `${sentenceCasedMessage}.`;
}
