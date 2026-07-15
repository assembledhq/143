import { withSentryConfig } from "@sentry/nextjs";
import { createMDX } from "fumadocs-mdx/next";
import path from "node:path";
import { fileURLToPath } from "node:url";

const apiTarget = process.env.API_PROXY_TARGET || "http://localhost:8080";
const frontendRoot = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(frontendRoot, "..");

// Dogfood previews (143.dev) build inside a memory-capped sandbox whose cgroup
// is shared by the frontend build, the Go server, and the DB seed. The platform
// sets ONEFORTYTHREE_ENV=preview (reserved; repos can't override it). In that
// mode we trim the build's peak RSS: type checking and ESLint each spawn their
// own long-lived process/heap during `next build`, and the Sentry webpack
// plugin instruments the whole graph. The branch is already typechecked and
// linted in CI, and preview builds upload no source maps, so none of that work
// needs to run again on the launch hot path — skipping it keeps the build under
// the sandbox cap.
const isPreview = process.env.ONEFORTYTHREE_ENV === "preview";

// Baseline security headers applied to every app response. These are
// defense-in-depth: the app sanitizes rendered content, but a CSP frame-ancestors
// directive plus X-Frame-Options prevents clickjacking, nosniff blocks MIME
// sniffing, and Referrer-Policy limits referrer leakage. The CSP intentionally
// only sets frame-ancestors so it does not constrain scripts/styles/connections
// (the app uses inline styles from Shiki/motion). HSTS is production-only so it
// does not pin http on localhost during development.
const securityHeaders = [
  { key: "X-Frame-Options", value: "SAMEORIGIN" },
  { key: "Content-Security-Policy", value: "frame-ancestors 'self'" },
  { key: "X-Content-Type-Options", value: "nosniff" },
  { key: "Referrer-Policy", value: "strict-origin-when-cross-origin" },
  ...(process.env.NODE_ENV === "production"
    ? [
        {
          key: "Strict-Transport-Security",
          value: "max-age=63072000; includeSubDomains",
        },
      ]
    : []),
];

/**
 * @type {import("next").NextConfig}
 */
const nextConfig = {
  output: process.env.NODE_ENV === "production" ? "standalone" : undefined,
  allowedDevOrigins: ["*.ngrok.dev", "localhost", "127.0.0.1"],
  // Preview-only: skip the type-check and lint passes during `next build` to
  // cut build-time memory (they already ran in CI). No effect on real builds.
  ...(isPreview
    ? {
        typescript: { ignoreBuildErrors: true },
        eslint: { ignoreDuringBuilds: true },
      }
    : {}),
  turbopack: {
    root: repoRoot,
  },
  env: {
    NEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE:
      process.env.NEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE ||
      process.env.PREVIEW_ORIGIN_TEMPLATE ||
      "http://{id}.preview.localhost:9090",
  },
  async rewrites() {
    return [
      {
        source: "/docs/:path*.md",
        destination: "/api/docs/raw/:path*",
      },
      {
        source: "/api/:path*",
        destination: `${apiTarget}/api/:path*`,
      },
    ];
  },
  async headers() {
    return [
      {
        source: "/:path*",
        headers: securityHeaders,
      },
    ];
  },
};

const withMDX = createMDX();

// In dogfood previews, skip the Sentry webpack plugin entirely: it instruments
// the whole module graph at build time (memory + wall-clock) and uploads no
// source maps here anyway (no SENTRY_AUTH_TOKEN), so it is pure build overhead
// in the memory-capped sandbox. Real builds keep full Sentry instrumentation.
export default isPreview
  ? withMDX(nextConfig)
  : withSentryConfig(withMDX(nextConfig), {
      // Suppress Sentry CLI logs during build.
      silent: true,

      // Upload source maps for readable stack traces.
      // Requires SENTRY_AUTH_TOKEN, SENTRY_ORG, and SENTRY_PROJECT env vars at build time.
      sourcemaps: {
        disable: !process.env.SENTRY_AUTH_TOKEN,
      },
    });
