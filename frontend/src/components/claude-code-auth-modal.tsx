"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { captureError } from "@/lib/errors";
import { Button } from "@/components/ui/button";
import { ErrorText } from "@/components/ui/error-notice";
import { Input } from "@/components/ui/input";
import {
  ResponsiveModal,
  ResponsiveModalBody,
  ResponsiveModalDescription,
  ResponsiveModalFooter,
  ResponsiveModalHeader,
  ResponsiveModalTitle,
} from "@/components/ui/responsive-modal";
import { Copy, ExternalLink } from "lucide-react";
import type { ClaudeCodeInitiateResponse } from "@/lib/types";

const claudeSetupTokenCommand = "claude setup-token";

// ClaudeCodeAuthModal defaults to the Claude Code setup-token flow. The
// legacy browser-code PKCE flow remains available as a fallback while the new
// auth mode rolls out.
export function ClaudeCodeAuthModal({
  onClose,
  onConnected,
  label,
  scope,
}: {
  onClose: () => void;
  onConnected?: () => void;
  label: string;
  // scope routes the pending-auth row into either the org or the caller's
  // personal credential stack. Defaults to org for backwards compatibility
  // with the admin /settings/agent flow.
  scope?: "org" | "personal";
}) {
  // Capture the label + scope at mount time so they stay stable throughout
  // the auth flow.
  const [stableLabel] = useState(() => label);
  const [stableScope] = useState(() => scope);
  const connectedTimerRef = useRef<ReturnType<typeof setTimeout>>(null);
  const [initiated, setInitiated] = useState<ClaudeCodeInitiateResponse | null>(null);
  const [mode, setMode] = useState<"setup_token" | "browser_oauth">("setup_token");
  const [status, setStatus] = useState<
    "awaiting_token" | "initiating" | "awaiting_paste" | "exchanging" | "completed" | "error"
  >("awaiting_token");
  const [error, setError] = useState("");
  const [code, setCode] = useState("");
  const [oauthToken, setOAuthToken] = useState("");

  const startBrowserAuth = useCallback(async () => {
    try {
      setMode("browser_oauth");
      setStatus("initiating");
      setError("");
      setCode("");
      const resp = await api.claudeCodeAuth.initiate(stableLabel, stableScope);
      setInitiated(resp.data);
      setStatus("awaiting_paste");
    } catch (err) {
      captureError(err, { feature: "claude-code-auth" });
      const message =
        err instanceof Error && err.message ? err.message : "Failed to start authentication. Please try again.";
      setError(message);
      setStatus("error");
    }
  }, [stableLabel, stableScope]);

  useEffect(() => {
    return () => {
      if (connectedTimerRef.current !== null) {
        clearTimeout(connectedTimerRef.current);
      }
    };
  }, []);

  const completeConnection = useCallback(() => {
    setStatus("completed");
    connectedTimerRef.current = setTimeout(() => {
      onConnected?.();
    }, 1200);
  }, [onConnected]);

  const submitOAuthToken = useCallback(async () => {
    const trimmed = oauthToken.trim();
    if (!trimmed) {
      setError("Paste the token printed by claude setup-token.");
      return;
    }
    try {
      setStatus("exchanging");
      setError("");
      await api.claudeCodeAuth.storeOAuthToken(stableLabel, trimmed, stableScope);
      completeConnection();
    } catch (err) {
      captureError(err, { feature: "claude-code-auth" });
      const message =
        err instanceof Error && err.message
          ? err.message
          : "Failed to store the Claude Code OAuth token. Please try again.";
      setError(message);
      setStatus("awaiting_token");
    }
  }, [oauthToken, stableLabel, stableScope, completeConnection]);

  const submitCode = useCallback(async () => {
    const trimmed = code.trim();
    if (!trimmed) {
      setError("Paste the code Anthropic displayed after you logged in.");
      return;
    }
    try {
      setStatus("exchanging");
      setError("");
      await api.claudeCodeAuth.complete(stableLabel, trimmed, stableScope);
      completeConnection();
    } catch (err) {
      captureError(err, { feature: "claude-code-auth" });
      const message =
        err instanceof Error && err.message
          ? err.message
          : "Failed to complete authentication. Please try again.";
      setError(message);
      setStatus("awaiting_paste");
    }
  }, [code, stableLabel, stableScope, completeConnection]);

  const copySetupCommand = useCallback(() => {
    if (!navigator.clipboard) return;
    void navigator.clipboard.writeText(claudeSetupTokenCommand).catch((err) => {
      captureError(err, { feature: "claude-code-auth-copy-command" });
    });
  }, []);

  return (
    <ResponsiveModal
      open
      onOpenChange={(nextOpen) => {
        if (!nextOpen) onClose();
      }}
      desktopClassName="sm:max-w-md"
    >
      <ResponsiveModalHeader>
        <ResponsiveModalTitle>Connect your Claude subscription</ResponsiveModalTitle>
        <ResponsiveModalDescription>
          Generate a Claude Code OAuth token locally, then paste it here.
        </ResponsiveModalDescription>
      </ResponsiveModalHeader>
      <ResponsiveModalBody>
        {mode === "setup_token" && (status === "awaiting_token" || status === "exchanging") && (
          <div className="mt-4 space-y-4">
            <div className="space-y-2">
              <p className="text-sm text-muted-foreground">1. Run this command in a terminal where Claude Code is installed:</p>
              <div className="flex items-center gap-2">
                <code className="rounded border bg-muted px-2 py-1 font-mono text-sm">
                  {claudeSetupTokenCommand}
                </code>
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  onClick={copySetupCommand}
                  aria-label="Copy claude setup-token command"
                >
                  <Copy className="h-3.5 w-3.5" />
                </Button>
              </div>
            </div>
            <div className="space-y-2">
              <p className="text-sm text-muted-foreground">2. Paste the token it prints:</p>
              <Input
                type="password"
                value={oauthToken}
                onChange={(e) => setOAuthToken(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && !e.shiftKey && status === "awaiting_token") {
                    e.preventDefault();
                    void submitOAuthToken();
                  }
                }}
                placeholder="Paste the token from claude setup-token"
                disabled={status === "exchanging"}
                autoFocus
                autoComplete="off"
                spellCheck={false}
              />
            </div>
            {error && <ErrorText className="text-sm">{error}</ErrorText>}
          </div>
        )}

        {mode === "browser_oauth" && status === "initiating" && (
          <p className="mt-4 text-sm text-muted-foreground">Starting authentication...</p>
        )}

        {mode === "browser_oauth" && (status === "awaiting_paste" || status === "exchanging") && initiated && (
          <div className="mt-4 space-y-4">
            <div className="space-y-2">
              <p className="text-sm text-muted-foreground">1. Log in to your Claude account:</p>
              <a
                href={initiated.authorize_url}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-1.5 text-sm font-medium text-primary underline"
              >
                Open Anthropic login
                <ExternalLink className="h-3.5 w-3.5" />
              </a>
            </div>
            <div className="space-y-2">
              <p className="text-sm text-muted-foreground">
                2. Paste the code Anthropic shows you after logging in:
              </p>
              <Input
                value={code}
                onChange={(e) => setCode(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && !e.shiftKey && status === "awaiting_paste") {
                    e.preventDefault();
                    void submitCode();
                  }
                }}
                placeholder="e.g. abc123#xyz789"
                disabled={status === "exchanging"}
                autoFocus
                autoComplete="off"
                spellCheck={false}
              />
              <p className="text-xs text-muted-foreground">
                The code looks like <code className="font-mono">&lt;code&gt;#&lt;state&gt;</code>. Paste the whole
                string.
              </p>
            </div>
            {error && <ErrorText className="text-sm">{error}</ErrorText>}
          </div>
        )}

        {status === "completed" && (
          <p className="text-sm font-medium text-success">Connected successfully!</p>
        )}

        {status === "error" && (
          <div className="mt-4">
            <ErrorText className="text-sm">{error}</ErrorText>
          </div>
        )}
      </ResponsiveModalBody>

      <ResponsiveModalFooter>
        <Button variant="outline" size="sm" onClick={onClose}>
          {status === "completed" ? "Done" : "Cancel"}
        </Button>
        {mode === "setup_token" && status === "awaiting_token" && (
          <Button variant="ghost" size="sm" onClick={startBrowserAuth}>
            Use browser login instead
          </Button>
        )}
        {mode === "setup_token" && status === "awaiting_token" && (
          <Button size="sm" onClick={submitOAuthToken} disabled={!oauthToken.trim()}>
            Connect
          </Button>
        )}
        {status === "awaiting_paste" && (
          <Button size="sm" onClick={submitCode} disabled={!code.trim()}>
            Connect
          </Button>
        )}
        {status === "exchanging" && (
          <Button size="sm" disabled>
            Connecting...
          </Button>
        )}
        {status === "error" && (
          <Button size="sm" onClick={mode === "browser_oauth" ? startBrowserAuth : submitOAuthToken}>
            Try again
          </Button>
        )}
      </ResponsiveModalFooter>
    </ResponsiveModal>
  );
}
