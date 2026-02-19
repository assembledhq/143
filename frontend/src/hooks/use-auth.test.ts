import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { createTestQueryClient } from '@/test/test-utils';
import { QueryClientProvider } from '@tanstack/react-query';
import React from 'react';

const meMock = vi.hoisted(() => vi.fn());
const providersMock = vi.hoisted(() => vi.fn());
const logoutMock = vi.hoisted(() => vi.fn());

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

  it('returns unauthenticated state when API returns error', async () => {
    meMock.mockRejectedValue(new Error('Unauthorized'));

    const { result } = renderHook(() => useAuth(), { wrapper: createWrapper() });

    await waitFor(() => {
      expect(result.current.isLoading).toBe(false);
    });

    expect(result.current.isAuthenticated).toBe(false);
    expect(result.current.user).toBeNull();
  });

  it('starts in loading state', () => {
    meMock.mockReturnValue(new Promise(() => {})); // never resolves

    const { result } = renderHook(() => useAuth(), { wrapper: createWrapper() });

    expect(result.current.isLoading).toBe(true);
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
