import { describe, expect, it } from "vitest";
import { queryKeys } from "./query-keys";

describe("queryKeys", () => {
  describe("sessions", () => {
    it("all returns static key", () => {
      expect(queryKeys.sessions.all).toEqual(["sessions"]);
    });

    it("list includes optional repo filter", () => {
      expect(queryKeys.sessions.list()).toEqual(["sessions", undefined]);
      expect(queryKeys.sessions.list("my-repo")).toEqual(["sessions", "my-repo"]);
      expect(queryKeys.sessions.list(null)).toEqual(["sessions", null]);
    });

    it("detail includes session id", () => {
      expect(queryKeys.sessions.detail("s-1")).toEqual(["session", "s-1"]);
    });

    it("timeline includes session id", () => {
      expect(queryKeys.sessions.timeline("s-1")).toEqual(["session", "s-1", "timeline"]);
    });

    it("pr includes session id", () => {
      expect(queryKeys.sessions.pr("s-1")).toEqual(["session", "s-1", "pr"]);
    });

    it("messages includes session id", () => {
      expect(queryKeys.sessions.messages("s-1")).toEqual(["session", "s-1", "messages"]);
    });

    it("threads includes session id", () => {
      expect(queryKeys.sessions.threads("s-1")).toEqual(["session", "s-1", "threads"]);
    });

    it("threadDetail includes session and thread id", () => {
      expect(queryKeys.sessions.threadDetail("s-1", "t-1")).toEqual(["session", "s-1", "thread", "t-1"]);
    });

    it("threadMessages includes session and thread id", () => {
      expect(queryKeys.sessions.threadMessages("s-1", "t-1")).toEqual(["session", "s-1", "thread", "t-1", "messages"]);
    });

    it("threadLogs includes session and thread id", () => {
      expect(queryKeys.sessions.threadLogs("s-1", "t-1")).toEqual(["session", "s-1", "thread", "t-1", "logs"]);
    });

    it("threadRecoverableInbox includes session and thread id", () => {
      expect(queryKeys.sessions.threadRecoverableInbox("s-1", "t-1")).toEqual(["session", "s-1", "thread", "t-1", "recoverable-inbox"]);
    });

    it("reviewLoops includes session id", () => {
      expect(queryKeys.sessions.reviewLoops("s-1")).toEqual(["session", "s-1", "review-loops"]);
    });

    it("detail keys are distinct from list keys", () => {
      const list = queryKeys.sessions.list("s-1");
      const detail = queryKeys.sessions.detail("s-1");
      expect(list).not.toEqual(detail);
    });
  });

  describe("repositories", () => {
    it("all returns static key", () => {
      expect(queryKeys.repositories.all).toEqual(["repositories"]);
    });

    it("branches includes repository id", () => {
      expect(queryKeys.repositories.branches("repo-1")).toEqual(["repositories", "repo-1", "branches"]);
    });
  });

  describe("settings", () => {
    it("all returns static key", () => {
      expect(queryKeys.settings.all).toEqual(["settings"]);
    });
  });

  describe("team", () => {
    it("members returns static key", () => {
      expect(queryKeys.team.members).toEqual(["team", "members"]);
    });
  });

  describe("evals", () => {
    it("tasks returns key with optional params", () => {
      expect(queryKeys.evals.tasks()).toEqual(["evals", "tasks", undefined]);
      expect(queryKeys.evals.tasks({ source: "manual" })).toEqual(["evals", "tasks", { source: "manual" }]);
    });

    it("taskDetail includes task id", () => {
      expect(queryKeys.evals.taskDetail("t-1")).toEqual(["evals", "task", "t-1"]);
    });

    it("runs includes task id", () => {
      expect(queryKeys.evals.runs("t-1")).toEqual(["evals", "task", "t-1", "runs"]);
    });

    it("runDetail includes run id", () => {
      expect(queryKeys.evals.runDetail("r-1")).toEqual(["evals", "run", "r-1"]);
    });

    it("batches returns static key", () => {
      expect(queryKeys.evals.batches).toEqual(["evals", "batches"]);
    });

    it("batch includes batch id", () => {
      expect(queryKeys.evals.batch("b-1")).toEqual(["evals", "batch", "b-1"]);
    });

    it("bootstrapCandidates returns static key", () => {
      expect(queryKeys.evals.bootstrapCandidates).toEqual(["evals", "bootstrap", "candidates"]);
    });
  });
});
