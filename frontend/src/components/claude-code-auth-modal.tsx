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
import { ExternalLink } from "lucide-react";
import type { ClaudeCodeInitiateResponse } from "@/lib/types";

// ClaudeCodeAuthModal drives the Claude Code subscription OAuth flow using
// authorization-code + PKCE:
//   1. POST /initiate — server generates a PKCE verifier + state and returns
//      an authorize URL.
//   2. User opens the URL, logs in, and Anthropic shows them `<code>#<state>`.
//   3. User pastes that string back into the input; POST /complete exchanges
//      it for tokens.
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
  scope?: 'org' | 'personal';
}) {
  // Capture the label + scope at mount time so they stay stable throughout
  // the auth flow.
  const [stableLabel] = useState(() => label);
  const [stableScope] = useState(() => scope);
  const connectedTimerRef = useRef<ReturnType<typeof setTimeout>>(null);
  const [initiated, setInitiated] = useState<ClaudeCodeInitiateResponse | null>(null);
  const [status, setStatus] = useState<
    "initiating" | "awaiting_paste" | "exchanging" | "completed" | "error"
  >("initiating");
  const [error, setError] = useState("");
  const [code, setCode] = useState("");

  const startAuth = useCallback(async () => {
    try {
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
    const id = setTimeout(() => {
      void startAuth();
    }, 0);
    return () => clearTimeout(id);
  }, [startAuth]);

  useEffect(() => {
    return () => {
      if (connectedTimerRef.current !== null) {
        clearTimeout(connectedTimerRef.current);
      }
    };
  }, []);

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
      setStatus("completed");
      connectedTimerRef.current = setTimeout(() => {
        onConnected?.();
      }, 1200);
    } catch (err) {
      captureError(err, { feature: "claude-code-auth" });
      const message =
        err instanceof Error && err.message
          ? err.message
          : "Failed to complete authentication. Please try again.";
      setError(message);
      setStatus("awaiting_paste");
    }
  }, [code, stableLabel, stableScope, onConnected]);

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
          Log in with Anthropic, then paste the authorization code shown after sign-in.
        </ResponsiveModalDescription>
      </ResponsiveModalHeader>
      <ResponsiveModalBody>
        {status === "initiating" && (
          <p className="mt-4 text-sm text-muted-foreground">Starting authentication...</p>
        )}

        {(status === "awaiting_paste" || status === "exchanging") && initiated && (
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
          <div className="mt-4">
            <p className="text-sm font-medium text-success">Connected successfully!</p>
          </div>
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
          <Button size="sm" onClick={startAuth}>
            Try again
          </Button>
        )}
      </ResponsiveModalFooter>
    </ResponsiveModal>
  );
}
