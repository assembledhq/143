"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Laptop } from "lucide-react";
import { api } from "@/lib/api";
import { captureError } from "@/lib/errors";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import type { CliToken, ListResponse } from "@/lib/types";

function formatWhen(value?: string | null): string {
  if (!value) return "never";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "unknown";
  return date.toLocaleString();
}

// CLISessionsCard lists the user's own 143-tools device tokens (one per
// laptop, minted by `143-tools login`) with revoke. Lives on the account
// settings page; revoking a device instantly cuts its local agents off from
// every integration.
export function CLISessionsCard() {
  const queryClient = useQueryClient();
  const [error, setError] = useState("");

  const { data, isLoading } = useQuery<ListResponse<CliToken>>({
    queryKey: ["cli", "tokens"],
    queryFn: () => api.cli.listCliTokens(),
  });
  const tokens = data?.data ?? [];

  const revokeMutation = useMutation({
    mutationFn: (id: string) => api.cli.revokeCliToken(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["cli", "tokens"] }),
    onError: (err) => {
      captureError(err);
      setError("Failed to revoke the CLI session.");
    },
  });

  return (
    <section className="space-y-3">
      <div className="flex items-center gap-1.5">
        <Laptop className="size-3.5 text-muted-foreground" />
        <h2 className="text-xs font-medium text-foreground">CLI sessions</h2>
      </div>
      <p className="text-xs text-muted-foreground">
        Devices logged in with <code>143-tools login</code>. Revoking a device
        signs its CLI out and cuts its local agents off immediately.
      </p>
      {error && (
        <div className="rounded-md bg-destructive/10 px-3 py-2 text-xs text-destructive">
          {error}
        </div>
      )}
      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <div className="px-4 py-3 text-xs text-muted-foreground">Loading…</div>
          ) : tokens.length === 0 ? (
            <div className="px-4 py-3 text-xs text-muted-foreground">
              No CLI sessions. Install 143-tools and run{" "}
              <code>143-tools login</code> to add one.
            </div>
          ) : (
            <div className="divide-y divide-border">
              {tokens.map((token) => (
                <div
                  key={token.id}
                  className="flex flex-col gap-2 px-4 py-3 sm:flex-row sm:items-center sm:justify-between"
                >
                  <div className="min-w-0">
                    <div className="truncate text-xs font-medium">
                      {token.device_name || "Unknown device"}
                    </div>
                    <div className="text-xs text-muted-foreground">
                      <code>{token.token_prefix}…</code> · last used{" "}
                      {formatWhen(token.last_used_at)}
                    </div>
                  </div>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="text-destructive hover:text-destructive sm:ml-4"
                    disabled={revokeMutation.isPending}
                    onClick={() => revokeMutation.mutate(token.id)}
                  >
                    Revoke
                  </Button>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </section>
  );
}
