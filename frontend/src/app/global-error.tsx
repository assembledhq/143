"use client";

import * as Sentry from "@sentry/nextjs";
import { useEffect } from "react";

export default function GlobalError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  useEffect(() => {
    Sentry.captureException(error);
  }, [error]);

  return (
    <html>
      <body>
        <div style={{ display: "flex", minHeight: "100vh", alignItems: "center", justifyContent: "center" }}>
          <div style={{ textAlign: "center" }}>
            <h2 style={{ fontSize: "1.25rem", fontWeight: 600 }}>Something went wrong</h2>
            <p style={{ color: "#666", marginTop: "0.5rem" }}>An unexpected error occurred. Please refresh the page.</p>
            <button
              onClick={() => reset()}
              style={{
                marginTop: "1rem",
                padding: "0.5rem 1rem",
                borderRadius: "0.375rem",
                backgroundColor: "#111",
                color: "#fff",
                fontSize: "0.875rem",
                fontWeight: 500,
                border: "none",
                cursor: "pointer",
              }}
            >
              Try again
            </button>
          </div>
        </div>
      </body>
    </html>
  );
}
