export type OperationalTone =
  | "neutral"
  | "primary"
  | "success"
  | "warning"
  | "attention"
  | "info"
  | "destructive";

export type ActivityTreatment =
  | "none"
  | "breathing"
  | "indeterminate"
  | "transitioning";

export type AttentionLevel =
  | "none"
  | "informational"
  | "action_required"
  | "blocking";

/**
 * Shared presentation contract for long-lived operational state.
 *
 * Tone communicates meaning, activity communicates whether work is actually
 * progressing, and attention communicates whether the user must act. Keeping
 * the axes independent prevents "important" from accidentally reading as
 * "working" and keeps waiting states visually still.
 */
export type OperationalStatePresentation = {
  label: string;
  tone: OperationalTone;
  activity: ActivityTreatment;
  attention: AttentionLevel;
  detail?: string;
};
