import { renderWithProviders, screen } from "@/test/test-utils";
import { useQueryClient } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Providers, shouldRetryQuery } from "./providers";

function QueryDefaultsProbe() {
  const queryClient = useQueryClient();
  const queries = queryClient.getDefaultOptions().queries;
  return (
    <output aria-label="query defaults">
      {JSON.stringify({
        staleTime: queries?.staleTime,
        gcTime: queries?.gcTime,
        refetchOnWindowFocus: queries?.refetchOnWindowFocus,
      })}
    </output>
  );
}

describe("Providers", () => {
  beforeEach(() => {
    Object.defineProperty(window, "matchMedia", {
      configurable: true,
      writable: true,
      value: vi.fn().mockImplementation((query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addListener: vi.fn(),
        removeListener: vi.fn(),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    });
  });

  it("sets app-wide query freshness defaults", () => {
    renderWithProviders(
      <Providers>
        <QueryDefaultsProbe />
      </Providers>,
    );

    expect(screen.getByLabelText("query defaults")).toHaveTextContent(
      JSON.stringify({
        staleTime: 30_000,
        gcTime: 10 * 60_000,
        refetchOnWindowFocus: false,
      }),
    );
  });

  it("does not retry deterministic client errors", () => {
    expect(shouldRetryQuery(0, { status: 403 })).toBe(false);
    expect(shouldRetryQuery(1, { status: 404 })).toBe(false);
  });

  it("retries transient query errors only within the app retry budget", () => {
    expect(shouldRetryQuery(0, { status: 500 })).toBe(true);
    expect(shouldRetryQuery(1, new Error("network reset"))).toBe(true);
    expect(shouldRetryQuery(2, new Error("network reset"))).toBe(false);
  });
});
