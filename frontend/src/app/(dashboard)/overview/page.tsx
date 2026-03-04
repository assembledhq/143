"use client";

import { useEffect, useState, useCallback, useRef } from "react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { PageHeader } from "@/components/page-header";
import { IntegrationsCard } from "@/components/integrations-card";
import { AgentSettingsEditor } from "@/components/agent-settings-editor";
import { INTEGRATIONS } from "@/lib/integrations";
import type { CodexAuthStatus, CodexDeviceAuth } from "@/lib/types";

function OverviewDeviceCodeModal({ onClose, onConnected }: { onClose: () => void; onConnected: () => void }) {
  const [deviceAuth, setDeviceAuth] = useState<CodexDeviceAuth | null>(null);
  const [status, setStatus] = useState<string>("initiating");
  const [error, setError] = useState<string>("");
  const [timeLeft, setTimeLeft] = useState(0);
  const pollRef = useRef<NodeJS.Timeout | null>(null);
  const timerRef = useRef<NodeJS.Timeout | null>(null);
  const onConnectedRef = useRef(onConnected);

  useEffect(() => { onConnectedRef.current = onConnected; }, [onConnected]);

  const startAuth = useCallback(async () => {
    try {
      setStatus("initiating");
      setError("");
      const resp = await api.codexAuth.initiate();
      setDeviceAuth(resp.data);
      setTimeLeft(resp.data.expires_in);
      setStatus("pending");
    } catch {
      setError("Failed to start authentication. Please try again.");
      setStatus("error");
    }
  }, []);

  useEffect(() => { const id = setTimeout(() => { void startAuth(); }, 0); return () => clearTimeout(id); }, [startAuth]);

  useEffect(() => {
    if (status !== "pending") return;
    pollRef.current = setInterval(async () => {
      try {
        const resp = await api.codexAuth.status();
        if (resp.data.status === "completed") {
          setStatus("completed");
          if (pollRef.current) clearInterval(pollRef.current);
          if (timerRef.current) clearInterval(timerRef.current);
          setTimeout(() => { onConnectedRef.current(); }, 1500);
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
      } catch { /* ignore transient errors */ }
    }, 3000);
    timerRef.current = setInterval(() => { setTimeLeft((t) => Math.max(0, t - 1)); }, 1000);
    return () => { if (pollRef.current) clearInterval(pollRef.current); if (timerRef.current) clearInterval(timerRef.current); };
  }, [status]);

  const minutes = Math.floor(timeLeft / 60);
  const seconds = timeLeft % 60;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
      <div className="w-full max-w-md rounded-lg border bg-background p-6 shadow-lg">
        <h3 className="text-lg font-medium">Connect your ChatGPT account</h3>
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
                <code className="rounded-md border bg-muted px-4 py-2 text-2xl font-mono font-bold tracking-widest">{deviceAuth.user_code}</code>
                <Button size="sm" variant="outline" onClick={() => navigator.clipboard.writeText(deviceAuth.user_code)}>Copy</Button>
              </div>
            </div>
            <div className="space-y-2">
              <p className="text-sm text-muted-foreground">Waiting for authentication...</p>
              <div className="h-1.5 w-full rounded-full bg-muted overflow-hidden">
                <div className="h-full rounded-full bg-primary transition-all duration-1000" style={{ width: `${Math.max(0, (timeLeft / deviceAuth.expires_in) * 100)}%` }} />
              </div>
              <p className="text-xs text-muted-foreground">Expires in {minutes}:{seconds.toString().padStart(2, "0")}</p>
            </div>
          </div>
        )}
        {status === "completed" && <div className="mt-4"><p className="text-sm font-medium text-green-600">Connected successfully!</p></div>}
        {(status === "error" || status === "expired") && (
          <div className="mt-4">
            <p className="text-sm text-destructive">{error}</p>
          </div>
        )}
        <div className="mt-6 flex items-center justify-end gap-2">
          <Button variant="outline" size="sm" onClick={onClose}>{status === "completed" ? "Done" : "Cancel"}</Button>
          {(status === "error" || status === "expired") && (
            <Button size="sm" onClick={startAuth}>Try Again</Button>
          )}
        </div>
      </div>
    </div>
  );
}

function AgentSettingsModal({ onClose }: { onClose: () => void }) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
      <div className="w-full max-w-2xl rounded-lg border bg-background p-6 shadow-lg">
        <AgentSettingsEditor
          title="Edit agent settings"
          description="Update your default coding agent and auth credentials without leaving setup."
          showAdvancedLink
          onClose={onClose}
        />
      </div>
    </div>
  );
}

function AgentSetupCard() {
  const [authStatus, setAuthStatus] = useState<CodexAuthStatus | null>(null);
  const [showModal, setShowModal] = useState(false);
  const [showSettingsModal, setShowSettingsModal] = useState(false);

  const fetchStatus = useCallback(() => {
    api.codexAuth.status().then((res) => setAuthStatus(res.data)).catch(() => {});
  }, []);

  useEffect(() => { fetchStatus(); }, [fetchStatus]);

  if (authStatus?.status === "completed") {
    return (
      <Card className="py-0">
        <CardContent className="flex items-center justify-between gap-4 py-4">
          <div className="min-w-0 flex-1">
            <p className="text-sm font-medium text-foreground">Coding Agent</p>
            <p className="mt-0.5 text-sm text-muted-foreground">
              Codex is connected via ChatGPT.
            </p>
          </div>
          <Badge variant="secondary">Connected</Badge>
        </CardContent>
      </Card>
    );
  }

  return (
    <>
      <Card className="py-0">
        <CardContent className="flex items-center justify-between gap-4 py-4">
          <div className="min-w-0 flex-1">
            <p className="text-sm font-medium text-foreground">Connect your coding agent</p>
            <p className="mt-0.5 text-sm text-muted-foreground">
              Sign in with ChatGPT to let Codex fix issues automatically, or configure an API key in Settings.
            </p>
          </div>
          <div className="flex shrink-0 gap-2">
            <Button size="sm" onClick={() => setShowModal(true)}>Sign in with ChatGPT</Button>
            <Button size="sm" variant="outline" onClick={() => setShowSettingsModal(true)}>Settings</Button>
          </div>
        </CardContent>
      </Card>
      {showModal && (
        <OverviewDeviceCodeModal
          onClose={() => setShowModal(false)}
          onConnected={() => { setShowModal(false); fetchStatus(); }}
        />
      )}
      {showSettingsModal && (
        <AgentSettingsModal onClose={() => setShowSettingsModal(false)} />
      )}
    </>
  );
}

export default function Overview() {
  const [github, sentry, linear] = INTEGRATIONS;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Overview"
        description="Get started by connecting your tools."
      />

      <div className="space-y-3">
        <IntegrationsCard
          items={[
            {
              id: github.key,
              title: `Connect ${github.name}`,
              description: github.description,
              action: (
                <Button size="sm" onClick={() => api.auth.login()} aria-label="Connect GitHub">
                  Connect
                </Button>
              ),
            },
            {
              id: sentry.key,
              title: `Connect ${sentry.name}`,
              description: sentry.description,
              action: (
                <Button size="sm" onClick={() => api.auth.loginSentry()} aria-label="Connect Sentry">
                  Connect
                </Button>
              ),
            },
            {
              id: linear.key,
              title: `Connect ${linear.name}`,
              description: linear.description,
              action: <Badge variant="secondary">Coming soon</Badge>,
            },
          ]}
        />
      </div>

      <AgentSetupCard />

      <p className="text-sm text-muted-foreground">
        Once integrations are connected, 143 picks up issues, generates fixes, and opens PRs automatically.
      </p>
    </div>
  );
}
