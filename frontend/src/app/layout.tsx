"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { NuqsAdapter } from "nuqs/adapters/next/app";
import "./globals.css";
import { useState, useEffect } from "react";
import { ErrorBoundary } from "@/components/error-boundary";

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  const [queryClient] = useState(() => new QueryClient());

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
      '%c  XP-80 Shooting Star  %c  Built in 143 days  ',
      'color:#d2dae6;background:#08080f;padding:3px 8px;font-family:monospace;font-weight:bold;letter-spacing:1px',
      'color:#ffd700;background:#08080f;padding:3px 8px;font-family:monospace;letter-spacing:1px'
    );
    console.log(
      '%c  "Be quick, be quiet, be on time."  ',
      'color:#ffd700;font-family:monospace;font-style:italic;font-size:13px;padding:4px 0;letter-spacing:0.5px'
    );
    console.log(
      '%c  — Kelly Johnson, Skunk Works',
      'color:#555555;font-family:monospace;font-size:11px;padding-bottom:4px'
    );
  }, []);

  return (
    <html lang="en">
      <body className="antialiased">
        <NuqsAdapter>
          <QueryClientProvider client={queryClient}>
            <ErrorBoundary>
              {children}
            </ErrorBoundary>
          </QueryClientProvider>
        </NuqsAdapter>
      </body>
    </html>
  );
}
