export const PREVIEW_BOOTSTRAP_READY_EVENT = "preview_bootstrap_ready";
export const PREVIEW_BOOTSTRAP_TOKEN_EVENT = "preview_bootstrap_token";
export const PREVIEW_BOOTSTRAP_COMPLETE_EVENT = "preview_bootstrap_complete";

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
