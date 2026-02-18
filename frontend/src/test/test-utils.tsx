import React from 'react';
import { render, type RenderOptions } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { NuqsTestingAdapter } from 'nuqs/adapters/testing';

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
        {children}
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

  if (searchParams) {
    const wrapper = ({ children }: { children: React.ReactNode }) => {
      const queryClient = createTestQueryClient();
      return (
        <NuqsTestingAdapter searchParams={searchParams}>
          <QueryClientProvider client={queryClient}>
            {children}
          </QueryClientProvider>
        </NuqsTestingAdapter>
      );
    };
    return render(ui, { wrapper, ...renderOptions });
  }

  return render(ui, { wrapper: TestProviders, ...renderOptions });
}

export { renderWithProviders, createTestQueryClient };
export { screen, waitFor, within } from '@testing-library/react';
export { default as userEvent } from '@testing-library/user-event';
