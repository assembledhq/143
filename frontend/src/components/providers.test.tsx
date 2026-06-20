import { renderWithProviders, screen, waitFor } from "@/test/test-utils";
import { act, render } from "@testing-library/react";
import { QueryClient, useQueryClient } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ACTIVE_ORG_CHANGED_EVENT } from "@/lib/active-org";
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

// Records the QueryClient instance the provider hands down on every render so a
// test can detect when the provider swaps it.
function ClientProbe({ onClient }: { onClient: (client: QueryClient) => void }) {
  onClient(useQueryClient());
  return null;
}

describe("Providers", () => {
  beforeEach(() => {
    window.sessionStorage.clear();
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

  it("swaps in a fresh query client when the active org switches, clearing the old one", async () => {
    window.sessionStorage.setItem("active_org_id", "org-a");
    const clients: QueryClient[] = [];
    render(
      <Providers>
        <ClientProbe onClient={(c) => clients.push(c)} />
      </Providers>,
    );
    const first = clients[clients.length - 1];
    first.setQueryData(["sessions"], ["cached-for-org-a"]);

    act(() => {
      window.sessionStorage.setItem("active_org_id", "org-b");
      window.dispatchEvent(new CustomEvent(ACTIVE_ORG_CHANGED_EVENT));
    });

    await waitFor(() => {
      expect(clients[clients.length - 1]).not.toBe(first);
    });
    // The discarded client is cleared immediately rather than left pinned alive
    // by its gcTime timers, so org-A data can't linger after the switch.
    expect(first.getQueryData(["sessions"])).toBeUndefined();
  });

  it("keeps the warm cache on first-load adoption (null → org) instead of refetching", () => {
    // Fresh tab: nothing pinned at mount, so the provider's baseline is null.
    const clients: QueryClient[] = [];
    render(
      <Providers>
        <ClientProbe onClient={(c) => clients.push(c)} />
      </Providers>,
    );
    const first = clients[clients.length - 1];
    first.setQueryData(["sessions"], ["warm"]);

    act(() => {
      window.sessionStorage.setItem("active_org_id", "org-a");
      window.dispatchEvent(new CustomEvent(ACTIVE_ORG_CHANGED_EVENT));
    });

    // No swap: the adopted org is the one header-less queries already resolved
    // against via last_org_id, so the cache is correct — recreating it would
    // only discard good data and force a needless refetch.
    expect(clients[clients.length - 1]).toBe(first);
    expect(first.getQueryData(["sessions"])).toEqual(["warm"]);
  });
});
