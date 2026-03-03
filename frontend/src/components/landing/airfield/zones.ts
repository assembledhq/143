export interface Zone {
  id: string;
  number: string;
  name: string;
  description: string;
  visual: string;
  progressStart: number;
  progressEnd: number;
  /** Normalized 0-1 position on the airfield canvas */
  position: { x: number; y: number };
}

export const ZONES: Zone[] = [
  {
    id: "issues-flow-in",
    number: "01",
    name: "Issues Flow In",
    description:
      "Sentry errors, Linear tickets, and support issues accumulate automatically",
    visual: "radar",
    progressStart: 0,
    progressEnd: 1 / 6,
    position: { x: 0.03, y: 0.65 },
  },
  {
    id: "pm-agent-plans",
    number: "02",
    name: "PM Agent Plans",
    description:
      "An AI product manager analyzes all issues, clusters related problems, and builds a prioritized plan",
    visual: "airfield",
    progressStart: 1 / 6,
    progressEnd: 2 / 6,
    position: { x: 0.03, y: 0.65 },
  },
  {
    id: "agents-execute",
    number: "03",
    name: "Agents Execute",
    description:
      "Coding agents spin up with full codebase context and the PM\u2019s approach for each task",
    visual: "cockpit-launch",
    progressStart: 2 / 6,
    progressEnd: 3 / 6,
    position: { x: 0.03, y: 0.55 },
  },
  {
    id: "changes-validated",
    number: "04",
    name: "Changes Validated",
    description:
      "Test suite, security scans, and quality checks confirm every change works",
    visual: "cockpit-hud",
    progressStart: 3 / 6,
    progressEnd: 4 / 6,
    position: { x: 0.03, y: 0.55 },
  },
  {
    id: "prs-shipped",
    number: "05",
    name: "PRs Shipped",
    description:
      "Validated fixes and improvements land in your repo, linked to original issues",
    visual: "cockpit-kill",
    progressStart: 4 / 6,
    progressEnd: 5 / 6,
    position: { x: 0.03, y: 0.55 },
  },
  {
    id: "system-learns",
    number: "06",
    name: "System Learns",
    description:
      "Every PR review and production outcome makes the next run smarter",
    visual: "aerial-return",
    progressStart: 5 / 6,
    progressEnd: 1,
    position: { x: 0.03, y: 0.65 },
  },
];

/** Returns the index of the active zone for a given progress, or -1 if none */
export function getActiveZone(progress: number): number {
  for (let i = 0; i < ZONES.length; i++) {
    if (progress >= ZONES[i].progressStart && progress < ZONES[i].progressEnd) {
      return i;
    }
  }
  if (progress >= 1) return ZONES.length - 1;
  return -1;
}

/** Returns 0-1 progress within a specific zone */
export function getZoneProgress(progress: number, zoneIndex: number): number {
  const zone = ZONES[zoneIndex];
  if (!zone) return 0;
  const range = zone.progressEnd - zone.progressStart;
  if (range <= 0) return 0;
  const local = (progress - zone.progressStart) / range;
  return Math.min(1, Math.max(0, local));
}
