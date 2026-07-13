"use client";

import { useState } from "react";
import { ChevronRight } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import {
  Collapsible,
  CollapsibleTrigger,
  CollapsibleContent,
} from "@/components/ui/collapsible";
import { cn } from "@/lib/utils";

interface AutopilotConfigFooterProps {
  directionSummary: string;
  focusAreas: string[];
  documentsSummary: string;
  weightsSummary: string;
  canEdit?: boolean;
  onEditDirection: () => void;
  onManageDocuments: () => void;
  onOpenSettings: () => void;
}

function ConfigRow({
  label,
  value,
  actionLabel,
  onAction,
  canEdit,
}: {
  label: string;
  value: React.ReactNode;
  actionLabel: string;
  onAction: () => void;
  canEdit: boolean;
}) {
  return (
    <div className="flex flex-col items-start gap-2 py-3 sm:flex-row sm:justify-between sm:gap-4">
      <div className="min-w-0">
        <p className="text-sm font-medium text-foreground">{label}</p>
        <div className="mt-0.5 text-sm text-muted-foreground sm:mt-0 sm:inline sm:ml-0">
          {value}
        </div>
      </div>
      {canEdit ? (
        <Button
          variant="ghost"
          size="sm"
          className="min-h-11 shrink-0 self-start text-muted-foreground sm:min-h-0"
          onClick={onAction}
        >
          {actionLabel}
        </Button>
      ) : null}
    </div>
  );
}

function ConfigPill({
  label,
  actionLabel,
  canEdit,
  onClick,
  className,
}: {
  label: string;
  actionLabel: string;
  canEdit: boolean;
  onClick: () => void;
  className?: string;
}) {
  if (!canEdit) {
    return (
      <Badge variant="secondary" className={cn("shrink-0 text-xs", className)}>
        {label}
      </Badge>
    );
  }

  return (
    <Button
      type="button"
      variant="secondary"
      size="xs"
      className={cn("min-h-11 shrink-0 rounded-full px-2 text-xs sm:min-h-0", className)}
      aria-label={actionLabel}
      onClick={onClick}
    >
      {label}
    </Button>
  );
}

export function AutopilotConfigFooter({
  directionSummary,
  focusAreas,
  documentsSummary,
  weightsSummary,
  canEdit = true,
  onEditDirection,
  onManageDocuments,
  onOpenSettings,
}: AutopilotConfigFooterProps) {
  const [isOpen, setIsOpen] = useState(false);

  const directionPill = directionSummary
    ? directionSummary.length > 48
      ? directionSummary.slice(0, 48) + "\u2026"
      : directionSummary
    : "No direction";

  const focusPill =
    focusAreas.length > 0
      ? `${focusAreas.length} focus area${focusAreas.length !== 1 ? "s" : ""}`
      : "No focus";

  const docsPill = documentsSummary || "No docs";

  const weightsPill = weightsSummary || "Default weights";

  const focusDisplay =
    focusAreas.length > 0 ? (
      <span className="inline-flex flex-wrap gap-1.5 align-middle">
        {focusAreas.map((area) => (
          <Badge key={area} variant="secondary" className="text-xs">
            {area}
          </Badge>
        ))}
      </span>
    ) : (
      "Add focus areas to narrow analysis"
    );

  return (
    <section>
      <Separator />
      <Collapsible open={isOpen} onOpenChange={setIsOpen}>
        {/* Pill bar — always visible */}
        <div className="flex items-center gap-2 py-2.5">
          <div className="flex min-w-0 flex-1 flex-nowrap items-center gap-1.5 overflow-hidden pb-0.5">
            <ConfigPill
              label={directionPill}
              actionLabel="Edit Autopilot direction"
              canEdit={canEdit}
              onClick={onEditDirection}
              className="max-w-36 truncate"
            />
            <ConfigPill
              label={focusPill}
              actionLabel="Edit Autopilot focus areas"
              canEdit={canEdit}
              onClick={onEditDirection}
            />
            <ConfigPill
              label={docsPill}
              actionLabel="Manage Autopilot documents"
              canEdit={canEdit}
              onClick={onManageDocuments}
              className="hidden sm:inline-flex"
            />
            <ConfigPill
              label={weightsPill}
              actionLabel="Open Autopilot settings"
              canEdit={canEdit}
              onClick={onOpenSettings}
              className="hidden sm:inline-flex"
            />
          </div>
          <CollapsibleTrigger asChild>
            <Button
              variant="ghost"
              size="icon-xs"
              className="size-11 shrink-0 text-muted-foreground sm:size-6"
              aria-label={isOpen ? "Collapse Autopilot configuration" : "Expand Autopilot configuration"}
            >
              <ChevronRight
                className={cn(
                  "h-3.5 w-3.5 transition-transform duration-200",
                  isOpen && "rotate-90"
                )}
              />
            </Button>
          </CollapsibleTrigger>
        </div>

        {/* Expanded detail view */}
        <CollapsibleContent className="overflow-hidden">
          <div>
            <div className="divide-y divide-border/60">
              <ConfigRow
                label="Direction"
                value={directionSummary || "Set a direction to guide analysis"}
                actionLabel="Edit"
                onAction={onEditDirection}
                canEdit={canEdit}
              />
              <ConfigRow
                label="Focus"
                value={focusDisplay}
                actionLabel="Edit"
                onAction={onEditDirection}
                canEdit={canEdit}
              />
              <ConfigRow
                label="Documents"
                value={documentsSummary}
                actionLabel="Manage"
                onAction={onManageDocuments}
                canEdit={canEdit}
              />
              <ConfigRow
                label="Weights & more"
                value={weightsSummary || "Using defaults"}
                actionLabel="Settings"
                onAction={onOpenSettings}
                canEdit={canEdit}
              />
            </div>
          </div>
        </CollapsibleContent>
      </Collapsible>
    </section>
  );
}
