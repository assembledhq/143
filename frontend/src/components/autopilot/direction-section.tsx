"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { api } from "@/lib/api";
import { AutonomySlider } from "@/components/autopilot/autonomy-slider";
import { AutonomyReadiness } from "@/components/autopilot/autonomy-readiness";
import { ContextHealth } from "@/components/autopilot/context-health";
import { PriorityWeights, areWeightsValid } from "@/components/autopilot/priority-weights";
import { DocumentsManager } from "@/components/autopilot/documents-manager";
import type { Organization, OrgSettings, SingleResponse, Repository, ListResponse, RepoSettings, PMDecisionsResponse, PMDocument, PMPlan } from "@/lib/types";
import { DEFAULT_PM_MODEL, PM_MODELS_BY_PROVIDER } from "@/lib/model-constants";

const DEFAULT_SETTINGS: Pick<
  Required<OrgSettings>,
  "pm_schedule_hours" | "pm_model" | "priority_weights" | "product_direction" | "product_context" | "autonomy_level"
> = {
  pm_schedule_hours: 4,
  pm_model: DEFAULT_PM_MODEL,
  autonomy_level: "auto_simple",
  priority_weights: {
    customer_impact: 0.35,
    severity: 0.25,
    recency: 0.2,
    revenue_risk: 0.2,
  },
  product_direction: "",
  product_context: {
    philosophy: "",
    direction: "",
    focus_areas: [],
    avoid_areas: [],
  },
};

export function DirectionSection() {
  const queryClient = useQueryClient();

  const { data: settings } = useQuery<SingleResponse<Organization>>({
    queryKey: ["settings"],
    queryFn: () => api.settings.get(),
  });

  const { data: agentDefaultsResponse } = useQuery({
    queryKey: ["agent-defaults"],
    queryFn: () => api.settings.getAgentDefaults(),
  });

  const { data: reposResponse } = useQuery<ListResponse<Repository>>({
    queryKey: ["repositories"],
    queryFn: () => api.repositories.list(),
  });

  const { data: documentsResponse } = useQuery({
    queryKey: ["pm", "documents"],
    queryFn: () => api.pm.listDocuments(),
  });

  const { data: decisionsResponse } = useQuery<PMDecisionsResponse>({
    queryKey: ["pm", "decisions"],
    queryFn: () => api.pm.decisions({ limit: 50 }),
  });

  const { data: plansResponse } = useQuery<ListResponse<PMPlan>>({
    queryKey: ["pm", "plans"],
    queryFn: () => api.pm.list({ limit: 50 }),
  });

  const pmDocuments = (documentsResponse?.data ?? []) as PMDocument[];
  const totalCycles = plansResponse?.data?.length ?? 0;

  const reposWithCustomPM = (reposResponse?.data ?? []).filter((repo) => {
    const rs = (repo.settings ?? {}) as RepoSettings;
    return rs.pm != null;
  });

  const orgSettings = (settings?.data?.settings ?? {}) as OrgSettings;

  const enabledPmModelGroups = useMemo(() => {
    const agentConfig = orgSettings.agent_config ?? {};
    const serverDefaults = agentDefaultsResponse?.data ?? {};
    const defaultAgent = orgSettings.default_agent_type || "codex";

    return Object.entries(PM_MODELS_BY_PROVIDER)
      .filter(([providerKey, { apiKeyVar }]) => {
        const orgKey = agentConfig[providerKey]?.[apiKeyVar];
        const serverKey = (serverDefaults[providerKey] ?? {})[apiKeyVar];
        return Boolean(orgKey) || Boolean(serverKey) || providerKey === defaultAgent;
      })
      .map(([, { label, models }]) => ({ label, models }));
  }, [orgSettings.agent_config, orgSettings.default_agent_type, agentDefaultsResponse?.data]);

  const [autonomyLevel, setAutonomyLevel] = useState(DEFAULT_SETTINGS.autonomy_level);
  const [pmScheduleHours, setPmScheduleHours] = useState(String(DEFAULT_SETTINGS.pm_schedule_hours));
  const [pmModel, setPmModel] = useState(DEFAULT_SETTINGS.pm_model);
  const [productPhilosophy, setProductPhilosophy] = useState(DEFAULT_SETTINGS.product_context.philosophy);
  const [productDirection, setProductDirection] = useState(DEFAULT_SETTINGS.product_direction);
  const [focusAreas, setFocusAreas] = useState<string[]>(DEFAULT_SETTINGS.product_context.focus_areas ?? []);
  const [avoidAreas, setAvoidAreas] = useState<string[]>(DEFAULT_SETTINGS.product_context.avoid_areas ?? []);
  const [focusInput, setFocusInput] = useState("");
  const [avoidInput, setAvoidInput] = useState("");
  const [weights, setWeights] = useState({
    customerImpact: String(DEFAULT_SETTINGS.priority_weights.customer_impact),
    severity: String(DEFAULT_SETTINGS.priority_weights.severity),
    recency: String(DEFAULT_SETTINGS.priority_weights.recency),
    revenueRisk: String(DEFAULT_SETTINGS.priority_weights.revenue_risk),
  });
  const [saveStatus, setSaveStatus] = useState<"idle" | "success" | "error">("idle");

  // Sync server data into form state when it arrives or changes.
  // setState calls here are intentional: this effect synchronises external
  // (server) data into local form state, which is a recommended use of useEffect.
  const prevSettingsRef = useRef<unknown>(undefined);
  useEffect(() => {
    const settingsData = settings?.data?.settings;
    if (!settingsData || settingsData === prevSettingsRef.current) return;
    prevSettingsRef.current = settingsData;
    const s = orgSettings;
    // eslint-disable-next-line react-hooks/set-state-in-effect -- syncing server data to form state
    setAutonomyLevel(s.autonomy_level ?? DEFAULT_SETTINGS.autonomy_level);
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
    setWeights({
      customerImpact: String(s.priority_weights?.customer_impact ?? DEFAULT_SETTINGS.priority_weights.customer_impact),
      severity: String(s.priority_weights?.severity ?? DEFAULT_SETTINGS.priority_weights.severity),
      recency: String(s.priority_weights?.recency ?? DEFAULT_SETTINGS.priority_weights.recency),
      revenueRisk: String(s.priority_weights?.revenue_risk ?? DEFAULT_SETTINGS.priority_weights.revenue_risk),
    });
  }, [settings?.data?.settings]); // eslint-disable-line react-hooks/exhaustive-deps -- orgSettings is derived from settings?.data?.settings

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

  const addTag = (value: string, list: string[], setList: (v: string[]) => void) => {
    const trimmed = value.trim();
    if (!trimmed || list.includes(trimmed)) return;
    setList([...list, trimmed]);
  };

  const removeTag = (value: string, list: string[], setList: (v: string[]) => void) => {
    setList(list.filter((item) => item !== value));
  };

  const handleWeightChange = (field: "customerImpact" | "severity" | "recency" | "revenueRisk", value: string) => {
    setWeights((prev) => ({ ...prev, [field]: value }));
  };

  function handleSave() {
    mutation.mutate({
      settings: {
        autonomy_level: autonomyLevel,
        pm_schedule_hours: parseInt(pmScheduleHours, 10) || DEFAULT_SETTINGS.pm_schedule_hours,
        pm_model: pmModel,
        priority_weights: {
          customer_impact: parseFloat(weights.customerImpact),
          severity: parseFloat(weights.severity),
          recency: parseFloat(weights.recency),
          revenue_risk: parseFloat(weights.revenueRisk),
        },
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
    <div className="space-y-6">
      {/* Org defaults notice with context inheritance */}
      <div className="rounded-md border border-border bg-muted/50 px-4 py-3 space-y-2">
        <p className="text-xs text-muted-foreground">
          These are <span className="font-medium text-foreground">organization defaults</span>.
          Individual repositories can override these settings from their repository settings page.
        </p>
        {reposWithCustomPM.length > 0 && (
          <div className="space-y-1.5">
            <div className="flex flex-wrap gap-1.5 items-center">
              <span className="text-xs text-muted-foreground">Repos with custom PM context:</span>
              {reposWithCustomPM.map((repo) => {
                const rs = (repo.settings ?? {}) as RepoSettings;
                const overrides: string[] = [];
                if (rs.pm?.product_context?.philosophy) overrides.push("philosophy");
                if (rs.pm?.product_context?.direction) overrides.push("direction");
                if (rs.pm?.product_context?.focus_areas?.length) overrides.push("focus");
                if (rs.pm?.product_context?.avoid_areas?.length) overrides.push("avoid");
                if (rs.pm?.pm_model) overrides.push("model");
                return (
                  <Badge key={repo.id} variant="secondary" className="text-[11px]">
                    {repo.full_name}
                    {overrides.length > 0 && (
                      <span className="text-muted-foreground/60 ml-1">
                        ({overrides.join(", ")})
                      </span>
                    )}
                  </Badge>
                );
              })}
            </div>
            <p className="text-[11px] text-muted-foreground/60">
              Org defaults apply to all other repos. Per-repo overrides take precedence.
            </p>
          </div>
        )}
      </div>

      {/* Context Health */}
      <ContextHealth
        productContext={{
          philosophy: productPhilosophy,
          direction: productDirection,
          focus_areas: focusAreas,
          avoid_areas: avoidAreas,
        }}
        settingsUpdatedAt={settings?.data?.updated_at}
        documents={pmDocuments}
      />

      {/* Autonomy level */}
      <section className="space-y-3">
        <h3 className="text-[13px] font-medium text-foreground">Autonomy level</h3>
        <AutonomySlider value={autonomyLevel} onChange={setAutonomyLevel} />
        <AutonomyReadiness
          autonomyLevel={autonomyLevel}
          decisionSummary={decisionsResponse?.summary}
          totalCycles={totalCycles}
        />
      </section>

      {/* PM Agent */}
      <section className="space-y-3">
        <h3 className="text-[13px] font-medium text-foreground">PM agent</h3>
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
                <Select value={pmModel} onValueChange={setPmModel}>
                  <SelectTrigger id="pm-model" aria-label="PM Model">
                    <SelectValue placeholder="Select a model" />
                  </SelectTrigger>
                  <SelectContent>
                    {enabledPmModelGroups.length === 0 ? (
                      <SelectItem value={DEFAULT_PM_MODEL} disabled>
                        No providers configured
                      </SelectItem>
                    ) : (
                      enabledPmModelGroups.map((group) => (
                        <SelectGroup key={group.label}>
                          <SelectLabel>{group.label}</SelectLabel>
                          {group.models.map((model) => (
                            <SelectItem key={model} value={model}>
                              {model}
                            </SelectItem>
                          ))}
                        </SelectGroup>
                      ))
                    )}
                  </SelectContent>
                </Select>
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
        <h3 className="text-[13px] font-medium text-foreground">Product context</h3>
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
                <Label htmlFor="product-direction">Current direction</Label>
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
                  <Label htmlFor="focus-areas">Focus areas</Label>
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
                  <Label htmlFor="avoid-areas">Avoid areas</Label>
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

      {/* Reference Documents */}
      <DocumentsManager />

      {/* Priority Weights */}
      <PriorityWeights weights={weights} onChange={handleWeightChange} />

      <div className="flex items-center justify-end gap-3">
        {saveStatus === "success" && (
          <span className="text-[13px] text-emerald-600 dark:text-emerald-400">Settings saved.</span>
        )}
        {saveStatus === "error" && (
          <span className="text-[13px] text-destructive">Failed to save settings.</span>
        )}
        <Button onClick={handleSave} disabled={mutation.isPending || !areWeightsValid(weights)}>
          {mutation.isPending ? "Saving..." : "Save settings"}
        </Button>
      </div>
    </div>
  );
}
