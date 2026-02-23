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
    id: "comms-tower",
    number: "01",
    name: "Comms Tower",
    description: "Issue ingestion — Sentry, Linear, alerts arrive",
    visual: "antenna",
    progressStart: 0,
    progressEnd: 1 / 6,
    position: { x: 0.15, y: 0.2 },
  },
  {
    id: "briefing-room",
    number: "02",
    name: "Briefing Room",
    description: "Agent analysis — reads codebase, understands the bug",
    visual: "screens",
    progressStart: 1 / 6,
    progressEnd: 2 / 6,
    position: { x: 0.35, y: 0.35 },
  },
  {
    id: "hangar",
    number: "03",
    name: "Hangar",
    description: "Fix generation — sandboxed environment builds the patch",
    visual: "hangar",
    progressStart: 2 / 6,
    progressEnd: 3 / 6,
    position: { x: 0.55, y: 0.55 },
  },
  {
    id: "test-strip",
    number: "04",
    name: "Test Strip",
    description: "Validation — automated tests confirm the fix",
    visual: "checkmarks",
    progressStart: 3 / 6,
    progressEnd: 4 / 6,
    position: { x: 0.72, y: 0.42 },
  },
  {
    id: "launch-pad",
    number: "05",
    name: "Launch Pad",
    description: "PR submission — fix shipped to your repo",
    visual: "runway",
    progressStart: 4 / 6,
    progressEnd: 5 / 6,
    position: { x: 0.85, y: 0.25 },
  },
  {
    id: "control-tower",
    number: "06",
    name: "Control Tower",
    description: "Feedback loop — learns from code reviews",
    visual: "tower",
    progressStart: 5 / 6,
    progressEnd: 1,
    position: { x: 0.75, y: 0.75 },
  },
];

/** Returns the index of the active zone for a given progress, or -1 if none */
export function getActiveZone(progress: number): number {
  for (let i = 0; i < ZONES.length; i++) {
    if (progress >= ZONES[i].progressStart && progress < ZONES[i].progressEnd) {
      return i;
    }
  }
  // At exactly 1.0, last zone is active
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
