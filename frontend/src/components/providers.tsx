"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { NuqsAdapter } from "nuqs/adapters/next/app";
import { useState, useEffect, useRef } from "react";
import { ErrorBoundary } from "@/components/error-boundary";
import { ThemeProvider } from "@/components/theme-provider";
import { DocumentTitle } from "@/components/document-title";
import { ACTIVE_ORG_CHANGED_EVENT, getActiveOrgId } from "@/lib/active-org";

export const DEFAULT_QUERY_STALE_TIME_MS = 30_000;
export const DEFAULT_QUERY_GC_TIME_MS = 10 * 60_000;
const DEFAULT_QUERY_RETRY_LIMIT = 2;

function errorStatus(error: unknown): number | null {
  if (typeof error !== "object" || error === null) return null;
  const status = (error as { status?: unknown }).status;
  return typeof status === "number" ? status : null;
}

export function shouldRetryQuery(failureCount: number, error: unknown): boolean {
  const status = errorStatus(error);
  if (status !== null && status >= 400 && status < 500) {
    return false;
  }
  return failureCount < DEFAULT_QUERY_RETRY_LIMIT;
}

function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: shouldRetryQuery,
        staleTime: DEFAULT_QUERY_STALE_TIME_MS,
        gcTime: DEFAULT_QUERY_GC_TIME_MS,
        refetchOnWindowFocus: false,
      },
      mutations: {
        retry: 0,
      },
    },
  });
}

export function Providers({ children }: { children: React.ReactNode }) {
  // The QueryClient is scoped to the active org: when the tab switches between
  // orgs we swap in a brand-new client, structurally guaranteeing that two
  // orgs' data can never coexist in one cache. We deliberately do NOT encode
  // the org id into individual query keys — that would mean every current and
  // future org-scoped query (and every invalidation prefix) has to remember to
  // include it, and missing one silently reintroduces cross-org bleed. The org
  // boundary lives here, in one place, instead. See frontend/AGENTS.md →
  // "Active organization (multi-tenancy)".
  const [queryClient, setQueryClient] = useState(createQueryClient);

  // Swap in a fresh client when the active org changes between two concrete
  // ids (a real switch), or drops to none (revocation). This supersedes the
  // explicit queryClient.clear() that org switching also does — that clear is
  // now belt-and-suspenders.
  //
  // We deliberately do NOT swap on the first-load `null → org` adoption that
  // OrgSwitcher performs. That isn't a switch: the org being adopted is the
  // one the server already resolved this tab to, so any queries that raced
  // ahead and fetched header-less (falling back to the same last_org_id) hold
  // correctly-scoped data. Recreating the client there would throw away a warm
  // cache and force a full refetch on every cold load for no correctness gain.
  const activeOrgRef = useRef<string | null>(null);
  useEffect(() => {
    activeOrgRef.current = getActiveOrgId();
    const handler = () => {
      const next = getActiveOrgId();
      const prev = activeOrgRef.current;
      if (next === prev) return;
      activeOrgRef.current = next;
      // First-load adoption (null → org): keep the warm cache (see above).
      if (prev === null) return;
      // Real org → org (or org → null) change: replace the client so org-A
      // data can't survive into org B, and clear the discarded one now rather
      // than leaving its cache pinned alive by gcTime timers.
      setQueryClient((old) => {
        old.clear();
        return createQueryClient();
      });
    };
    window.addEventListener(ACTIVE_ORG_CHANGED_EVENT, handler);
    return () => window.removeEventListener(ACTIVE_ORG_CHANGED_EVENT, handler);
  }, []);

  useEffect(() => {
    // ── P-80 Shooting Star easter egg ──────────────────────────────────────────
    // Top-down silhouette: nose (top), straight wings, torpedo wingtip tanks,
    // bubble canopy, and H-tail — just like the canvas on the landing page.
    const art = [
      '                         *',
      '                        /|\\',
      '                       / | \\',
      '        ______________/  |  \\______________',
      '       /               ( ^ )               \\',
      '      /                                     \\',
      '=====/                                       \\=====',
      '      \\                                     /',
      '       \\______________       ______________/',
      '                      \\     /',
      '                       \\   /',
      '                        | |',
      '                       _| |_',
      '                      / | | \\',
      '                     /  | |  \\',
    ].join('\n');

    console.log(
      '%c' + art,
      'color:#d2dae6;font-family:monospace;font-size:11px;line-height:1.5'
    );
    console.log(
      '%c  143 days  ',
      'color:#ffd700;background:#08080f;padding:3px 8px;font-family:monospace;letter-spacing:2px'
    );
    console.log(
      '%cBe quick, be quiet, be on time.',
      'color:#555555;font-family:monospace;font-style:italic;font-size:11px;letter-spacing:0.5px'
    );
  }, []);

  return (
    <ThemeProvider attribute="class" defaultTheme="system" enableSystem disableTransitionOnChange>
      <NuqsAdapter>
        <QueryClientProvider client={queryClient}>
          <ErrorBoundary>
            <DocumentTitle />
            {children}
          </ErrorBoundary>
        </QueryClientProvider>
      </NuqsAdapter>
    </ThemeProvider>
  );
}
