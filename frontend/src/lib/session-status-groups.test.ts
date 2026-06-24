import { describe, expect, it } from "vitest";

import {
  activeSet,
  activeStatuses,
  doneStatuses,
  filterToStatusParam,
  workingSet,
  workingStatusesSet,
} from "./session-status-groups";

describe("session status groups", () => {
  it("exposes active and done status sets used by filters", () => {
    expect(activeSet.has("running")).toBe(true);
    expect(activeSet.has("completed")).toBe(false);
    expect(workingSet.has("running")).toBe(true);
    expect(workingSet.has("awaiting_input")).toBe(false);
    expect(workingStatusesSet.has("awaiting_input")).toBe(true);
    expect(doneStatuses).toContain("pr_created");
  });
});

describe("filterToStatusParam", () => {
  it("omits the status parameter for empty, all, and passthrough filters", () => {
    expect(filterToStatusParam(null)).toBeUndefined();
    expect(filterToStatusParam("all")).toBeUndefined();
    expect(filterToStatusParam("mine", ["mine"])).toBeUndefined();
  });

  it("maps active and done filters to comma-separated status groups", () => {
    expect(filterToStatusParam("active")).toBe(activeStatuses.join(","));
    expect(filterToStatusParam("done")).toBe(doneStatuses.join(","));
  });

  it("passes custom status filters through", () => {
    expect(filterToStatusParam("failed")).toBe("failed");
  });
});
