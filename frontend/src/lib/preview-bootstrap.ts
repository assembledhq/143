export const PREVIEW_BOOTSTRAP_READY_EVENT = "preview_bootstrap_ready";
export const PREVIEW_BOOTSTRAP_TOKEN_EVENT = "preview_bootstrap_token";
export const PREVIEW_BOOTSTRAP_COMPLETE_EVENT = "preview_bootstrap_complete";
export const PREVIEW_BOOTSTRAP_TIMEOUT_MS = 5_000;
export const PREVIEW_BOOTSTRAP_TIMEOUT_ERROR =
  "Preview connection timed out. The preview gateway did not answer in time.";
// Sent to window.opener (the preview-domain control overlay) when a popup
// launch finishes the bootstrap handshake. Mirrored in the gateway overlay
// script (internal/api/gateway/preview_gateway.go).
export const PREVIEW_LAUNCH_COMPLETE_EVENT = "preview_launch_complete";

export function previewOriginFromURL(previewURL: string): string | undefined {
  try {
    const parsed = new URL(previewURL);
    if (parsed.protocol !== "https:") return undefined;
    return parsed.origin;
  } catch {
    return undefined;
  }
}

export function buildPreviewBootstrapSrc(previewOrigin: string): string {
  return `${previewOrigin.replace(/\/+$/, "")}/bootstrap`;
}

export function previewBootstrapTimeoutDetails(timeoutMs = PREVIEW_BOOTSTRAP_TIMEOUT_MS): string {
  const seconds = Math.round(timeoutMs / 1000);
  return `The preview gateway did not answer the browser bootstrap handshake within ${seconds} seconds. The runtime may still be starting, or the preview edge may be temporarily unavailable.`;
}
