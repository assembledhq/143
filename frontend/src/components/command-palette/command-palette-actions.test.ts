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

  it("hides member-only settings and eval actions for builders", () => {
    const actions = getFilteredActions("builder");
    expect(actions.find((a) => a.id === "settings-integrations")).toBeUndefined();
    expect(actions.find((a) => a.id === "settings-evals")).toBeUndefined();
    expect(actions.find((a) => a.id === "settings-team")).toBeUndefined();
    expect(actions.find((a) => a.id === "action-new-project")).toBeUndefined();
    expect(actions.find((a) => a.id === "action-new-eval")).toBeUndefined();
    expect(actions.find((a) => a.id === "settings-agents")).toBeDefined();
  });

  it("includes create preview for build-capable roles", () => {
    const actions = getFilteredActions("builder");
    expect(actions.find((a) => a.id === "action-create-preview")?.href).toBe("/previews/new");
    expect(getFilteredActions("viewer").find((a) => a.id === "action-create-preview")).toBeUndefined();
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

  it("global new session action does not preserve ambient repo context", () => {
    const newSessionAction = staticActions.find((a) => a.id === "action-new-session");
    expect(newSessionAction?.preserveRepo).toBeUndefined();
  });
});
