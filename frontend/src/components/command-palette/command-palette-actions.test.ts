import { describe, it, expect } from "vitest";
import { getFilteredActions, staticActions } from "./command-palette-actions";

describe("command-palette-actions", () => {
  it("includes audit log for admin users", () => {
    const actions = getFilteredActions("admin");
    expect(actions.find((a) => a.id === "settings-audit-log")).toBeDefined();
  });

  it("excludes audit log for non-admin users", () => {
    const actions = getFilteredActions("member");
    expect(actions.find((a) => a.id === "settings-audit-log")).toBeUndefined();
  });

  it("excludes audit log for viewer users", () => {
    const actions = getFilteredActions("viewer");
    expect(actions.find((a) => a.id === "settings-audit-log")).toBeUndefined();
  });

  it("all static actions have unique ids", () => {
    const ids = staticActions.map((a) => a.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("navigation actions have href", () => {
    const navActions = staticActions.filter((a) => a.group === "navigation");
    for (const action of navActions) {
      expect(action.href).toBeDefined();
    }
  });

  it("repo-scoped navigation actions have preserveRepo", () => {
    const sessionsAction = staticActions.find((a) => a.id === "nav-sessions");
    expect(sessionsAction?.preserveRepo).toBe(true);
    const projectsAction = staticActions.find((a) => a.id === "nav-projects");
    expect(projectsAction?.preserveRepo).toBe(true);
  });
});
