// poll-intervals.ts — Central wrapper for UI polling and reconnect delays.
//
// Fast (< 10s) poll loops and backoff timers route their delays through
// pollMs() so the jsdom test setup can shrink them globally (see
// src/test/setup.ts). Tests that drive a component through poll-advanced
// state machines then observe transitions in tens of milliseconds instead
// of sitting through real multi-second poll cycles.
//
// Slow background polls (>= 10s) intentionally do not use this wrapper:
// they should stay quiet for the duration of a test, not fire mid-test.
export function pollMs(ms: number): number {
  return ms;
}
