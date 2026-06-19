import { afterAll, vi } from 'vitest';

// The node project runs with isolate:false (vitest.config.ts), so files share
// one module graph per worker. Reset it after each file so vi.mock factories
// and module-level state cannot leak across files (see setup.ts for the jsdom
// equivalent, which also restores shared window state).
afterAll(() => {
  vi.resetModules();
});
