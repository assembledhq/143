import { renderWithProviders, screen } from "@/test/test-utils";
import { useQueryClient } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Providers } from "./providers";

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
});
