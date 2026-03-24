"use client";

import { Label } from "@/components/ui/label";
import { Card, CardContent } from "@/components/ui/card";
import { Slider } from "@/components/ui/slider";

interface PriorityWeightsProps {
  weights: {
    customerImpact: string;
    severity: string;
    recency: string;
    revenueRisk: string;
  };
  onChange: (field: "customerImpact" | "severity" | "recency" | "revenueRisk", value: string) => void;
}

export function PriorityWeights({ weights, onChange }: PriorityWeightsProps) {
  const weightsSum = weightsTotal(weights);
  const weightsValid = areWeightsValid(weights);

  const fields = [
    { id: "w-customer", label: "Customer impact", field: "customerImpact" as const, value: weights.customerImpact },
    { id: "w-severity", label: "Severity", field: "severity" as const, value: weights.severity },
    { id: "w-recency", label: "Recency", field: "recency" as const, value: weights.recency },
    { id: "w-revenue", label: "Revenue risk", field: "revenueRisk" as const, value: weights.revenueRisk },
  ];

  return (
    <section className="space-y-3">
      <h3 className="text-[13px] font-medium text-foreground">Priority weights</h3>
      <Card>
        <CardContent>
          <div className="space-y-4">
            <div className="flex items-center justify-between">
              <p className="text-xs text-muted-foreground">
                Weights control how the PM agent scores and ranks issues.
              </p>
              <span className={`text-xs tabular-nums ${weightsValid ? "text-muted-foreground" : "text-destructive font-medium"}`}>
                Sum: {weightsSum.toFixed(2)} / 1.00
              </span>
            </div>
            {!weightsValid && (
              <p className="text-xs text-destructive">
                Weights must sum to 1.0
              </p>
            )}
            <div className="space-y-4">
              {fields.map((f) => (
                <div key={f.id} className="space-y-2">
                  <div className="flex items-center justify-between">
                    <Label htmlFor={f.id} className="text-xs text-muted-foreground">{f.label}</Label>
                    <span className="text-xs font-medium tabular-nums">{f.value}</span>
                  </div>
                  <Slider
                    id={f.id}
                    min={0}
                    max={100}
                    step={5}
                    value={[Math.round(parseFloat(f.value) * 100)]}
                    onValueChange={([v]) => onChange(f.field, (v / 100).toFixed(2))}
                  />
                </div>
              ))}
            </div>
          </div>
        </CardContent>
      </Card>
    </section>
  );
}

/** Compute the total of all weight values. */
export function weightsTotal(weights: { customerImpact: string; severity: string; recency: string; revenueRisk: string }) {
  return (
    parseFloat(weights.customerImpact || "0") +
    parseFloat(weights.severity || "0") +
    parseFloat(weights.recency || "0") +
    parseFloat(weights.revenueRisk || "0")
  );
}

/** Check if the current weight values are valid (sum to 1.0). */
export function areWeightsValid(weights: { customerImpact: string; severity: string; recency: string; revenueRisk: string }) {
  return Math.abs(weightsTotal(weights) - 1.0) < 0.01;
}
