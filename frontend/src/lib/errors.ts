import * as Sentry from "@sentry/nextjs";

/**
 * Report a caught error to Sentry with optional context.
 *
 * Use this in catch blocks and error callbacks where you handle the error
 * gracefully but still want visibility in Sentry.
 *
 * @example
 *   try {
 *     await riskyOperation();
 *   } catch (err) {
 *     captureError(err, { feature: "session-polling" });
 *     // show fallback UI
 *   }
 */
export function captureError(
  error: unknown,
  tags?: Record<string, string>,
): void {
  Sentry.captureException(error, { tags });
}

/**
 * Report a non-error message to Sentry (warnings, unexpected states).
 *
 * @example
 *   if (!expectedData) {
 *     captureMessage("Missing expected data in response", { endpoint: "/api/sessions" });
 *   }
 */
export function captureMessage(
  message: string,
  tags?: Record<string, string>,
): void {
  Sentry.captureMessage(message, { tags });
}
