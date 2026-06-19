import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterAll, afterEach, beforeAll, vi } from 'vitest';
import { server } from './mocks/server';

(
  globalThis as typeof globalThis & {
    IS_REACT_ACT_ENVIRONMENT?: boolean;
  }
).IS_REACT_ACT_ENVIRONMENT = true;

// Shrink polling/backoff delays so tests that wait on poll-driven state
// transitions (PR creation, thread refetch, SSE reconnect, debounced
// inputs) resolve in tens of milliseconds instead of sitting through real
// multi-second cycles. waitFor() polls every 50ms, so a 50ms floor keeps
// the shrunk delays observable without busy-looping.
vi.mock('@/lib/poll-intervals', () => ({
  pollMs: (ms: number) => Math.max(50, Math.round(ms / 20)),
}));

// Polyfill ResizeObserver for JSDOM (used by Radix UI Slider)
globalThis.ResizeObserver ??= class ResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof globalThis.ResizeObserver;

if (!Element.prototype.hasPointerCapture) {
  Element.prototype.hasPointerCapture = () => false;
}

if (!Element.prototype.setPointerCapture) {
  Element.prototype.setPointerCapture = () => {};
}

if (!Element.prototype.releasePointerCapture) {
  Element.prototype.releasePointerCapture = () => {};
}

if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {};
}

// With isolate:false, vitest reuses one worker — a single module graph and a
// single jsdom window — across every test file the worker runs. Vitest still
// scopes the vi.mock registry per file, but it does NOT re-evaluate modules an
// earlier file already imported, so a module evaluated under another file's
// mocks (e.g. @/lib/notify bound to a mocked sonner) or one holding
// module-level state would leak into later files. Likewise, globals a test
// overrides on the shared window (matchMedia, location, ...) would leak.
// Snapshot the commonly-overridden globals once per worker, and restore them
// plus the module registry after each file.
const pristineGlobals = (() => {
  const key = Symbol.for('143-tests/pristine-globals');
  const holder = globalThis as {
    [key: symbol]: Array<[object, string, PropertyDescriptor | undefined]>;
  };
  holder[key] ??= (
    [
      [window, 'matchMedia'],
      [window, 'location'],
      [window, 'ResizeObserver'],
      [navigator, 'clipboard'],
      [navigator, 'vibrate'],
    ] as Array<[object, string]>
  ).map(([owner, prop]) => [owner, prop, Object.getOwnPropertyDescriptor(owner, prop)]);
  return holder[key];
})();

afterAll(() => {
  for (const [owner, prop, descriptor] of pristineGlobals) {
    if (descriptor) {
      Object.defineProperty(owner, prop, descriptor);
    } else {
      delete (owner as Record<string, unknown>)[prop];
    }
  }
  document.title = '';
  vi.unstubAllGlobals();
  vi.useRealTimers();
  vi.resetModules();
});

// The MSW server is a per-worker singleton (see mocks/server.ts). Patch the
// network interceptors once per worker and leave them active for its
// lifetime — listen/close cycling per file would re-patch fetch on a window
// that other files share.
const mswListeningKey = Symbol.for('143-tests/msw-listening');
beforeAll(() => {
  const holder = globalThis as { [mswListeningKey]?: boolean };
  if (holder[mswListeningKey]) return;
  holder[mswListeningKey] = true;
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => {
  cleanup();
  server.resetHandlers();
  // Reset browser storage so persisted state (e.g. /sessions/new draft) does
  // not leak between tests.
  window.sessionStorage.clear();
  window.localStorage.clear();
});
