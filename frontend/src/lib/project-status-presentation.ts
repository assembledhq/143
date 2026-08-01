import type { OperationalStatePresentation } from "@/lib/operational-state";
import type { ProjectStatus } from "@/lib/types";

const projectStatusPresentation: Record<ProjectStatus, OperationalStatePresentation> = {
  draft: { label: "Draft", tone: "neutral", activity: "none", attention: "none" },
  active: { label: "Active", tone: "info", activity: "breathing", attention: "none" },
  completed: { label: "Done", tone: "success", activity: "none", attention: "none" },
};

export function deriveProjectStatusPresentation(status: ProjectStatus): OperationalStatePresentation {
  return projectStatusPresentation[status];
}
