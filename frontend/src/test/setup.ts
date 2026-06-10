import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterAll, afterEach, beforeAll, vi } from 'vitest';
import { server } from './mocks/server';

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

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => {
  cleanup();
  server.resetHandlers();
  // Reset browser storage so persisted state (e.g. /sessions/new draft) does
  // not leak between tests.
  window.sessionStorage.clear();
  window.localStorage.clear();
});
afterAll(() => server.close());
