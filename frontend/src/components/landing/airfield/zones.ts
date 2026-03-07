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
  // — First half: The Big Picture (control tower view) —
  {
    id: "everything-connects",
    number: "01",
    name: "Everything Connects",
    description:
      "Sentry errors, Linear tickets, support threads, and your product roadmap — the PM sees your entire production surface in one place",
    visual: "radar",
    progressStart: 0,
    progressEnd: 1 / 6,
    position: { x: 0.03, y: 0.65 },
  },
  {
    id: "projects",
    number: "02",
    name: "Projects",
    description:
      "Define big initiatives — migrations, refactors, new capabilities. The PM breaks them into sequenced tasks and chips away across days and weeks",
    visual: "airfield",
    progressStart: 1 / 6,
    progressEnd: 2 / 6,
    position: { x: 0.03, y: 0.65 },
  },
  {
    id: "you-guide-pm-decides",
    number: "03",
    name: "You Guide, PM Decides",
    description:
      "Set priorities in plain language — \"focus on auth this sprint\" or \"customer-reported bugs first.\" The PM balances your direction against bugs, projects, and tech debt",
    visual: "cockpit-launch",
    progressStart: 2 / 6,
    progressEnd: 3 / 6,
    position: { x: 0.03, y: 0.55 },
  },
  // — Second half: The Execution (cockpit view) —
  {
    id: "your-agents-execute",
    number: "04",
    name: "Your Agents Execute",
    description:
      "Bring your own coding agents — Claude Code, Codex, Gemini CLI, or any agent your team already uses. The PM dispatches work, your agents build it",
    visual: "cockpit-hud",
    progressStart: 3 / 6,
    progressEnd: 4 / 6,
    position: { x: 0.03, y: 0.55 },
  },
  {
    id: "production-validated",
    number: "05",
    name: "Production Validated",
    description:
      "Your CI pipeline, test suite, and security scans — not ours. Every change is held to your team's standards before it ships",
    visual: "cockpit-kill",
    progressStart: 4 / 6,
    progressEnd: 5 / 6,
    position: { x: 0.03, y: 0.55 },
  },
  {
    id: "ship-and-learn",
    number: "06",
    name: "Ship & Learn",
    description:
      "Validated PRs land linked to their original issues. Every review and production outcome makes the next cycle smarter",
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
