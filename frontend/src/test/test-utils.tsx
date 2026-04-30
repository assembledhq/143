import React from 'react';
import { render, type RenderOptions } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { NuqsTestingAdapter } from 'nuqs/adapters/testing';
import { OptimisticSessionsProvider } from '@/contexts/optimistic-sessions';

function createTestQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
        gcTime: 0,
      },
    },
  });
}

function TestProviders({ children }: { children: React.ReactNode }) {
  const queryClient = createTestQueryClient();
  return (
    <NuqsTestingAdapter
      hasMemory
      onUrlUpdate={({ queryString }) => {
        const normalizedQuery = queryString.replace(/^\?/, '');
        const nextUrl = normalizedQuery ? `${window.location.pathname}?${normalizedQuery}` : window.location.pathname;
        window.history.replaceState({}, '', nextUrl);
      }}
    >
      <QueryClientProvider client={queryClient}>
        <OptimisticSessionsProvider>
          {children}
        </OptimisticSessionsProvider>
      </QueryClientProvider>
    </NuqsTestingAdapter>
  );
}

interface RenderWithProvidersOptions extends Omit<RenderOptions, 'wrapper'> {
  searchParams?: Record<string, string>;
}

function renderWithProviders(
  ui: React.ReactElement,
  options?: RenderWithProvidersOptions,
) {
  const { searchParams, ...renderOptions } = options ?? {};

  if (typeof window !== 'undefined') {
    const initialQuery = searchParams ? new URLSearchParams(searchParams).toString() : '';
    const initialUrl = initialQuery ? `/?${initialQuery}` : '/';
    window.history.replaceState({}, '', initialUrl);
  }

  if (searchParams) {
    const wrapper = ({ children }: { children: React.ReactNode }) => {
      const queryClient = createTestQueryClient();
      return (
        <NuqsTestingAdapter
          searchParams={searchParams}
          hasMemory
          onUrlUpdate={({ queryString }) => {
            const normalizedQuery = queryString.replace(/^\?/, '');
            const nextUrl = normalizedQuery ? `${window.location.pathname}?${normalizedQuery}` : window.location.pathname;
            window.history.replaceState({}, '', nextUrl);
          }}
        >
          <QueryClientProvider client={queryClient}>
            <OptimisticSessionsProvider>
              {children}
            </OptimisticSessionsProvider>
          </QueryClientProvider>
        </NuqsTestingAdapter>
      );
    };
    return render(ui, { wrapper, ...renderOptions });
  }

  return render(ui, { wrapper: TestProviders, ...renderOptions });
}

export { renderWithProviders, createTestQueryClient };
export { fireEvent, screen, waitFor, within } from '@testing-library/react';
export { default as userEvent } from '@testing-library/user-event';
