"use client";

import { useCallback, useEffect, useMemo, useRef, useState, type ComponentProps, type ReactNode } from "react";
import { useMutation } from "@tanstack/react-query";
import { ExternalLink, Loader2 } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import {
  buildPreviewBootstrapSrc,
  PREVIEW_BOOTSTRAP_COMPLETE_EVENT,
  PREVIEW_BOOTSTRAP_READY_EVENT,
  PREVIEW_BOOTSTRAP_TOKEN_EVENT,
  PREVIEW_BOOTSTRAP_TIMEOUT_ERROR,
  PREVIEW_BOOTSTRAP_TIMEOUT_MS,
  previewBootstrapTimeoutDetails,
} from "@/lib/preview-bootstrap";

const POPUP_NAVIGATION_POLL_MS = 250;
const POPUP_NAVIGATION_TIMEOUT_MS = 30_000;

// Document shown in the popup while the preview connects. Once the bootstrap
// handshake completes we navigate the popup to the preview origin; because that
// is a cross-origin load the opener cannot observe its progress, so this
// placeholder stays visible until the preview's first bytes arrive (which can
// take a few seconds while a cold worker wakes up). A styled spinner + copy
// makes that wait read as intentional instead of a frozen "Opening preview..."
const OPENING_PREVIEW_DOCUMENT = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Opening preview</title><style>:root{color-scheme:light dark}html,body{height:100%;margin:0}body{font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;display:grid;place-items:center;background:Canvas;color:CanvasText}main{text-align:center;padding:24px}.spinner{width:28px;height:28px;margin:0 auto 16px;border-radius:50%;border:3px solid color-mix(in srgb,CanvasText 18%,transparent);border-top-color:CanvasText;animation:spin .8s linear infinite}h1{font-size:16px;font-weight:600;margin:0}p{font-size:13px;margin:8px 0 0;color:color-mix(in srgb,CanvasText 62%,transparent)}@keyframes spin{to{transform:rotate(360deg)}}</style></head><body><main><div class="spinner" role="status" aria-label="Connecting"></div><h1>Opening preview…</h1><p>Connecting to your preview — this can take a few moments while it wakes up.</p></main></body></html>`;

type BootstrapToken = {
  token: string;
  preview_id?: string;
};

type PendingOpen = {
  previewID: string;
  previewURL: string;
  origin: string;
  returnURL: string;
  popup: Window | null;
  currentTab: boolean;
  tokenRequested: boolean;
};

type OpenFailure = {
  title: string;
  description: string;
  message: string;
};

export type OpenPreviewButtonProps = {
  previewId: string | undefined | null;
  previewUrl: string | undefined | null;
  label?: string;
  disabled?: boolean;
  variant?: ComponentProps<typeof Button>["variant"];
  size?: ComponentProps<typeof Button>["size"];
  className?: string;
  bootstrapPreview?: (previewId: string) => Promise<BootstrapToken>;
};

type LaunchPreviewInput = {
  previewId: string;
  previewUrl: string;
  popup?: Window | null;
  target?: "new_tab" | "current_tab";
};

export function usePreviewLauncher(bootstrapPreview?: (previewId: string) => Promise<BootstrapToken>): {
  launchPreview: (input: LaunchPreviewInput) => Promise<void>;
  isOpening: boolean;
  error: Error | null;
  bootstrapFrame: ReactNode;
} {
  const iframeRef = useRef<HTMLIFrameElement | null>(null);
  const pendingRef = useRef<PendingOpen | null>(null);
  const timeoutRef = useRef<number | null>(null);
  const popupLoadCleanupRef = useRef<(() => void) | null>(null);
  const popupNavigationPollRef = useRef<number | null>(null);
  const popupNavigationTimeoutRef = useRef<number | null>(null);
  const [iframeSrc, setIframeSrc] = useState<string | undefined>();
  const [isOpening, setIsOpening] = useState(false);
  const [error, setError] = useState<Error | null>(null);

  const clearTimeoutRef = useCallback(() => {
    if (timeoutRef.current !== null) {
      window.clearTimeout(timeoutRef.current);
      timeoutRef.current = null;
    }
  }, []);

  const resetPendingOpen = useCallback(() => {
    clearTimeoutRef();
    popupLoadCleanupRef.current?.();
    popupLoadCleanupRef.current = null;
    if (popupNavigationPollRef.current !== null) {
      window.clearInterval(popupNavigationPollRef.current);
      popupNavigationPollRef.current = null;
    }
    if (popupNavigationTimeoutRef.current !== null) {
      window.clearTimeout(popupNavigationTimeoutRef.current);
      popupNavigationTimeoutRef.current = null;
    }
    pendingRef.current = null;
    setIframeSrc(undefined);
    setIsOpening(false);
  }, [clearTimeoutRef]);

  const completeOpenWhenPopupLoads = useCallback(
    (popup: Window) => {
      popupLoadCleanupRef.current?.();

      const onLoad = () => {
        resetPendingOpen();
      };

      popup.addEventListener("load", onLoad, { once: true });
      popupLoadCleanupRef.current = () => {
        popup.removeEventListener("load", onLoad);
      };

      popupNavigationPollRef.current = window.setInterval(() => {
        if (popup.closed) {
          resetPendingOpen();
          return;
        }

        try {
          void popup.document;
        } catch {
          resetPendingOpen();
        }
      }, POPUP_NAVIGATION_POLL_MS);

      popupNavigationTimeoutRef.current = window.setTimeout(() => {
        resetPendingOpen();
      }, POPUP_NAVIGATION_TIMEOUT_MS);
    },
    [resetPendingOpen],
  );

  const failOpen = useCallback(
    (message: string, failure?: Partial<OpenFailure>) => {
      const pending = pendingRef.current;
      if (pending && !pending.currentTab) {
        writePopupDocument(
          pending.popup,
          buildPreviewOpenErrorDocument({
            title: failure?.title ?? "Preview could not open",
            description: failure?.description ?? "143 could not finish connecting this browser to the preview.",
            message,
            previewID: pending.previewID,
            previewURL: pending.previewURL,
            returnURL: pending.returnURL,
          }),
        );
      }
      resetPendingOpen();
      setError(new Error(message));
      toast.error(message);
    },
    [resetPendingOpen],
  );

  const bootstrapMutation = useMutation({
    mutationFn: (id: string) =>
      bootstrapPreview ? bootstrapPreview(id) : api.previews.bootstrap(id).then((response) => response.data),
    onSuccess: (response) => {
      const pending = pendingRef.current;
      const token = response.token;
      if (!pending || !token) return;

      const contentWindow = iframeRef.current?.contentWindow;
      if (!contentWindow) {
        failOpen("Could not connect to the preview. Try opening it again.");
        return;
      }
      contentWindow.postMessage(
        { type: PREVIEW_BOOTSTRAP_TOKEN_EVENT, token },
        pending.origin,
      );
    },
    onError: (error) => {
      failOpen(error instanceof Error ? error.message : "Could not create preview access.");
    },
  });

  const launchPreview = useCallback(async ({ previewId, previewUrl, popup: providedPopup, target = "new_tab" }: LaunchPreviewInput) => {
    const safePreview = parsePreviewURL(previewUrl);
    if (!previewId || !safePreview) {
      toast.error("Preview link is unavailable.");
      setError(new Error("Preview link is unavailable."));
      return;
    }
    const { href: safePreviewURL, origin: previewOrigin } = safePreview;

    let popup: Window | null = providedPopup ?? null;
    const currentTab = target === "current_tab";
    if (!popup && !currentTab) {
      try {
        popup = window.open("about:blank", "_blank");
        if (popup) {
          popup.opener = null;
          popup.document.write(OPENING_PREVIEW_DOCUMENT);
          popup.document.close();
        }
      } catch {
        popup = null;
      }
    }

    if (!popup && !currentTab) {
      toast.error("Your browser blocked the preview tab. Allow pop-ups and try again.");
      setError(new Error("Your browser blocked the preview tab. Allow pop-ups and try again."));
      return;
    }

    clearTimeoutRef();
    const nextPending: PendingOpen = {
      previewID: previewId,
      previewURL: safePreviewURL,
      origin: previewOrigin,
      returnURL: window.location.href,
      popup,
      currentTab,
      tokenRequested: false,
    };
    pendingRef.current = nextPending;
    setError(null);
    setIsOpening(true);
    setIframeSrc(buildPreviewBootstrapSrc(previewOrigin));
    timeoutRef.current = window.setTimeout(() => {
      failOpen(PREVIEW_BOOTSTRAP_TIMEOUT_ERROR, {
        title: "Preview connection timed out",
        description: previewBootstrapTimeoutDetails(),
      });
    }, PREVIEW_BOOTSTRAP_TIMEOUT_MS);
  }, [clearTimeoutRef, failOpen]);

  useEffect(() => {
    const handleMessage = (event: MessageEvent) => {
      const pending = pendingRef.current;
      if (!pending || event.origin !== pending.origin) return;

      if (event.data?.type === PREVIEW_BOOTSTRAP_READY_EVENT) {
        if (pending.tokenRequested) return;
        pending.tokenRequested = true;
        bootstrapMutation.mutate(pending.previewID);
        return;
      }

      if (event.data?.type === PREVIEW_BOOTSTRAP_COMPLETE_EVENT) {
        clearTimeoutRef();
        setIframeSrc(undefined);
        if (pending.currentTab) {
          window.open(pending.previewURL, "_self");
        } else if (pending.popup) {
          completeOpenWhenPopupLoads(pending.popup);
          pending.popup.location.href = pending.previewURL;
        }
      }
    };

    window.addEventListener("message", handleMessage);
    return () => window.removeEventListener("message", handleMessage);
  }, [bootstrapMutation, clearTimeoutRef, completeOpenWhenPopupLoads]);

  useEffect(() => {
    return () => {
      clearTimeoutRef();
      popupLoadCleanupRef.current?.();
      popupLoadCleanupRef.current = null;
      if (popupNavigationPollRef.current !== null) {
        window.clearInterval(popupNavigationPollRef.current);
        popupNavigationPollRef.current = null;
      }
      if (popupNavigationTimeoutRef.current !== null) {
        window.clearTimeout(popupNavigationTimeoutRef.current);
        popupNavigationTimeoutRef.current = null;
      }
      if (pendingRef.current && !pendingRef.current.currentTab) {
        pendingRef.current.popup?.close();
      }
      pendingRef.current = null;
    };
  }, [clearTimeoutRef]);

  const bootstrapFrame = iframeSrc ? (
    <iframe
      ref={iframeRef}
      src={iframeSrc}
      title="Preview bootstrap"
      className="sr-only"
      aria-hidden="true"
    />
  ) : null;

  return { launchPreview, isOpening, error, bootstrapFrame };
}

export function OpenPreviewButton({
  previewId,
  previewUrl,
  label = "Open preview",
  disabled,
  variant,
  size,
  className,
  bootstrapPreview,
}: OpenPreviewButtonProps) {
  const { launchPreview, isOpening, bootstrapFrame } = usePreviewLauncher(bootstrapPreview);
  const safePreview = useMemo(() => parsePreviewURL(previewUrl), [previewUrl]);
  const safePreviewURL = safePreview?.href;
  const previewOrigin = safePreview?.origin;

  const openPreview = useCallback(() => {
    if (!previewId || !previewUrl) {
      toast.error("Preview link is unavailable.");
      return;
    }
    void launchPreview({ previewId, previewUrl });
  }, [launchPreview, previewId, previewUrl]);

  return (
    <>
      <Button
        type="button"
        variant={variant}
        size={size}
        className={className}
        onClick={openPreview}
        disabled={disabled || isOpening || !previewId || !safePreviewURL || !previewOrigin}
      >
        {isOpening ? <Loader2 className="h-4 w-4 animate-spin" /> : <ExternalLink className="h-4 w-4" />}
        {isOpening ? "Opening..." : label}
      </Button>
      {bootstrapFrame}
    </>
  );
}

function parsePreviewURL(url: string | undefined | null): { href: string; origin: string } | undefined {
  if (!url) return undefined;
  try {
    const parsed = new URL(url);
    if (parsed.protocol === "https:" || isLocalPreviewHTTP(parsed)) {
      return { href: url, origin: parsed.origin };
    }
  } catch {
    return undefined;
  }
  return undefined;
}

function isLocalPreviewHTTP(parsed: URL): boolean {
  if (parsed.protocol !== "http:") return false;
  return (
    parsed.hostname === "localhost" ||
    parsed.hostname === "127.0.0.1" ||
    parsed.hostname === "[::1]" ||
    parsed.hostname.endsWith(".localhost") ||
    parsed.hostname.endsWith(".test")
  );
}

function writePopupDocument(popup: Window | null | undefined, html: string) {
  if (!popup || popup.closed) return;
  try {
    popup.document.open();
    popup.document.write(html);
    popup.document.close();
  } catch {
    // Cross-origin or user-closed popups cannot be rewritten. The opener still
    // surfaces the toast and resets its button state.
  }
}

function buildPreviewOpenErrorDocument({
  title,
  description,
  message,
  previewID,
  previewURL,
  returnURL,
}: {
  title: string;
  description: string;
  message: string;
  previewID: string;
  previewURL: string;
  returnURL: string;
}): string {
  const escapedTitle = escapeHTML(title);
  const escapedDescription = escapeHTML(description);
  const escapedMessage = escapeHTML(message);
  const escapedPreviewID = escapeHTML(previewID);
  const escapedPreviewURL = escapeHTML(previewURL);
  const escapedPreviewHref = escapeAttribute(previewURL);
  const escapedReturnHref = escapeAttribute(returnURL);

  return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>${escapedTitle}</title><style>:root{color-scheme:light dark}html,body{height:100%;margin:0}body{font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;display:grid;place-items:center;background:Canvas;color:CanvasText}main{width:min(92vw,520px);padding:24px}.panel{border:1px solid color-mix(in srgb,CanvasText 14%,transparent);border-radius:8px;padding:22px;box-shadow:0 16px 48px color-mix(in srgb,CanvasText 12%,transparent)}.eyebrow{margin:0 0 8px;font-size:12px;color:color-mix(in srgb,CanvasText 56%,transparent)}h1{font-size:20px;line-height:1.2;margin:0}p{font-size:14px;line-height:1.5;margin:10px 0 0;color:color-mix(in srgb,CanvasText 68%,transparent)}dl{display:grid;grid-template-columns:max-content minmax(0,1fr);gap:8px 12px;margin:18px 0 0;font-size:12px}dt{color:color-mix(in srgb,CanvasText 56%,transparent)}dd{margin:0;min-width:0;overflow-wrap:anywhere}.actions{display:flex;flex-wrap:wrap;gap:10px;margin-top:20px}a{display:inline-flex;align-items:center;justify-content:center;min-height:36px;border-radius:8px;padding:0 14px;font-size:14px;font-weight:600;text-decoration:none}.primary{background:CanvasText;color:Canvas}.secondary{border:1px solid color-mix(in srgb,CanvasText 16%,transparent);color:CanvasText}</style></head><body><main><section class="panel"><p class="eyebrow">143 preview</p><h1>${escapedTitle}</h1><p>${escapedDescription}</p><dl><dt>Error</dt><dd>${escapedMessage}</dd><dt>Preview ID</dt><dd>${escapedPreviewID}</dd><dt>Preview URL</dt><dd>${escapedPreviewURL}</dd></dl><div class="actions"><a class="primary" href="${escapedReturnHref}">Back to 143</a><a class="secondary" href="${escapedPreviewHref}">Try preview URL</a></div></section></main></body></html>`;
}

function escapeHTML(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}

function escapeAttribute(value: string): string {
  return escapeHTML(value);
}
