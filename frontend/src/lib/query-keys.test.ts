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

    it("validation includes session id", () => {
      expect(queryKeys.sessions.validation("s-1")).toEqual(["session", "s-1", "validation"]);
    });

    it("pr includes session id", () => {
      expect(queryKeys.sessions.pr("s-1")).toEqual(["session", "s-1", "pr"]);
    });

    it("messages includes session id", () => {
      expect(queryKeys.sessions.messages("s-1")).toEqual(["session", "s-1", "messages"]);
    });

    it("detail keys are distinct from list keys", () => {
      const list = queryKeys.sessions.list("s-1");
      const detail = queryKeys.sessions.detail("s-1");
      expect(list).not.toEqual(detail);
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
});
