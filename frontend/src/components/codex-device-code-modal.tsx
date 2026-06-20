"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { captureError } from "@/lib/errors";
import { pollMs } from "@/lib/poll-intervals";
import { Button } from "@/components/ui/button";
import { ErrorText } from "@/components/ui/error-notice";
import {
  ResponsiveModal,
  ResponsiveModalBody,
  ResponsiveModalDescription,
  ResponsiveModalFooter,
  ResponsiveModalHeader,
  ResponsiveModalTitle,
} from "@/components/ui/responsive-modal";
import { Check, Copy } from "lucide-react";
import type { CodexDeviceAuth } from "@/lib/types";

export function CodexDeviceCodeModal({
  onClose,
  onConnected,
  label,
  scope,
}: {
  onClose: () => void;
  onConnected?: () => void;
  label?: string;
  // scope routes the pending-auth row into either the org or the caller's
  // personal credential stack. Defaults to org for backwards compatibility
  // with the admin /settings/agent flow.
  scope?: 'org' | 'personal';
}) {
  // Capture the label + scope at mount time so they stay stable throughout
  // the auth flow.
  const [stableLabel] = useState(() => label);
  const [stableScope] = useState(() => scope);
  const [deviceAuth, setDeviceAuth] = useState<CodexDeviceAuth | null>(null);
  const [status, setStatus] = useState<string>("initiating");
  const [error, setError] = useState("");
  const [timeLeft, setTimeLeft] = useState(0);
  const [copied, setCopied] = useState(false);
  const copyTimeoutRef = useRef<NodeJS.Timeout | null>(null);
  const pollRef = useRef<NodeJS.Timeout | null>(null);
  const timerRef = useRef<NodeJS.Timeout | null>(null);
  const onConnectedRef = useRef(onConnected);

  useEffect(() => {
    onConnectedRef.current = onConnected;
  }, [onConnected]);

  const startAuth = useCallback(async () => {
    try {
      setStatus("initiating");
      setError("");
      const resp = await api.codexAuth.initiate(stableLabel, stableScope);
      setDeviceAuth(resp.data);
      setTimeLeft(resp.data.expires_in);
      setStatus("pending");
    } catch (err) {
      captureError(err, { feature: "codex-auth" });
      const message =
        err instanceof Error && err.message ? err.message : "Failed to start authentication. Please try again.";
      setError(message);
      setStatus("error");
    }
  }, [stableLabel, stableScope]);

  useEffect(() => {
    const id = setTimeout(() => {
      void startAuth();
    }, 0);
    return () => clearTimeout(id);
  }, [startAuth]);

  useEffect(() => {
    if (status !== "pending") return;

    pollRef.current = setInterval(async () => {
      try {
        const resp = await api.codexAuth.status(stableLabel, stableScope);
        if (resp.data.status === "completed") {
          setStatus("completed");
          if (pollRef.current) clearInterval(pollRef.current);
          if (timerRef.current) clearInterval(timerRef.current);
          setTimeout(() => {
            onConnectedRef.current?.();
          }, pollMs(1500));
        } else if (resp.data.status === "expired") {
          setStatus("expired");
          setError("Code expired. Please try again.");
          if (pollRef.current) clearInterval(pollRef.current);
          if (timerRef.current) clearInterval(timerRef.current);
        } else if (resp.data.status === "error") {
          setStatus("error");
          setError(resp.data.message || "Authentication failed.");
          if (pollRef.current) clearInterval(pollRef.current);
          if (timerRef.current) clearInterval(timerRef.current);
        }
      } catch (err) {
        captureError(err, { feature: "codex-auth-poll" });
      }
    }, pollMs(3000));

    timerRef.current = setInterval(() => {
      setTimeLeft((time) => Math.max(0, time - 1));
    }, 1000);

    return () => {
      if (pollRef.current) clearInterval(pollRef.current);
      if (timerRef.current) clearInterval(timerRef.current);
      if (copyTimeoutRef.current) clearTimeout(copyTimeoutRef.current);
    };
  }, [status, stableLabel, stableScope]);

  const minutes = Math.floor(timeLeft / 60);
  const seconds = timeLeft % 60;

  return (
    <ResponsiveModal
      open
      onOpenChange={(nextOpen) => {
        if (!nextOpen) onClose();
      }}
      desktopClassName="sm:max-w-md"
    >
      <ResponsiveModalHeader>
        <ResponsiveModalTitle>Connect your ChatGPT account</ResponsiveModalTitle>
        <ResponsiveModalDescription>
          Open the verification link, enter the device code, and keep this window open while authentication completes.
        </ResponsiveModalDescription>
      </ResponsiveModalHeader>
      <ResponsiveModalBody>
        {status === "initiating" && <p className="mt-4 text-sm text-muted-foreground">Starting authentication...</p>}
        {status === "pending" && deviceAuth && (
          <div className="mt-4 space-y-4">
            <div className="space-y-2">
              <p className="text-sm text-muted-foreground">1. Open this link:</p>
              <a href={deviceAuth.verification_uri} target="_blank" rel="noopener noreferrer" className="text-sm font-medium text-primary underline">{deviceAuth.verification_uri}</a>
            </div>
            <div className="space-y-2">
              <p className="text-sm text-muted-foreground">2. Enter this code:</p>
              <div className="flex items-center gap-2">
                <code className="rounded-md border bg-muted px-4 py-2 text-2xl font-mono font-semibold tracking-wider">{deviceAuth.user_code}</code>
                <Button
                  size="sm"
                  variant="outline"
                  className="min-w-[90px] transition-all duration-200"
                  onClick={() => {
                    navigator.clipboard.writeText(deviceAuth.user_code).then(() => {
                      setCopied(true);
                      if (copyTimeoutRef.current) clearTimeout(copyTimeoutRef.current);
                      copyTimeoutRef.current = setTimeout(() => setCopied(false), 2000);
                    }).catch(() => {
                      console.error("Failed to copy Codex device code");
                    });
                  }}
                >
                  {copied ? (
                    <span className="flex items-center gap-1.5 text-success">
                      <Check className="h-3.5 w-3.5" />
                      Copied
                    </span>
                  ) : (
                    <span className="flex items-center gap-1.5">
                      <Copy className="h-3.5 w-3.5" />
                      Copy
                    </span>
                  )}
                </Button>
              </div>
            </div>
            <div className="space-y-2">
              <p className="text-sm text-muted-foreground">Waiting for authentication...</p>
              <div className="h-1.5 w-full overflow-hidden rounded-full bg-muted">
                <div className="h-full rounded-full bg-primary transition-all duration-1000" style={{ width: `${Math.max(0, (timeLeft / deviceAuth.expires_in) * 100)}%` }} />
              </div>
              <p className="text-xs text-muted-foreground">Expires in {minutes}:{seconds.toString().padStart(2, "0")}</p>
            </div>
          </div>
        )}
        {status === "completed" && <p className="text-sm font-medium text-success">Connected successfully!</p>}
        {(status === "error" || status === "expired") && (
          <div className="mt-4">
            <ErrorText className="text-sm">{error}</ErrorText>
          </div>
        )}
      </ResponsiveModalBody>
      <ResponsiveModalFooter>
        <Button variant="outline" size="sm" onClick={onClose}>{status === "completed" ? "Done" : "Cancel"}</Button>
        {(status === "error" || status === "expired") && (
          <Button size="sm" onClick={startAuth}>Try again</Button>
        )}
      </ResponsiveModalFooter>
    </ResponsiveModal>
  );
}
