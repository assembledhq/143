"use client";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

export interface ProviderKeyRowProps {
  provider: string;
  info: { name: string; description: string; keyPlaceholder: string };
  status: { orgConfigured: boolean; platformAvailable: boolean; maskedKey?: string };
  isDefaultOwner: boolean;
  onEdit: () => void;
}

export function ProviderKeyRow({
  info,
  status,
  isDefaultOwner,
  onEdit,
}: ProviderKeyRowProps) {
  const configured = status.orgConfigured;
  const usingPlatformDefault = !configured && status.platformAvailable;

  let statusText: React.ReactNode;
  if (configured && status.maskedKey) {
    statusText = (
      <span className="truncate font-mono text-xs text-muted-foreground">{status.maskedKey}</span>
    );
  } else if (usingPlatformDefault) {
    statusText = (
      <span className="text-xs text-muted-foreground">Using 143&apos;s default key</span>
    );
  } else {
    statusText = <span className="text-xs text-muted-foreground">Not set</span>;
  }

  return (
    <div
      data-testid="provider-key-row"
      className="flex items-center justify-between gap-3 rounded-lg border bg-card px-3 py-2"
    >
      <div className="flex min-w-0 flex-1 items-center gap-3">
        <span
          role="status"
          aria-label={
            configured
              ? "Configured"
              : usingPlatformDefault
                ? "Using platform default"
                : "Not configured"
          }
          className={
            configured
              ? "inline-block h-2 w-2 shrink-0 rounded-full bg-success"
              : usingPlatformDefault
                ? "inline-block h-2 w-2 shrink-0 rounded-full bg-warning"
                : "inline-block h-2 w-2 shrink-0 rounded-full bg-muted-foreground/40"
          }
        />
        <span className="text-sm font-medium">{info.name}</span>
        {statusText}
        {isDefaultOwner && (
          <Badge
            variant="secondary"
            className="text-xs px-1.5 py-0"
            title="Provider for the current default model"
          >
            Current
          </Badge>
        )}
      </div>
      <Button variant="ghost" size="sm" onClick={onEdit} className="text-xs">
        {configured ? "Edit" : "Add"}
      </Button>
    </div>
  );
}
