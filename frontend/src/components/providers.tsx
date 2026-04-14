"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { NuqsAdapter } from "nuqs/adapters/next/app";
import { useState, useEffect } from "react";
import { ErrorBoundary } from "@/components/error-boundary";
import { ThemeProvider } from "@/components/theme-provider";

export function Providers({ children }: { children: React.ReactNode }) {
  const [queryClient] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            retry: 2,
          },
          mutations: {
            retry: 0,
          },
        },
      })
  );

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
            {children}
          </ErrorBoundary>
        </QueryClientProvider>
      </NuqsAdapter>
    </ThemeProvider>
  );
}
