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
} from "@/lib/preview-bootstrap";

const BOOTSTRAP_TIMEOUT_MS = 5_000;

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
  popup: Window | null;
  currentTab: boolean;
  tokenRequested: boolean;
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
    pendingRef.current = null;
    setIframeSrc(undefined);
    setIsOpening(false);
  }, [clearTimeoutRef]);

  const failOpen = useCallback(
    (message: string) => {
      const pending = pendingRef.current;
      if (pending && !pending.currentTab) {
        pending.popup?.close();
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
      popup,
      currentTab,
      tokenRequested: false,
    };
    pendingRef.current = nextPending;
    setError(null);
    setIsOpening(true);
    setIframeSrc(buildPreviewBootstrapSrc(previewOrigin));
    timeoutRef.current = window.setTimeout(() => {
      failOpen("Preview bootstrap timed out. Try opening it again.");
    }, BOOTSTRAP_TIMEOUT_MS);
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
        if (pending.currentTab) {
          window.open(pending.previewURL, "_self");
        } else if (pending.popup) {
          pending.popup.location.href = pending.previewURL;
        }
        resetPendingOpen();
      }
    };

    window.addEventListener("message", handleMessage);
    return () => window.removeEventListener("message", handleMessage);
  }, [bootstrapMutation, resetPendingOpen]);

  useEffect(() => {
    return () => {
      clearTimeoutRef();
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
