"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Slider } from "@/components/ui/slider";
import { PageHeader } from "@/components/page-header";
import { X } from "lucide-react";
import type { Organization, OrgSettings, SingleResponse } from "@/lib/types";
import { DEFAULT_PM_MODEL } from "@/lib/model-constants";

const DEFAULT_SETTINGS: Pick<
  Required<OrgSettings>,
  "pm_schedule_hours" | "pm_model" | "priority_weights" | "min_priority_threshold" | "product_direction" | "product_context"
> = {
  pm_schedule_hours: 4,
  pm_model: DEFAULT_PM_MODEL,
  priority_weights: {
    customer_impact: 0.35,
    severity: 0.25,
    recency: 0.2,
    revenue_risk: 0.2,
  },
  min_priority_threshold: 20,
  product_direction: "",
  product_context: {
    philosophy: "",
    direction: "",
    focus_areas: [],
    avoid_areas: [],
  },
};

export default function PrioritizationPage() {
  const queryClient = useQueryClient();

  const { data: settings } = useQuery<SingleResponse<Organization>>({
    queryKey: ["settings"],
    queryFn: () => api.settings.get(),
  });

  const orgSettings = (settings?.data?.settings ?? {}) as OrgSettings;

  const [pmScheduleHours, setPmScheduleHours] = useState(String(DEFAULT_SETTINGS.pm_schedule_hours));
  const [pmModel, setPmModel] = useState(DEFAULT_SETTINGS.pm_model);
  const [productPhilosophy, setProductPhilosophy] = useState(DEFAULT_SETTINGS.product_context.philosophy);
  const [productDirection, setProductDirection] = useState(DEFAULT_SETTINGS.product_direction);
  const [focusAreas, setFocusAreas] = useState<string[]>(DEFAULT_SETTINGS.product_context.focus_areas ?? []);
  const [avoidAreas, setAvoidAreas] = useState<string[]>(DEFAULT_SETTINGS.product_context.avoid_areas ?? []);
  const [focusInput, setFocusInput] = useState("");
  const [avoidInput, setAvoidInput] = useState("");
  const [customerImpact, setCustomerImpact] = useState(String(DEFAULT_SETTINGS.priority_weights.customer_impact));
  const [severity, setSeverity] = useState(String(DEFAULT_SETTINGS.priority_weights.severity));
  const [recency, setRecency] = useState(String(DEFAULT_SETTINGS.priority_weights.recency));
  const [revenueRisk, setRevenueRisk] = useState(String(DEFAULT_SETTINGS.priority_weights.revenue_risk));
  const [minThreshold, setMinThreshold] = useState(String(DEFAULT_SETTINGS.min_priority_threshold));
  const [saveStatus, setSaveStatus] = useState<"idle" | "success" | "error">("idle");

  // Sync server data into form state.
  const [prevSettingsRef, setPrevSettingsRef] = useState<unknown>(undefined);
  const settingsData = settings?.data?.settings;
  if (settingsData && settingsData !== prevSettingsRef) {
    setPrevSettingsRef(settingsData);
    const s = orgSettings;
    setPmScheduleHours(String(s.pm_schedule_hours ?? DEFAULT_SETTINGS.pm_schedule_hours));
    setPmModel(s.pm_model ?? DEFAULT_SETTINGS.pm_model);
    const productContext = s.product_context;
    setProductPhilosophy(productContext?.philosophy ?? DEFAULT_SETTINGS.product_context.philosophy);
    setProductDirection(
      productContext?.direction ??
        s.product_direction ??
        DEFAULT_SETTINGS.product_direction
    );
    setFocusAreas(productContext?.focus_areas ?? DEFAULT_SETTINGS.product_context.focus_areas ?? []);
    setAvoidAreas(productContext?.avoid_areas ?? DEFAULT_SETTINGS.product_context.avoid_areas ?? []);
    setCustomerImpact(String(s.priority_weights?.customer_impact ?? DEFAULT_SETTINGS.priority_weights.customer_impact));
    setSeverity(String(s.priority_weights?.severity ?? DEFAULT_SETTINGS.priority_weights.severity));
    setRecency(String(s.priority_weights?.recency ?? DEFAULT_SETTINGS.priority_weights.recency));
    setRevenueRisk(String(s.priority_weights?.revenue_risk ?? DEFAULT_SETTINGS.priority_weights.revenue_risk));
    setMinThreshold(String(s.min_priority_threshold ?? DEFAULT_SETTINGS.min_priority_threshold));
  }

  const mutation = useMutation({
    mutationFn: (data: Record<string, unknown>) => api.settings.update(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["settings"] });
      setSaveStatus("success");
      setTimeout(() => setSaveStatus("idle"), 2000);
    },
    onError: () => {
      setSaveStatus("error");
      setTimeout(() => setSaveStatus("idle"), 3000);
    },
  });

  const weightsSum =
    parseFloat(customerImpact || "0") +
    parseFloat(severity || "0") +
    parseFloat(recency || "0") +
    parseFloat(revenueRisk || "0");
  const weightsValid = Math.abs(weightsSum - 1.0) < 0.01;

  const addTag = (value: string, list: string[], setList: (v: string[]) => void) => {
    const trimmed = value.trim();
    if (!trimmed || list.includes(trimmed)) return;
    setList([...list, trimmed]);
  };

  const removeTag = (value: string, list: string[], setList: (v: string[]) => void) => {
    setList(list.filter((item) => item !== value));
  };

  function handleSave() {
    mutation.mutate({
      settings: {
        pm_schedule_hours: parseInt(pmScheduleHours, 10),
        pm_model: pmModel,
        priority_weights: {
          customer_impact: parseFloat(customerImpact),
          severity: parseFloat(severity),
          recency: parseFloat(recency),
          revenue_risk: parseFloat(revenueRisk),
        },
        min_priority_threshold: parseInt(minThreshold, 10),
        product_direction: productDirection,
        product_context: {
          philosophy: productPhilosophy,
          direction: productDirection,
          focus_areas: focusAreas,
          avoid_areas: avoidAreas,
        },
      },
    });
  }

  return (
    <div className="space-y-8">
      <PageHeader
        title="Prioritization"
        description="Define product context and how the PM agent prioritizes work."
      />

      {/* PM Agent */}
      <section className="space-y-3">
        <h2 className="text-[13px] font-medium text-foreground">PM Agent</h2>
        <Card>
          <CardContent>
            <div className="grid gap-4 md:grid-cols-2">
              <div className="space-y-2">
                <Label htmlFor="pm-schedule">Schedule (hours)</Label>
                <Input
                  id="pm-schedule"
                  type="number"
                  min={1}
                  max={24}
                  value={pmScheduleHours}
                  onChange={(e) => setPmScheduleHours(e.target.value)}
                  placeholder="4"
                />
                <p className="text-xs text-muted-foreground">
                  How often the PM agent runs automatically.
                </p>
              </div>
              <div className="space-y-2">
                <Label htmlFor="pm-model">PM Model</Label>
                <Input
                  id="pm-model"
                  value={pmModel}
                  onChange={(e) => setPmModel(e.target.value)}
                  placeholder={DEFAULT_PM_MODEL}
                />
                <p className="text-xs text-muted-foreground">
                  The LLM model used for PM planning.
                </p>
              </div>
            </div>
          </CardContent>
        </Card>
      </section>

      {/* Product Context */}
      <section className="space-y-3">
        <h2 className="text-[13px] font-medium text-foreground">Product Context</h2>
        <Card>
          <CardContent>
            <div className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="product-philosophy">Philosophy</Label>
                <Textarea
                  id="product-philosophy"
                  rows={4}
                  value={productPhilosophy}
                  onChange={(e) => setProductPhilosophy(e.target.value)}
                  placeholder="Describe how the PM should think about tradeoffs, risk, and fix style."
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="product-direction">Current Direction</Label>
                <Textarea
                  id="product-direction"
                  rows={3}
                  value={productDirection}
                  onChange={(e) => setProductDirection(e.target.value)}
                  placeholder="What is the team focused on this quarter?"
                />
              </div>
              <div className="grid gap-4 md:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="focus-areas">Focus Areas</Label>
                  <Input
                    id="focus-areas"
                    value={focusInput}
                    onChange={(e) => setFocusInput(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" || e.key === ",") {
                        e.preventDefault();
                        addTag(focusInput, focusAreas, setFocusAreas);
                        setFocusInput("");
                      }
                    }}
                    placeholder="Add focus area and press Enter"
                  />
                  <div className="flex flex-wrap gap-2">
                    {focusAreas.map((area) => (
                      <Badge key={area} variant="secondary" className="text-[11px]">
                        {area}
                        <Button
                          variant="ghost"
                          size="sm"
                          className="ml-1 h-4 w-4 p-0"
                          onClick={() => removeTag(area, focusAreas, setFocusAreas)}
                        >
                          <X className="h-3 w-3" />
                        </Button>
                      </Badge>
                    ))}
                  </div>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="avoid-areas">Avoid Areas</Label>
                  <Input
                    id="avoid-areas"
                    value={avoidInput}
                    onChange={(e) => setAvoidInput(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" || e.key === ",") {
                        e.preventDefault();
                        addTag(avoidInput, avoidAreas, setAvoidAreas);
                        setAvoidInput("");
                      }
                    }}
                    placeholder="Add avoid area and press Enter"
                  />
                  <div className="flex flex-wrap gap-2">
                    {avoidAreas.map((area) => (
                      <Badge key={area} variant="secondary" className="text-[11px]">
                        {area}
                        <Button
                          variant="ghost"
                          size="sm"
                          className="ml-1 h-4 w-4 p-0"
                          onClick={() => removeTag(area, avoidAreas, setAvoidAreas)}
                        >
                          <X className="h-3 w-3" />
                        </Button>
                      </Badge>
                    ))}
                  </div>
                </div>
              </div>
            </div>
          </CardContent>
        </Card>
      </section>

      {/* Priority Weights */}
      <section className="space-y-3">
        <h2 className="text-[13px] font-medium text-foreground">Priority Weights</h2>
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
                <div className="space-y-2">
                  <div className="flex items-center justify-between">
                    <Label htmlFor="w-customer" className="text-xs text-muted-foreground">Customer Impact</Label>
                    <span className="text-xs font-medium tabular-nums">{customerImpact}</span>
                  </div>
                  <Slider
                    id="w-customer"
                    min={0}
                    max={100}
                    step={5}
                    value={[Math.round(parseFloat(customerImpact) * 100)]}
                    onValueChange={([v]) => setCustomerImpact((v / 100).toFixed(2))}
                  />
                </div>
                <div className="space-y-2">
                  <div className="flex items-center justify-between">
                    <Label htmlFor="w-severity" className="text-xs text-muted-foreground">Severity</Label>
                    <span className="text-xs font-medium tabular-nums">{severity}</span>
                  </div>
                  <Slider
                    id="w-severity"
                    min={0}
                    max={100}
                    step={5}
                    value={[Math.round(parseFloat(severity) * 100)]}
                    onValueChange={([v]) => setSeverity((v / 100).toFixed(2))}
                  />
                </div>
                <div className="space-y-2">
                  <div className="flex items-center justify-between">
                    <Label htmlFor="w-recency" className="text-xs text-muted-foreground">Recency</Label>
                    <span className="text-xs font-medium tabular-nums">{recency}</span>
                  </div>
                  <Slider
                    id="w-recency"
                    min={0}
                    max={100}
                    step={5}
                    value={[Math.round(parseFloat(recency) * 100)]}
                    onValueChange={([v]) => setRecency((v / 100).toFixed(2))}
                  />
                </div>
                <div className="space-y-2">
                  <div className="flex items-center justify-between">
                    <Label htmlFor="w-revenue" className="text-xs text-muted-foreground">Revenue Risk</Label>
                    <span className="text-xs font-medium tabular-nums">{revenueRisk}</span>
                  </div>
                  <Slider
                    id="w-revenue"
                    min={0}
                    max={100}
                    step={5}
                    value={[Math.round(parseFloat(revenueRisk) * 100)]}
                    onValueChange={([v]) => setRevenueRisk((v / 100).toFixed(2))}
                  />
                </div>
              </div>

              <div className="space-y-3 pt-2">
                <div className="flex items-center justify-between">
                  <Label htmlFor="min-threshold">Minimum Score Threshold</Label>
                  <span className="text-sm font-medium tabular-nums">{minThreshold}</span>
                </div>
                <Slider
                  id="min-threshold"
                  min={0}
                  max={100}
                  step={1}
                  value={[parseInt(minThreshold, 10) || 0]}
                  onValueChange={([v]) => setMinThreshold(String(v))}
                />
                <p className="text-xs text-muted-foreground">
                  Issues scoring below this threshold will not be auto-processed.
                </p>
              </div>
            </div>
          </CardContent>
        </Card>
      </section>

      <div className="flex items-center gap-3">
        <Button onClick={handleSave} disabled={mutation.isPending || !weightsValid}>
          {mutation.isPending ? "Saving..." : "Save Settings"}
        </Button>
        {saveStatus === "success" && (
          <span className="text-sm text-green-600">Settings saved.</span>
        )}
        {saveStatus === "error" && (
          <span className="text-sm text-destructive">Failed to save settings.</span>
        )}
      </div>
    </div>
  );
}
