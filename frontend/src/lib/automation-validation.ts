export const AUTOMATION_GOAL_MAX_LENGTH = 8000;

export function automationGoalLengthState(goal: string): {
  isTooLong: boolean;
  countText: string;
  message: string | null;
} {
  const length = goal.length;
  return {
    isTooLong: length > AUTOMATION_GOAL_MAX_LENGTH,
    countText: `${length} / ${AUTOMATION_GOAL_MAX_LENGTH}`,
    message: length > AUTOMATION_GOAL_MAX_LENGTH
      ? `Goal must be at most ${AUTOMATION_GOAL_MAX_LENGTH} characters.`
      : null,
  };
}
