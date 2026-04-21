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

  return (
    <div className="flex items-center justify-between gap-3 rounded-lg border bg-card px-3 py-2">
      <div className="flex min-w-0 flex-1 items-center gap-3">
        <span
          aria-label={configured ? "Configured" : "Not configured"}
          className={
            configured
              ? "inline-block h-2 w-2 shrink-0 rounded-full bg-emerald-500"
              : "inline-block h-2 w-2 shrink-0 rounded-full bg-muted-foreground/40"
          }
        />
        <span className="text-sm font-medium">{info.name}</span>
        {configured && status.maskedKey ? (
          <span className="truncate font-mono text-xs text-muted-foreground">
            {status.maskedKey}
          </span>
        ) : (
          <span className="text-xs text-muted-foreground">Not set</span>
        )}
        {isDefaultOwner && (
          <Badge variant="secondary" className="text-xs px-1.5 py-0">
            default
          </Badge>
        )}
      </div>
      <Button variant="ghost" size="sm" onClick={onEdit} className="text-xs">
        {configured ? "Edit" : "Add"}
      </Button>
    </div>
  );
}
