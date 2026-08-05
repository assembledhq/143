import { afterAll, vi } from 'vitest';

// Keep explicit module cleanup for focused runs and future pool/config changes.
afterAll(() => {
  vi.resetModules();
});
