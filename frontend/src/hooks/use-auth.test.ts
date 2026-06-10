import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { createTestQueryClient } from '@/test/test-utils';
import { QueryClientProvider } from '@tanstack/react-query';
import React from 'react';

const meMock = vi.hoisted(() => vi.fn());
const providersMock = vi.hoisted(() => vi.fn());
const logoutMock = vi.hoisted(() => vi.fn());

// Build an error object matching the ApiError duck-type (has a `code` field).
function apiError(code: string, message: string): Error & { code: string } {
  const err = new Error(message) as Error & { code: string };
  err.code = code;
  return err;
}

vi.mock('@/lib/api', () => ({
  api: {
    auth: {
      me: meMock,
      providers: providersMock,
      logout: logoutMock,
    },
  },
}));

import { useAuth, useAuthProviders } from './use-auth';
import { readCachedViewerScope } from '@/lib/viewer-scope-cache';

function createWrapper() {
  const queryClient = createTestQueryClient();
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return React.createElement(QueryClientProvider, { client: queryClient }, children);
  };
}

describe('useAuth', () => {
  beforeEach(() => {
    meMock.mockReset();
    logoutMock.mockReset();
  });

  it('returns authenticated state when user is found', async () => {
    meMock.mockResolvedValue({
      data: { id: '1', email: 'test@test.com', name: 'Test' },
    });

    const { result } = renderHook(() => useAuth(), { wrapper: createWrapper() });

    await waitFor(() => {
      expect(result.current.isLoading).toBe(false);
    });

    expect(result.current.isAuthenticated).toBe(true);
    expect(result.current.user?.email).toBe('test@test.com');
  });

  it('returns unauthenticated state when API returns 401', async () => {
    meMock.mockRejectedValue(apiError('UNAUTHORIZED', 'Unauthorized'));

    const { result } = renderHook(() => useAuth(), { wrapper: createWrapper() });

    await waitFor(() => {
      expect(result.current.isLoading).toBe(false);
    });

    expect(result.current.isAuthenticated).toBe(false);
    expect(result.current.isUnauthorized).toBe(true);
    expect(result.current.user).toBeNull();
  });

  it('does not mark unauthorized on transient non-401 errors', async () => {
    meMock.mockRejectedValue(apiError('INTERNAL_ERROR', 'boom'));

    const { result } = renderHook(() => useAuth(), { wrapper: createWrapper() });

    await waitFor(() => {
      expect(result.current.isLoading).toBe(false);
    });

    expect(result.current.isAuthenticated).toBe(false);
    expect(result.current.isUnauthorized).toBe(false);
    expect(result.current.isTransientError).toBe(true);
    expect(result.current.user).toBeNull();
  });

  it('is neither unauthorized nor transient-error on success', async () => {
    meMock.mockResolvedValue({
      data: { id: '1', email: 'test@test.com', name: 'Test' },
    });

    const { result } = renderHook(() => useAuth(), { wrapper: createWrapper() });

    await waitFor(() => {
      expect(result.current.isLoading).toBe(false);
    });

    expect(result.current.isUnauthorized).toBe(false);
    expect(result.current.isTransientError).toBe(false);
  });

  it('starts in loading state', () => {
    meMock.mockReturnValue(new Promise(() => {})); // never resolves

    const { result } = renderHook(() => useAuth(), { wrapper: createWrapper() });

    expect(result.current.isLoading).toBe(true);
  });

  it('logout calls api, clears query cache and redirects to the landing page', async () => {
    meMock.mockResolvedValue({
      data: { id: '1', email: 'test@test.com', name: 'Test' },
    });
    logoutMock.mockResolvedValue({});

    const { result } = renderHook(() => useAuth(), { wrapper: createWrapper() });

    await waitFor(() => {
      expect(result.current.isLoading).toBe(false);
    });

    // Save original location
    const originalLocation = window.location;
    Object.defineProperty(window, 'location', {
      value: { href: '' },
      writable: true,
      configurable: true,
    });

    await result.current.logout();

    expect(logoutMock).toHaveBeenCalledTimes(1);
    expect(window.location.href).toBe('/');

    // Restore original location
    Object.defineProperty(window, 'location', {
      value: originalLocation,
      writable: true,
      configurable: true,
    });
  });

  it('returns backend-backed user settings from auth/me', async () => {
    meMock.mockResolvedValue({
      data: {
        id: 'user-1',
        email: 'test@test.com',
        name: 'Test',
        settings: {
          coding_agent_reasoning_defaults: {
            codex: 'xhigh',
          },
        },
      },
    });

    const { result } = renderHook(() => useAuth(), { wrapper: createWrapper() });

    await waitFor(() => {
      expect(result.current.isLoading).toBe(false);
    });

    expect(result.current.user?.settings?.coding_agent_reasoning_defaults?.codex).toBe('xhigh');
  });

  it('caches the viewer scope in localStorage when auth/me resolves', async () => {
    window.localStorage.clear();
    meMock.mockResolvedValue({
      data: { id: 'user-7', org_id: 'org-9', email: 'test@test.com', name: 'Test' },
    });

    const { result } = renderHook(() => useAuth(), { wrapper: createWrapper() });

    await waitFor(() => {
      expect(result.current.isAuthenticated).toBe(true);
    });

    expect(readCachedViewerScope(window.localStorage)).toEqual({
      userId: 'user-7',
      orgId: 'org-9',
    });
  });

  it('prefers the per-tab active org over the home org when caching the scope', async () => {
    window.localStorage.clear();
    window.sessionStorage.setItem('active_org_id', 'org-tab');
    meMock.mockResolvedValue({
      data: { id: 'user-7', org_id: 'org-9', email: 'test@test.com', name: 'Test' },
    });

    const { result } = renderHook(() => useAuth(), { wrapper: createWrapper() });

    await waitFor(() => {
      expect(result.current.isAuthenticated).toBe(true);
    });

    expect(readCachedViewerScope(window.localStorage)).toEqual({
      userId: 'user-7',
      orgId: 'org-tab',
    });
    window.sessionStorage.removeItem('active_org_id');
  });
});

describe('useAuthProviders', () => {
  beforeEach(() => {
    providersMock.mockReset();
  });

  it('returns provider flags', async () => {
    providersMock.mockResolvedValue({
      data: { github: true, google: false, email: true },
    });

    const { result } = renderHook(() => useAuthProviders(), { wrapper: createWrapper() });

    await waitFor(() => {
      expect(result.current.isLoading).toBe(false);
    });

    expect(result.current.providers?.github).toBe(true);
    expect(result.current.providers?.google).toBe(false);
    expect(result.current.providers?.email).toBe(true);
  });
});
