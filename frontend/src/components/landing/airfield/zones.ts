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
    id: "alert-received",
    number: "01",
    name: "Alert Received",
    description:
      "Sentry errors, Linear tickets, and PagerDuty alerts are ingested automatically",
    visual: "radar",
    progressStart: 0,
    progressEnd: 1 / 6,
    position: { x: 0.03, y: 0.65 },
  },
  {
    id: "agent-activated",
    number: "02",
    name: "Agent Activated",
    description:
      "Agent spins up, loads the codebase, and pulls in issue context",
    visual: "airfield",
    progressStart: 1 / 6,
    progressEnd: 2 / 6,
    position: { x: 0.03, y: 0.65 },
  },
  {
    id: "root-cause-analysis",
    number: "03",
    name: "Root Cause Analysis",
    description:
      "Agent navigates the code, tracing the bug to its source",
    visual: "cockpit-launch",
    progressStart: 2 / 6,
    progressEnd: 3 / 6,
    position: { x: 0.03, y: 0.55 },
  },
  {
    id: "fix-generated",
    number: "04",
    name: "Fix Generated",
    description:
      "Root cause identified, patch built in a sandboxed environment",
    visual: "cockpit-hud",
    progressStart: 3 / 6,
    progressEnd: 4 / 6,
    position: { x: 0.03, y: 0.55 },
  },
  {
    id: "tests-passing",
    number: "05",
    name: "Tests Passing",
    description:
      "Your existing test suite runs against the fix and confirms it works",
    visual: "cockpit-kill",
    progressStart: 4 / 6,
    progressEnd: 5 / 6,
    position: { x: 0.03, y: 0.55 },
  },
  {
    id: "pr-merged",
    number: "06",
    name: "PR Merged",
    description:
      "Fix shipped to your repo. Agent learns from code review feedback",
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
