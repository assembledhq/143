import { describe, expect, it } from "vitest";
import {
  SNAPSHOT_EXPIRED_PR_MESSAGE,
  SNAPSHOT_NOT_CAPTURED_PR_MESSAGE,
  SNAPSHOT_UNAVAILABLE_PR_MESSAGE,
  classifyPRSnapshotState,
  formatPRCreationError,
  prErrorTitle,
  snapshotPRMessage,
} from "./session-pr-snapshot";

describe("session-pr-snapshot", () => {
  describe("classifyPRSnapshotState", () => {
    it("prefers explicit local error codes", () => {
      expect(classifyPRSnapshotState({ localCode: "SNAPSHOT_EXPIRED" })).toBe("expired");
      expect(classifyPRSnapshotState({ localCode: "SNAPSHOT_NOT_CAPTURED" })).toBe("not_captured");
      expect(classifyPRSnapshotState({ localCode: "SNAPSHOT_UNAVAILABLE" })).toBe("unavailable");
    });

    it("recognizes canonical backend snapshot messages", () => {
      expect(classifyPRSnapshotState({ serverMessage: SNAPSHOT_EXPIRED_PR_MESSAGE })).toBe("expired");
      expect(classifyPRSnapshotState({ serverMessage: SNAPSHOT_NOT_CAPTURED_PR_MESSAGE })).toBe("not_captured");
      expect(classifyPRSnapshotState({ serverMessage: SNAPSHOT_UNAVAILABLE_PR_MESSAGE })).toBe("unavailable");
    });

    it("treats legacy session state expired copy as unavailable", () => {
      expect(classifyPRSnapshotState({ serverMessage: "session state expired while creating PR" })).toBe("unavailable");
    });

    it("classifies implicit missing snapshots only when allowed", () => {
      expect(
        classifyPRSnapshotState({
          sessionSnapshotKey: null,
          sessionSandboxState: "destroyed",
          allowImplicitMissingSnapshot: true,
        }),
      ).toBe("expired");

      expect(
        classifyPRSnapshotState({
          sessionSnapshotKey: null,
          sessionSandboxState: "idle",
          allowImplicitMissingSnapshot: true,
        }),
      ).toBe("not_captured");

      expect(
        classifyPRSnapshotState({
          sessionSnapshotKey: null,
          sessionSandboxState: "destroyed",
          allowImplicitMissingSnapshot: false,
        }),
      ).toBeNull();
    });

    it("does not infer a failure when a snapshot key exists", () => {
      expect(
        classifyPRSnapshotState({
          sessionSnapshotKey: "snap-123",
          sessionSandboxState: "destroyed",
          allowImplicitMissingSnapshot: true,
        }),
      ).toBeNull();
    });
  });

  describe("snapshotPRMessage", () => {
    it("returns explicit non-legacy backend messages unchanged", () => {
      expect(snapshotPRMessage("expired", "custom backend detail")).toBe("custom backend detail");
    });

    it("maps snapshot states to canonical fallback copy", () => {
      expect(snapshotPRMessage("expired")).toBe(SNAPSHOT_EXPIRED_PR_MESSAGE);
      expect(snapshotPRMessage("not_captured")).toBe(SNAPSHOT_NOT_CAPTURED_PR_MESSAGE);
      expect(snapshotPRMessage("unavailable")).toBe(SNAPSHOT_UNAVAILABLE_PR_MESSAGE);
      expect(snapshotPRMessage(null)).toBe(SNAPSHOT_UNAVAILABLE_PR_MESSAGE);
    });

    it("replaces legacy session state expired copy with the normalized message", () => {
      expect(snapshotPRMessage("expired", "session state expired while creating PR")).toBe(
        SNAPSHOT_EXPIRED_PR_MESSAGE,
      );
    });
  });

  describe("prErrorTitle", () => {
    it("maps snapshot states to the user-facing title", () => {
      expect(prErrorTitle("expired")).toBe("Session snapshot expired");
      expect(prErrorTitle("not_captured")).toBe("No reusable checkpoint saved");
      expect(prErrorTitle("unavailable")).toBe("Saved checkpoint unavailable");
    });

    it("maps non-snapshot PR resume errors separately", () => {
      expect(prErrorTitle(null, "PR_RESUME_EXPIRED")).toBe("Couldn't resume PR creation");
    });

    it("maps snapshot quiescence errors to actionable titles", () => {
      expect(prErrorTitle(null, "SNAPSHOT_PENDING")).toBe("Snapshot still saving");
      expect(prErrorTitle(null, "SESSION_RUNNING")).toBe("Session still running");
      expect(prErrorTitle(null, "SNAPSHOT_NOT_QUIESCENT")).toBe("Active tabs still running");
    });

    it("falls back to the generic PR creation title", () => {
      expect(prErrorTitle(null, "SOMETHING_ELSE")).toBe("Couldn't create the PR");
    });
  });

  describe("formatPRCreationError", () => {
    it.each([
      {
        name: "workspace rejection",
        code: "WORKSPACE_NOT_READY",
        message: "pull request publication request was rejected",
        expected: "This workspace is not ready to publish a pull request. Review its status, then try again.",
      },
      {
        name: "ineligible session",
        code: "SESSION_NOT_PUBLICATION_ELIGIBLE",
        message: "pull request publication request was rejected",
        expected: "This session cannot create a pull request in its current state.",
      },
      {
        name: "legacy lowercase detail",
        code: undefined,
        message: "pull request was rejected",
        expected: "Pull request was rejected.",
      },
      {
        name: "already polished detail",
        code: undefined,
        message: "GitHub rejected the branch push.",
        expected: "GitHub rejected the branch push.",
      },
      {
        name: "specific coordinator detail",
        code: "WORKSPACE_NOT_READY",
        message: "The working branch is no longer available.",
        expected: "The working branch is no longer available.",
      },
      {
        name: "missing detail",
        code: undefined,
        message: "  ",
        expected: "Something went wrong while creating the pull request. Try again.",
      },
    ])("formats $name as polished product copy", ({ code, message, expected }) => {
      expect(formatPRCreationError(code, message)).toBe(expected);
    });
  });
});
