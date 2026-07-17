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
    <NuqsTestingAdapter>
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
  queryClient?: QueryClient;
  nuqsHasMemory?: boolean;
}

function renderWithProviders(
  ui: React.ReactElement,
  options?: RenderWithProvidersOptions,
) {
  const {
    searchParams,
    queryClient: providedQueryClient,
    nuqsHasMemory,
    ...renderOptions
  } = options ?? {};

  if (searchParams || providedQueryClient || nuqsHasMemory) {
    const wrapper = ({ children }: { children: React.ReactNode }) => {
      const queryClient = providedQueryClient ?? createTestQueryClient();
      return (
        <NuqsTestingAdapter searchParams={searchParams} hasMemory={nuqsHasMemory}>
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
