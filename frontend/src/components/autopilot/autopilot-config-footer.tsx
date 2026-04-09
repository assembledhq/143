"use client";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";

interface AutopilotConfigFooterProps {
  directionSummary: string;
  focusAreas: string[];
  documentsSummary: string;
  weightsSummary: string;
  onEditDirection: () => void;
  onManageDocuments: () => void;
  onOpenSettings: () => void;
}

function ConfigRow({
  label,
  value,
  actionLabel,
  onAction,
}: {
  label: string;
  value: React.ReactNode;
  actionLabel: string;
  onAction: () => void;
}) {
  return (
    <div className="flex items-start justify-between gap-4 py-3">
      <div className="min-w-0">
        <p className="text-sm font-medium text-foreground">{label}</p>
        <div className="mt-0.5 text-sm text-muted-foreground sm:mt-0 sm:inline sm:ml-0">
          {value}
        </div>
      </div>
      <Button
        variant="ghost"
        size="sm"
        className="shrink-0 text-muted-foreground"
        onClick={onAction}
      >
        {actionLabel}
      </Button>
    </div>
  );
}

export function AutopilotConfigFooter({
  directionSummary,
  focusAreas,
  documentsSummary,
  weightsSummary,
  onEditDirection,
  onManageDocuments,
  onOpenSettings,
}: AutopilotConfigFooterProps) {
  const focusDisplay = focusAreas.length > 0
    ? (
      <span className="inline-flex flex-wrap gap-1.5 align-middle">
        {focusAreas.map((area) => (
          <Badge key={area} variant="secondary" className="text-xs">{area}</Badge>
        ))}
      </span>
    )
    : "Add focus areas to narrow analysis";

  return (
    <section>
      <Separator />
      <div className="divide-y divide-border/60">
        <ConfigRow
          label="Direction"
          value={directionSummary || "Set a direction to guide analysis"}
          actionLabel="Edit"
          onAction={onEditDirection}
        />
        <ConfigRow
          label="Focus"
          value={focusDisplay}
          actionLabel="Edit"
          onAction={onEditDirection}
        />
        <ConfigRow
          label="Documents"
          value={documentsSummary}
          actionLabel="Manage"
          onAction={onManageDocuments}
        />
        <ConfigRow
          label="Weights & more"
          value={weightsSummary || "Using defaults"}
          actionLabel="Settings"
          onAction={onOpenSettings}
        />
      </div>
    </section>
  );
}
