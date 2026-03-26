"use client";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";

interface AutopilotDirectionSummaryProps {
  philosophySummary: string;
  directionSummary: string;
  focusAreas: string[];
  avoidAreas: string[];
  autonomyLabel: string;
  documentsSummary: string;
  weightsSummary: string;
  onEditDirection: () => void;
  onManageDocuments: () => void;
  onCustomizeWeights: () => void;
  onOpenSettings: () => void;
}

function SummaryRow({
  label,
  value,
  actionLabel,
  onAction,
}: {
  label: string;
  value: React.ReactNode;
  actionLabel?: string;
  onAction?: () => void;
}) {
  return (
    <div className="flex flex-col gap-3 py-4 sm:flex-row sm:items-start sm:justify-between">
      <div className="space-y-1">
        <p className="text-sm font-medium text-foreground">{label}</p>
        <div className="text-sm text-muted-foreground">{value}</div>
      </div>
      {actionLabel && onAction && (
        <Button variant="ghost" size="sm" aria-label={actionLabel} onClick={onAction}>
          {actionLabel.replace(/^[A-Z][a-z]+\s/, "")}
        </Button>
      )}
    </div>
  );
}

function TagList({ items, emptyLabel }: { items: string[]; emptyLabel: string }) {
  if (items.length === 0) {
    return <p>{emptyLabel}</p>;
  }

  return (
    <div className="flex flex-wrap gap-2">
      {items.map((item) => (
        <Badge key={item} variant="secondary">{item}</Badge>
      ))}
    </div>
  );
}

export function AutopilotDirectionSummary({
  philosophySummary,
  directionSummary,
  focusAreas,
  avoidAreas,
  autonomyLabel,
  documentsSummary,
  weightsSummary,
  onEditDirection,
  onManageDocuments,
  onCustomizeWeights,
  onOpenSettings,
}: AutopilotDirectionSummaryProps) {
  return (
    <section className="space-y-2">
      <Separator />
      <div className="flex items-center justify-between pt-4">
        <h2 className="text-lg font-semibold text-foreground">Your Direction</h2>
        <Button variant="ghost" size="sm" aria-label="Edit direction" onClick={onEditDirection}>
          Edit
        </Button>
      </div>
      <SummaryRow
        label="Philosophy"
        value={philosophySummary}
      />
      <Separator />
      <SummaryRow
        label="Current direction"
        value={directionSummary}
      />
      <Separator />
      <SummaryRow
        label="Focus"
        value={<TagList items={focusAreas} emptyLabel="None set" />}
      />
      <Separator />
      <SummaryRow
        label="Avoid"
        value={<TagList items={avoidAreas} emptyLabel="None set" />}
      />
      <Separator />
      <SummaryRow
        label="Autonomy"
        value={autonomyLabel}
      />
      <Separator />
      <SummaryRow
        label="Documents"
        value={documentsSummary}
        actionLabel="Manage documents"
        onAction={onManageDocuments}
      />
      <Separator />
      <SummaryRow
        label="Weights"
        value={weightsSummary}
        actionLabel="Customize weights"
        onAction={onCustomizeWeights}
      />
      <Separator />
      <SummaryRow
        label="Advanced"
        value="Model, cadence, org defaults"
        actionLabel="Open settings"
        onAction={onOpenSettings}
      />
    </section>
  );
}
