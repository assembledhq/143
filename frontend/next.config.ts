import type { NextConfig } from "next";
import { withSentryConfig } from "@sentry/nextjs";

const apiTarget = process.env.API_PROXY_TARGET || "http://localhost:8080";

const nextConfig: NextConfig = {
  output: process.env.NODE_ENV === "production" ? "standalone" : undefined,
  allowedDevOrigins: ["*.ngrok.dev", "localhost", "127.0.0.1"],
  env: {
    NEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE:
      process.env.NEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE ||
      process.env.PREVIEW_ORIGIN_TEMPLATE ||
      "http://{id}.preview.localhost:9090",
  },
  async rewrites() {
    return [
      {
        source: '/api/:path*',
        destination: `${apiTarget}/api/:path*`,
      },
    ];
  },
};

export default withSentryConfig(nextConfig, {
  // Suppress Sentry CLI logs during build
  silent: true,

  // Upload source maps for readable stack traces.
  // Requires SENTRY_AUTH_TOKEN, SENTRY_ORG, and SENTRY_PROJECT env vars at build time.
  sourcemaps: {
    disable: !process.env.SENTRY_AUTH_TOKEN,
  },
});
