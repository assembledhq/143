import { describe, expect, it } from "vitest";
import { deriveSessionsRouteState } from "./sessions-route-state";

describe("deriveSessionsRouteState", () => {
  it("treats bare /sessions as the sidebar-first workspace route", () => {
    const state = deriveSessionsRouteState([]);

    expect(state).toEqual({
      mode: "index",
      selectedSessionId: null,
      isCreatingSession: false,
      isUnsupportedRoute: false,
      mobileShow: "sidebar",
      routeKey: "index",
    });
  });

  it("treats /sessions/new as create mode in the content pane", () => {
    const state = deriveSessionsRouteState(["new"]);

    expect(state).toEqual({
      mode: "create",
      selectedSessionId: null,
      isCreatingSession: true,
      isUnsupportedRoute: false,
      mobileShow: "content",
      routeKey: "new",
    });
  });

  it("treats any other first segment as the selected session id", () => {
    const state = deriveSessionsRouteState(["session-123"]);

    expect(state).toEqual({
      mode: "detail",
      selectedSessionId: "session-123",
      isCreatingSession: false,
      isUnsupportedRoute: false,
      mobileShow: "content",
      routeKey: "session:session-123",
    });
  });

  it("marks nested session routes as unsupported until explicitly modeled", () => {
    const state = deriveSessionsRouteState(["session-123", "diff"]);

    expect(state).toEqual({
      mode: "unsupported",
      selectedSessionId: null,
      isCreatingSession: false,
      isUnsupportedRoute: true,
      mobileShow: "content",
      routeKey: "unsupported:session-123/diff",
    });
  });
});
