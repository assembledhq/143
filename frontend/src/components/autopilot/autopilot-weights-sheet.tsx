"use client";

import { useMemo, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Slider } from "@/components/ui/slider";
import { queryKeys } from "@/lib/query-keys";
import type { OrgSettings } from "@/lib/types";
import { DEFAULT_PRIORITY_WEIGHTS } from "./autopilot-helpers";

function clamp(value: number): number {
  return Math.max(0, Math.min(1, Number(value.toFixed(2))));
}

function WeightRow({
  label,
  value,
  onChange,
}: {
  label: string;
  value: number;
  onChange: (value: number) => void;
}) {
  const lowerLabel = label.toLowerCase();
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between">
        <Label>{label}</Label>
        <span className="text-sm text-muted-foreground">{value.toFixed(2)}</span>
      </div>
      <div className="flex items-center gap-2">
        <Button
          variant="outline"
          size="sm"
          aria-label={`${label} decrease`}
          onClick={() => onChange(clamp(value - 0.05))}
        >
          -
        </Button>
        <Slider
          value={[Math.round(value * 100)]}
          min={0}
          max={100}
          step={5}
          onValueChange={([next]) => onChange(clamp(next / 100))}
          aria-label={label}
        />
        <Button
          variant="outline"
          size="sm"
          aria-label={`${label} increase`}
          onClick={() => onChange(clamp(value + 0.05))}
        >
          +
        </Button>
      </div>
      <p className="sr-only">{lowerLabel}</p>
    </div>
  );
}

export function AutopilotWeightsSheet({
  open,
  onOpenChange,
  weights,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  weights: NonNullable<OrgSettings["priority_weights"]>;
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      {open && (
        <AutopilotWeightsSheetBody
          key={JSON.stringify(weights)}
          onOpenChange={onOpenChange}
          weights={weights}
        />
      )}
    </Sheet>
  );
}

function AutopilotWeightsSheetBody({
  onOpenChange,
  weights,
}: {
  onOpenChange: (open: boolean) => void;
  weights: NonNullable<OrgSettings["priority_weights"]>;
}) {
  const queryClient = useQueryClient();
  const [customerImpact, setCustomerImpact] = useState(weights.customer_impact ?? DEFAULT_PRIORITY_WEIGHTS.customer_impact);
  const [severity, setSeverity] = useState(weights.severity ?? DEFAULT_PRIORITY_WEIGHTS.severity);
  const [recency, setRecency] = useState(weights.recency ?? DEFAULT_PRIORITY_WEIGHTS.recency);
  const [revenueRisk, setRevenueRisk] = useState(weights.revenue_risk ?? DEFAULT_PRIORITY_WEIGHTS.revenue_risk);

  const sum = useMemo(
    () => customerImpact + severity + recency + revenueRisk,
    [customerImpact, recency, revenueRisk, severity]
  );
  const valid = Math.abs(sum - 1) < 0.01;

  const mutation = useMutation({
    mutationFn: (payload: Record<string, unknown>) => api.settings.update(payload),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.settings.all });
      onOpenChange(false);
    },
    onError: (error) => {
      console.error("failed to save autopilot weights", error);
    },
  });

  function handleSave() {
    mutation.mutate({
      settings: {
        priority_weights: {
          customer_impact: customerImpact,
          severity,
          recency,
          revenue_risk: revenueRisk,
        },
      },
    });
  }

  return (
    <SheetContent className="w-full sm:max-w-xl">
      <SheetHeader>
        <SheetTitle>Customize weights</SheetTitle>
        <SheetDescription>Control how Autopilot ranks issues.</SheetDescription>
      </SheetHeader>
      <div className="mt-6 space-y-5">
        <p className={`text-sm ${valid ? "text-muted-foreground" : "text-destructive"}`}>
          Sum: {sum.toFixed(2)} / 1.00
        </p>
        <WeightRow label="Customer impact" value={customerImpact} onChange={setCustomerImpact} />
        <WeightRow label="Severity" value={severity} onChange={setSeverity} />
        <WeightRow label="Recency" value={recency} onChange={setRecency} />
        <WeightRow label="Revenue risk" value={revenueRisk} onChange={setRevenueRisk} />
        <div className="flex justify-end gap-2">
          <Button variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button onClick={handleSave} disabled={!valid || mutation.isPending}>
            {mutation.isPending ? "Saving..." : "Save"}
          </Button>
        </div>
      </div>
    </SheetContent>
  );
}
