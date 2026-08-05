import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterAll, afterEach, beforeAll, beforeEach, vi } from 'vitest';
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

// Snapshot commonly overridden globals and restore them after each file. This
// keeps focused runs and future pool/config changes safe even though the main
// suite currently gives every file an isolated environment.
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

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' });
});
beforeEach(() => {
  // A test that aborts or times out before its local cleanup can leave fake
  // timers installed on the reused jsdom worker. Real-time polling and
  // debounce tests must never inherit that state from an unrelated file.
  vi.useRealTimers();
});
afterEach(() => {
  cleanup();
  server.resetHandlers();
  // Reset browser storage so persisted state (e.g. /sessions/new draft) does
  // not leak between tests.
  window.sessionStorage.clear();
  window.localStorage.clear();
});

afterAll(() => {
  server.close();
});
