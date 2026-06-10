"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, Terminal } from "lucide-react";
import { api } from "@/lib/api";
import { captureError } from "@/lib/errors";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { roleLabel } from "@/lib/roles";
import type { CreatedJoinToken, JoinToken, ListResponse } from "@/lib/types";

function statusBadge(status: JoinToken["status"]) {
  if (status === "active") {
    return <Badge variant="outline" className="ml-2">active</Badge>;
  }
  return (
    <Badge variant="secondary" className="ml-2 text-muted-foreground">
      {status}
    </Badge>
  );
}

// CLIJoinTokensCard is the admin "CLI install link" surface on the Members
// settings page: create a join link, copy the one-liner, list and revoke
// active links. The plaintext token is only available in the create
// response, so the copy affordance lives in the post-create dialog.
export function CLIJoinTokensCard() {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [role, setRole] = useState("member");
  const [created, setCreated] = useState<CreatedJoinToken | null>(null);
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState("");

  const { data } = useQuery<ListResponse<JoinToken>>({
    queryKey: ["cli", "join-tokens"],
    queryFn: () => api.cli.listJoinTokens(),
  });
  const tokens = data?.data ?? [];

  const createMutation = useMutation({
    mutationFn: () =>
      api.cli.createJoinToken({ name: name.trim() || undefined, role }),
    onSuccess: (resp) => {
      setCreated(resp.data);
      setName("");
      setError("");
      void queryClient.invalidateQueries({ queryKey: ["cli", "join-tokens"] });
    },
    onError: (err) => {
      captureError(err);
      setError("Failed to create the install link.");
    },
  });

  const revokeMutation = useMutation({
    mutationFn: (id: string) => api.cli.revokeJoinToken(id),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: ["cli", "join-tokens"] }),
    onError: (err) => {
      captureError(err);
      setError("Failed to revoke the install link.");
    },
  });

  const copyInstallCommand = async () => {
    if (!created) return;
    try {
      await navigator.clipboard.writeText(created.install_command);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch (err) {
      captureError(err);
    }
  };

  return (
    <section className="space-y-3">
      <div className="flex items-center gap-1.5">
        <Terminal className="size-3.5 text-muted-foreground" />
        <h2 className="text-xs font-medium text-foreground">CLI install links</h2>
      </div>
      <p className="text-xs text-muted-foreground">
        Anyone with this link can install 143-tools and join this org by
        signing in with GitHub — one command, no pre-registration. Share it in
        Slack; revoke it here any time.
      </p>
      {error && (
        <div className="rounded-md bg-destructive/10 px-3 py-2 text-xs text-destructive">
          {error}
        </div>
      )}
      <Card>
        <CardContent className="space-y-3 p-4">
          <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
            <div className="flex-1 space-y-1.5">
              <Label htmlFor="join-token-name">Link name</Label>
              <Input
                id="join-token-name"
                placeholder="Eng team link, June 2026"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label>Role granted</Label>
              <Select value={role} onValueChange={setRole}>
                <SelectTrigger className="w-full sm:w-32">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="member">Member</SelectItem>
                  <SelectItem value="builder">Builder</SelectItem>
                  <SelectItem value="viewer">Viewer</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <Button
              size="sm"
              disabled={createMutation.isPending}
              onClick={() => createMutation.mutate()}
            >
              Create link
            </Button>
          </div>

          {tokens.length > 0 && (
            <div className="divide-y divide-border rounded-md border">
              {tokens.map((token) => (
                <div
                  key={token.id}
                  className="flex flex-col gap-2 px-3 py-2 sm:flex-row sm:items-center sm:justify-between"
                >
                  <div className="min-w-0">
                    <div className="flex items-center text-xs font-medium">
                      <span className="truncate">
                        {token.name || `${token.token_prefix}…`}
                      </span>
                      {statusBadge(token.status)}
                    </div>
                    <div className="text-xs text-muted-foreground">
                      <code>{token.token_prefix}…</code> · grants{" "}
                      {roleLabel(token.role)} · used {token.use_count}
                      {token.max_uses != null ? ` of ${token.max_uses}` : ""}{" "}
                      times
                    </div>
                  </div>
                  {token.status === "active" && (
                    <Button
                      variant="ghost"
                      size="sm"
                      className="text-destructive hover:text-destructive"
                      disabled={revokeMutation.isPending}
                      onClick={() => revokeMutation.mutate(token.id)}
                    >
                      Revoke
                    </Button>
                  )}
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      <AlertDialog open={created !== null} onOpenChange={(open) => !open && setCreated(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Install link created</AlertDialogTitle>
            <AlertDialogDescription>
              Copy the one-liner below and share it with your team. The full
              link is shown only once — after this dialog closes, only its
              prefix remains visible.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <div className="flex items-center gap-2">
            <code className="block flex-1 overflow-x-auto whitespace-nowrap rounded-md bg-muted px-3 py-2 text-xs">
              {created?.install_command}
            </code>
            <Button variant="outline" size="sm" onClick={copyInstallCommand}>
              {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
            </Button>
          </div>
          <AlertDialogFooter>
            <AlertDialogCancel>Done</AlertDialogCancel>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  );
}
