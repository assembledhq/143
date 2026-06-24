import { describe, expect, it } from "vitest";

import {
  AUTOMATION_GOAL_MAX_LENGTH,
  automationGoalLengthState,
} from "./automation-validation";

describe("automationGoalLengthState", () => {
  it("returns a valid state with a formatted count under the limit", () => {
    expect(automationGoalLengthState("Ship it")).toEqual({
      isTooLong: false,
      countText: "7 / 64,000",
      message: null,
    });
  });

  it("treats the exact maximum as valid", () => {
    const state = automationGoalLengthState("a".repeat(AUTOMATION_GOAL_MAX_LENGTH));

    expect(state).toEqual({
      isTooLong: false,
      countText: "64,000 / 64,000",
      message: null,
    });
  });

  it("returns a validation message above the limit", () => {
    const state = automationGoalLengthState("a".repeat(AUTOMATION_GOAL_MAX_LENGTH + 1));

    expect(state).toEqual({
      isTooLong: true,
      countText: "64,001 / 64,000",
      message: "Goal must be at most 64,000 characters.",
    });
  });
});
