import '@testing-library/jest-dom/vitest';
import { act, cleanup } from '@testing-library/react';
import { afterAll, afterEach, beforeAll } from 'vitest';
import { server } from './mocks/server';

const originalConsoleError = console.error.bind(console);
const originalConsoleWarn = console.warn.bind(console);
const originalLocation = window.location;

type MockLocation = {
  href: string;
  pathname: string;
  search: string;
  hash: string;
  origin: string;
  assign: (url: string | URL) => void;
  replace: (url: string | URL) => void;
  reload: () => void;
  toString: () => string;
};

function createMockLocation(): MockLocation {
  const location: MockLocation = {
    href: originalLocation.href,
    pathname: originalLocation.pathname,
    search: originalLocation.search,
    hash: originalLocation.hash,
    origin: originalLocation.origin,
    assign: (url) => {
      location.href = String(url);
    },
    replace: (url) => {
      location.href = String(url);
    },
    reload: () => {},
    toString: () => location.href,
  };

  return location;
}

const mockLocation = createMockLocation();

function handleDocumentNavigation(event: MouseEvent) {
  const target = event.target;
  if (!(target instanceof Element)) return;

  const anchor = target.closest('a[href]');
  if (!(anchor instanceof HTMLAnchorElement) || event.defaultPrevented) return;

  event.preventDefault();
  mockLocation.href = anchor.href;
}

const SUPPRESSED_TEST_NOISE_PATTERNS = [
  /^An update to .* inside a test was not wrapped in act/,
  /^A component suspended inside an `act` scope, but the `act` call was not awaited/,
  /^The current testing environment is not configured to support act/,
  /^Not implemented: navigation to another Document/,
  /^applyOrgSettingsPatch: cache entry is empty; optimistic write skipped\./,
];

function formatConsoleArg(arg: unknown): string {
  if (typeof arg === 'string') return arg;
  if (arg instanceof Error) return arg.message;
  return String(arg);
}

function shouldSuppressTestNoise(args: unknown[]): boolean {
  const message = args.map(formatConsoleArg).join(' ');
  return SUPPRESSED_TEST_NOISE_PATTERNS.some((pattern) => pattern.test(message));
}

console.error = (...args: Parameters<typeof console.error>) => {
  if (shouldSuppressTestNoise(args)) {
    return;
  }
  originalConsoleError(...args);
};

console.warn = (...args: Parameters<typeof console.warn>) => {
  if (shouldSuppressTestNoise(args)) {
    return;
  }
  originalConsoleWarn(...args);
};

Object.defineProperty(window, 'location', {
  value: mockLocation,
  writable: true,
  configurable: true,
});

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
beforeAll(() => {
  document.addEventListener('click', handleDocumentNavigation);
});
async function flushPendingReactWork() {
  await act(async () => {
    await Promise.resolve();
    await new Promise<void>((resolve) => setTimeout(resolve, 0));
    await new Promise<void>((resolve) => window.requestAnimationFrame(() => resolve()));
  });
}

afterEach(async () => {
  // Give pending query completions, Radix presence teardowns, and composer
  // requestAnimationFrame callbacks a final act-wrapped tick before cleanup.
  await flushPendingReactWork();
  cleanup();
  server.resetHandlers();
  // Reset browser storage so persisted state (e.g. /sessions/new draft) does
  // not leak between tests.
  window.sessionStorage.clear();
  window.localStorage.clear();
  Object.assign(mockLocation, createMockLocation());
});
afterAll(() => {
  console.error = originalConsoleError;
  console.warn = originalConsoleWarn;
  document.removeEventListener('click', handleDocumentNavigation);
  Object.defineProperty(window, 'location', {
    value: originalLocation,
    writable: true,
    configurable: true,
  });
  server.close();
});
