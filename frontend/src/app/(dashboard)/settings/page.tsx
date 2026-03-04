"use client";

import { useState } from "react";
import Link from "next/link";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { ChevronRight, X } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Slider } from "@/components/ui/slider";
import { IntegrationsCard } from "@/components/integrations-card";
import { INTEGRATIONS } from "@/lib/integrations";
import type { Organization, OrgSettings, SingleResponse } from "@/lib/types";

const DEFAULT_SETTINGS: Required<OrgSettings> = {
  autonomy_level: "manual",
  execution_aggressiveness: 2,
  max_concurrent_runs: 3,
  pm_schedule_hours: 4,
  pm_model: "sonnet",
  confidence_thresholds: {
    auto_proceed: 0.8,
    human_review: 0.5,
  },
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
  agent_config: {},
  default_agent_type: "codex",
};

export default function SettingsPage() {
  const [github, sentry, linear] = INTEGRATIONS;
  const queryClient = useQueryClient();

  const { data: settings } = useQuery<SingleResponse<Organization>>({
    queryKey: ["settings"],
    queryFn: () => api.settings.get(),
  });

  const { data: integrationsResp } = useQuery({
    queryKey: ["integrations"],
    queryFn: () => api.integrations.list(),
  });
  const linearIntegration = integrationsResp?.data?.find(
    (integration) => integration.provider === "linear" && integration.status === "active"
  );
  const orgSettings = (settings?.data?.settings ?? {}) as OrgSettings;

  const [autonomyLevel, setAutonomyLevel] = useState(DEFAULT_SETTINGS.autonomy_level);
  const [aggressiveness, setAggressiveness] = useState(String(DEFAULT_SETTINGS.execution_aggressiveness));
  const [maxConcurrent, setMaxConcurrent] = useState(String(DEFAULT_SETTINGS.max_concurrent_runs));
  const [pmScheduleHours, setPmScheduleHours] = useState(String(DEFAULT_SETTINGS.pm_schedule_hours));
  const [pmModel, setPmModel] = useState(DEFAULT_SETTINGS.pm_model);
  const [autoProceed, setAutoProceed] = useState(String(DEFAULT_SETTINGS.confidence_thresholds.auto_proceed));
  const [humanReview, setHumanReview] = useState(String(DEFAULT_SETTINGS.confidence_thresholds.human_review));
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
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [saveStatus, setSaveStatus] = useState<"idle" | "success" | "error">("idle");

  // Sync server data into form state.
  const [prevSettingsRef, setPrevSettingsRef] = useState<unknown>(undefined);
  const settingsData = settings?.data?.settings;
  if (settingsData && settingsData !== prevSettingsRef) {
    setPrevSettingsRef(settingsData);
    const s = orgSettings;
    setAutonomyLevel(s.autonomy_level ?? DEFAULT_SETTINGS.autonomy_level);
    setAggressiveness(String(s.execution_aggressiveness ?? DEFAULT_SETTINGS.execution_aggressiveness));
    setMaxConcurrent(String(s.max_concurrent_runs ?? DEFAULT_SETTINGS.max_concurrent_runs));
    setPmScheduleHours(String(s.pm_schedule_hours ?? DEFAULT_SETTINGS.pm_schedule_hours));
    setPmModel(s.pm_model ?? DEFAULT_SETTINGS.pm_model);
    setAutoProceed(String(s.confidence_thresholds?.auto_proceed ?? DEFAULT_SETTINGS.confidence_thresholds.auto_proceed));
    setHumanReview(String(s.confidence_thresholds?.human_review ?? DEFAULT_SETTINGS.confidence_thresholds.human_review));
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

  const connectLinearMutation = useMutation({
    mutationFn: () => api.integrations.connectLinear(),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["integrations"] });
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
    const payload: Record<string, unknown> = {
      settings: {
        autonomy_level: autonomyLevel,
        execution_aggressiveness: parseInt(aggressiveness, 10),
        max_concurrent_runs: parseInt(maxConcurrent, 10),
        pm_schedule_hours: parseInt(pmScheduleHours, 10),
        pm_model: pmModel,
        confidence_thresholds: {
          auto_proceed: parseFloat(autoProceed),
          human_review: parseFloat(humanReview),
        },
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
    };
    mutation.mutate(payload);
  }

  return (
    <div className="space-y-8">
      <section className="space-y-3">
        <h2 className="text-[13px] font-medium text-foreground">General</h2>
        <Card>
          <CardContent>
            <div className="space-y-2">
              <Label htmlFor="org-name">Organization Name</Label>
              <Input
                id="org-name"
                value={settings?.data?.name ?? ""}
                disabled
                className="bg-muted"
              />
            </div>
          </CardContent>
        </Card>
      </section>

      <section className="space-y-3">
        <h2 className="text-[13px] font-medium text-foreground">Integrations</h2>
        <IntegrationsCard
          items={[
            {
              id: github.key,
              title: github.name,
              description: github.description,
              action: (
                <Button size="sm" onClick={() => api.auth.login()} aria-label="Connect GitHub">
                  Connect
                </Button>
              ),
            },
            {
              id: sentry.key,
              title: sentry.name,
              description: sentry.description,
              action: <Badge variant="secondary">Coming soon</Badge>,
            },
            {
              id: linear.key,
              title: linear.name,
              description: linear.description,
              action: (
                <Button
                  size="sm"
                  aria-label={linearIntegration ? "Linear Connected" : "Connect Linear"}
                  loading={connectLinearMutation.isPending}
                  disabled={Boolean(linearIntegration) || connectLinearMutation.isPending}
                  onClick={() => connectLinearMutation.mutate()}
                >
                  {linearIntegration ? "Connected" : "Connect"}
                </Button>
              ),
            },
          ]}
        />
      </section>

      <section className="space-y-3">
        <h2 className="text-[13px] font-medium text-foreground">Agent Setup</h2>
        <Card>
          <CardContent className="space-y-3">
            <p className="text-sm text-muted-foreground">
              Agent setup is now managed in its own focused settings page.
            </p>
            <Button asChild size="sm" variant="outline">
              <Link href="/settings/agents">Open Agent Settings</Link>
            </Button>
          </CardContent>
        </Card>
      </section>

      <section className="space-y-3">
        <h2 className="text-[13px] font-medium text-foreground">Agent Execution</h2>
        <Card>
          <CardContent>
            <div className="space-y-6">
              <div className="space-y-3">
                <Label>Autonomy Level</Label>
                <RadioGroup
                  value={autonomyLevel}
                  onValueChange={(v) => setAutonomyLevel(v as OrgSettings["autonomy_level"] & string)}
                  className="grid grid-cols-3 gap-3"
                >
                  {[
                    { value: "manual", label: "Manual", description: "Admin triggers all runs" },
                    { value: "auto_simple", label: "Auto (simple)", description: "Auto-run simple issues" },
                    { value: "auto_all", label: "Auto (all)", description: "Auto-run all eligible" },
                  ].map((option) => (
                    <label
                      key={option.value}
                      className={`relative flex cursor-pointer flex-col rounded-lg border p-3 transition-colors ${
                        autonomyLevel === option.value
                          ? "border-primary bg-primary/5"
                          : "border-input hover:bg-muted/50"
                      }`}
                    >
                      <div className="flex items-center gap-2">
                        <RadioGroupItem value={option.value} />
                        <span className="text-sm font-medium">{option.label}</span>
                      </div>
                      <span className="mt-1 pl-6 text-xs text-muted-foreground">
                        {option.description}
                      </span>
                    </label>
                  ))}
                </RadioGroup>
              </div>

              <div className="space-y-3">
                <Label>Execution Aggressiveness</Label>
                <RadioGroup
                  value={aggressiveness}
                  onValueChange={setAggressiveness}
                  className="grid grid-cols-4 gap-3"
                >
                  {[
                    { value: "1", label: "Conservative", description: "Minimal changes" },
                    { value: "2", label: "Moderate", description: "Balanced approach" },
                    { value: "3", label: "Aggressive", description: "More changes" },
                    { value: "4", label: "Maximum", description: "Full autonomy" },
                  ].map((option) => (
                    <label
                      key={option.value}
                      className={`relative flex cursor-pointer flex-col rounded-lg border p-3 transition-colors ${
                        aggressiveness === option.value
                          ? "border-primary bg-primary/5"
                          : "border-input hover:bg-muted/50"
                      }`}
                    >
                      <div className="flex items-center gap-2">
                        <RadioGroupItem value={option.value} />
                        <span className="text-sm font-medium">{option.label}</span>
                      </div>
                      <span className="mt-1 pl-6 text-xs text-muted-foreground">
                        {option.description}
                      </span>
                    </label>
                  ))}
                </RadioGroup>
              </div>

              <div className="space-y-2">
                <Label htmlFor="max-concurrent">Max Concurrent Runs</Label>
                <Input
                  id="max-concurrent"
                  type="number"
                  min={1}
                  max={10}
                  value={maxConcurrent}
                  onChange={(e) => setMaxConcurrent(e.target.value)}
                />
              </div>
            </div>
          </CardContent>
        </Card>
      </section>

      <section className="space-y-3">
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="flex items-center gap-1.5 px-0 text-[13px] font-medium text-muted-foreground hover:text-foreground hover:bg-transparent"
          onClick={() => setShowAdvanced((v) => !v)}
        >
          <ChevronRight className={`h-3.5 w-3.5 transition-transform ${showAdvanced ? "rotate-90" : ""}`} />
          Advanced Settings
        </Button>
        {showAdvanced && (
          <div className="space-y-6">
            <div className="space-y-3">
              <h3 className="text-[13px] font-medium text-foreground">Confidence Thresholds</h3>
              <Card>
                <CardContent>
                  <div className="space-y-6">
                    <div className="space-y-3">
                      <div className="flex items-center justify-between">
                        <Label htmlFor="auto-proceed">Auto-proceed Threshold</Label>
                        <span className="text-sm font-medium tabular-nums">{autoProceed}</span>
                      </div>
                      <Slider
                        id="auto-proceed"
                        min={0}
                        max={100}
                        step={5}
                        value={[Math.round(parseFloat(autoProceed) * 100)]}
                        onValueChange={([v]) => setAutoProceed((v / 100).toFixed(2))}
                      />
                      <p className="text-xs text-muted-foreground">
                        Minimum confidence score to proceed without human review.
                      </p>
                    </div>

                    <div className="space-y-3">
                      <div className="flex items-center justify-between">
                        <Label htmlFor="human-review">Human Review Threshold</Label>
                        <span className="text-sm font-medium tabular-nums">{humanReview}</span>
                      </div>
                      <Slider
                        id="human-review"
                        min={0}
                        max={100}
                        step={5}
                        value={[Math.round(parseFloat(humanReview) * 100)]}
                        onValueChange={([v]) => setHumanReview((v / 100).toFixed(2))}
                      />
                      <p className="text-xs text-muted-foreground">
                        Below this score, issues are flagged for human review.
                      </p>
                    </div>
                  </div>
                </CardContent>
              </Card>
            </div>

            <div className="space-y-3">
              <h3 className="text-[13px] font-medium text-foreground">PM Agent</h3>
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
                        placeholder="sonnet"
                      />
                      <p className="text-xs text-muted-foreground">
                        The LLM model used for PM planning.
                      </p>
                    </div>
                  </div>
                </CardContent>
              </Card>
            </div>

            <div className="space-y-3">
              <h3 className="text-[13px] font-medium text-foreground">Prioritization</h3>
              <Card>
                <CardContent>
                  <div className="space-y-4">
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

                    <div className="space-y-4">
                      <div className="flex items-center justify-between">
                        <Label>Priority Weights</Label>
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
                    </div>

                    <div className="space-y-3">
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
            </div>

          </div>
        )}
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
