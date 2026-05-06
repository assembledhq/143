export const AUTOMATION_GOAL_MAX_LENGTH = 64000;

function formatAutomationGoalLength(value: number): string {
  return value.toLocaleString("en-US");
}

export function automationGoalLengthState(goal: string): {
  isTooLong: boolean;
  countText: string;
  message: string | null;
} {
  const length = goal.length;
  return {
    isTooLong: length > AUTOMATION_GOAL_MAX_LENGTH,
    countText: `${formatAutomationGoalLength(length)} / ${formatAutomationGoalLength(AUTOMATION_GOAL_MAX_LENGTH)}`,
    message: length > AUTOMATION_GOAL_MAX_LENGTH
      ? `Goal must be at most ${formatAutomationGoalLength(AUTOMATION_GOAL_MAX_LENGTH)} characters.`
      : null,
  };
}
