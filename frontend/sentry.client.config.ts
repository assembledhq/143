import * as Sentry from "@sentry/nextjs";

Sentry.init({
  dsn: process.env.NEXT_PUBLIC_SENTRY_DSN,
  enabled: !!process.env.NEXT_PUBLIC_SENTRY_DSN,
  environment: process.env.NEXT_PUBLIC_SENTRY_ENVIRONMENT ?? "development",

  // Capture 100% of errors. Adjust down if volume grows.
  sampleRate: 1.0,

  // Capture 10% of transactions for performance monitoring.
  tracesSampleRate: 0.1,

  // Capture session replay on errors only (no baseline recording).
  replaysOnErrorSampleRate: 1.0,
  replaysSessionSampleRate: 0,

  integrations: [Sentry.replayIntegration()],
});
