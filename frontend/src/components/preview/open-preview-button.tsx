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
  previewOriginFromURL,
} from "@/lib/preview-bootstrap";
import { safeExternalUrl } from "@/lib/utils";

const BOOTSTRAP_TIMEOUT_MS = 15_000;

type PendingOpen = {
  previewID: string;
  previewURL: string;
  origin: string;
  popup: Window | null;
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
};

type LaunchPreviewInput = {
  previewId: string;
  previewUrl: string;
  popup?: Window | null;
};

export function usePreviewLauncher(): {
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
      pending?.popup?.close();
      resetPendingOpen();
      setError(new Error(message));
      toast.error(message);
    },
    [resetPendingOpen],
  );

  const bootstrapMutation = useMutation({
    mutationFn: (id: string) => api.previews.bootstrap(id),
    onSuccess: (response) => {
      const pending = pendingRef.current;
      const token = response.data.token;
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

  const launchPreview = useCallback(async ({ previewId, previewUrl, popup: providedPopup }: LaunchPreviewInput) => {
    const safePreviewURL = safeExternalUrl(previewUrl);
    const previewOrigin = safePreviewURL ? previewOriginFromURL(safePreviewURL) : undefined;
    if (!previewId || !safePreviewURL || !previewOrigin) {
      toast.error("Preview link is unavailable.");
      setError(new Error("Preview link is unavailable."));
      return;
    }

    let popup: Window | null = providedPopup ?? null;
    if (!popup) {
      try {
        popup = window.open("about:blank", "_blank");
        if (popup) {
          popup.opener = null;
          popup.document.write("<!doctype html><title>Opening preview</title><p>Opening preview...</p>");
          popup.document.close();
        }
      } catch {
        popup = null;
      }
    }

    if (!popup) {
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
        if (pending.popup) {
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
      pendingRef.current?.popup?.close();
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
}: OpenPreviewButtonProps) {
  const { launchPreview, isOpening, bootstrapFrame } = usePreviewLauncher();
  const safePreviewURL = useMemo(() => safeExternalUrl(previewUrl), [previewUrl]);
  const previewOrigin = useMemo(
    () => (safePreviewURL ? previewOriginFromURL(safePreviewURL) : undefined),
    [safePreviewURL],
  );

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
