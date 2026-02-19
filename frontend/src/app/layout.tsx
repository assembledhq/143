"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { NuqsAdapter } from "nuqs/adapters/next/app";
import "./globals.css";
import { useState } from "react";
import { ErrorBoundary } from "@/components/error-boundary";

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  const [queryClient] = useState(() => new QueryClient());

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
