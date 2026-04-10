import * as Sentry from "@sentry/nextjs";

// Patch performance.measure to suppress "negative time stamp" errors thrown by
// Sentry's BrowserTracing integration when it instruments React component renders.
if (typeof window !== "undefined" && window.performance?.measure) {
  const originalMeasure = window.performance.measure.bind(window.performance);
  window.performance.measure = (...args: Parameters<Performance["measure"]>) => {
    try {
      return originalMeasure(...args);
    } catch (e) {
      if (
        e instanceof DOMException &&
        e.message.includes("negative time stamp")
      ) {
        return undefined as unknown as PerformanceMeasure;
      }
      throw e;
    }
  };
}

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
