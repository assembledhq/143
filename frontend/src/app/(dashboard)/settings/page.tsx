"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { ChevronRight } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Slider } from "@/components/ui/slider";
import { IntegrationsCard } from "@/components/integrations-card";
import { INTEGRATIONS } from "@/lib/integrations";
import type { Organization, OrgSettings, SingleResponse } from "@/lib/types";

interface AgentEnvVar {
  name: string;
  label: string;
  sensitive?: boolean;
  placeholder?: string;
}

const AGENT_TYPES: { key: string; label: string; envVars: AgentEnvVar[] }[] = [
  {
    key: "claude_code",
    label: "Claude Code",
    envVars: [
      { name: "ANTHROPIC_API_KEY", label: "API Key", sensitive: true },
      { name: "ANTHROPIC_MODEL", label: "Model", placeholder: "e.g. claude-sonnet-4-5, opus" },
      { name: "ANTHROPIC_BASE_URL", label: "Base URL", placeholder: "Custom API endpoint (optional)" },
    ],
  },
  {
    key: "gemini_cli",
    label: "Gemini CLI",
    envVars: [
      { name: "GEMINI_API_KEY", label: "API Key", sensitive: true },
      { name: "GEMINI_MODEL", label: "Model", placeholder: "e.g. gemini-2.5-pro, gemini-2.5-flash" },
    ],
  },
  {
    key: "codex",
    label: "Codex",
    envVars: [
      { name: "OPENAI_API_KEY", label: "API Key", sensitive: true },
      { name: "OPENAI_MODEL", label: "Model", placeholder: "e.g. codex-mini, o3" },
      { name: "OPENAI_BASE_URL", label: "Base URL", placeholder: "Custom API endpoint (optional)" },
    ],
  },
];

const DEFAULT_SETTINGS: Required<OrgSettings> = {
  autonomy_level: "manual",
  execution_aggressiveness: 2,
  max_concurrent_runs: 3,
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
  agent_config: {},
};

export default function SettingsPage() {
  const [github, sentry, linear] = INTEGRATIONS;
  const queryClient = useQueryClient();

  const { data: settings } = useQuery<SingleResponse<Organization>>({
    queryKey: ["settings"],
    queryFn: () => api.settings.get(),
  });

  const { data: agentDefaultsResponse } = useQuery({
    queryKey: ["agent-defaults"],
    queryFn: () => api.settings.getAgentDefaults(),
  });
  const serverAgentDefaults = agentDefaultsResponse?.data ?? {};

  const orgSettings = (settings?.data?.settings ?? {}) as OrgSettings;

  const [autonomyLevel, setAutonomyLevel] = useState(DEFAULT_SETTINGS.autonomy_level);
  const [aggressiveness, setAggressiveness] = useState(String(DEFAULT_SETTINGS.execution_aggressiveness));
  const [maxConcurrent, setMaxConcurrent] = useState(String(DEFAULT_SETTINGS.max_concurrent_runs));
  const [autoProceed, setAutoProceed] = useState(String(DEFAULT_SETTINGS.confidence_thresholds.auto_proceed));
  const [humanReview, setHumanReview] = useState(String(DEFAULT_SETTINGS.confidence_thresholds.human_review));
  const [productDirection, setProductDirection] = useState(DEFAULT_SETTINGS.product_direction);
  const [customerImpact, setCustomerImpact] = useState(String(DEFAULT_SETTINGS.priority_weights.customer_impact));
  const [severity, setSeverity] = useState(String(DEFAULT_SETTINGS.priority_weights.severity));
  const [recency, setRecency] = useState(String(DEFAULT_SETTINGS.priority_weights.recency));
  const [revenueRisk, setRevenueRisk] = useState(String(DEFAULT_SETTINGS.priority_weights.revenue_risk));
  const [minThreshold, setMinThreshold] = useState(String(DEFAULT_SETTINGS.min_priority_threshold));
  const [agentConfig, setAgentConfig] = useState<Record<string, Record<string, string>>>({});
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [saveStatus, setSaveStatus] = useState<"idle" | "success" | "error">("idle");

  // Sync server data into form state during render (React-recommended
  // pattern for adjusting state when external data changes, avoids the
  // extra render cycle caused by setState-in-useEffect).
  const [prevSettingsRef, setPrevSettingsRef] = useState<unknown>(undefined);
  const settingsData = settings?.data?.settings;
  if (settingsData && settingsData !== prevSettingsRef) {
    setPrevSettingsRef(settingsData);
    const s = orgSettings;
    setAutonomyLevel(s.autonomy_level ?? DEFAULT_SETTINGS.autonomy_level);
    setAggressiveness(String(s.execution_aggressiveness ?? DEFAULT_SETTINGS.execution_aggressiveness));
    setMaxConcurrent(String(s.max_concurrent_runs ?? DEFAULT_SETTINGS.max_concurrent_runs));
    setAutoProceed(String(s.confidence_thresholds?.auto_proceed ?? DEFAULT_SETTINGS.confidence_thresholds.auto_proceed));
    setHumanReview(String(s.confidence_thresholds?.human_review ?? DEFAULT_SETTINGS.confidence_thresholds.human_review));
    setProductDirection(s.product_direction ?? DEFAULT_SETTINGS.product_direction);
    setCustomerImpact(String(s.priority_weights?.customer_impact ?? DEFAULT_SETTINGS.priority_weights.customer_impact));
    setSeverity(String(s.priority_weights?.severity ?? DEFAULT_SETTINGS.priority_weights.severity));
    setRecency(String(s.priority_weights?.recency ?? DEFAULT_SETTINGS.priority_weights.recency));
    setRevenueRisk(String(s.priority_weights?.revenue_risk ?? DEFAULT_SETTINGS.priority_weights.revenue_risk));
    setMinThreshold(String(s.min_priority_threshold ?? DEFAULT_SETTINGS.min_priority_threshold));
    const loadedConfig = s.agent_config ?? {};
    setAgentConfig(loadedConfig);
    if (Object.keys(loadedConfig).length > 0) setShowAdvanced(true);
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

  function handleSave() {
    // Only persist values that differ from the server default.
    const cleanedAgentConfig: Record<string, Record<string, string>> = {};
    for (const [agentKey, vars] of Object.entries(agentConfig)) {
      const filtered: Record<string, string> = {};
      const serverVars = serverAgentDefaults[agentKey] ?? {};
      for (const [k, v] of Object.entries(vars)) {
        if (v && v !== serverVars[k]) filtered[k] = v;
      }
      if (Object.keys(filtered).length > 0) {
        cleanedAgentConfig[agentKey] = filtered;
      }
    }

    const payload: Record<string, unknown> = {
      settings: {
        autonomy_level: autonomyLevel,
        execution_aggressiveness: parseInt(aggressiveness, 10),
        max_concurrent_runs: parseInt(maxConcurrent, 10),
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
        ...(Object.keys(cleanedAgentConfig).length > 0 && { agent_config: cleanedAgentConfig }),
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
              action: <Badge variant="secondary">Coming soon</Badge>,
            },
          ]}
        />
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
              <h3 className="text-[13px] font-medium text-foreground">Prioritization</h3>
              <Card>
                <CardContent>
                  <div className="space-y-4">
                    <div className="space-y-2">
                      <Label htmlFor="product-direction">Product Direction</Label>
                      <textarea
                        id="product-direction"
                        rows={3}
                        value={productDirection}
                        onChange={(e) => setProductDirection(e.target.value)}
                        placeholder="Describe your product direction to guide issue prioritization..."
                        className="border-input bg-transparent placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-ring/50 flex w-full rounded-md border px-3 py-2 text-sm shadow-xs outline-none focus-visible:ring-[3px] disabled:cursor-not-allowed disabled:opacity-50"
                      />
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

            <div className="space-y-3">
              <h3 className="text-[13px] font-medium text-foreground">Agent Configuration</h3>
              <p className="text-xs text-muted-foreground">
                Override server-level agent credentials and model selection per agent.
                Fields show the server default when no override is set.
              </p>
              {AGENT_TYPES.map((agent) => {
                const serverVars = serverAgentDefaults[agent.key] ?? {};
                const hasServerConfig = Object.keys(serverVars).length > 0;
                return (
                  <Card key={agent.key}>
                    <CardContent>
                      <div className="space-y-4">
                        <div className="flex items-center justify-between">
                          <h3 className="text-sm font-medium">{agent.label}</h3>
                          {hasServerConfig ? (
                            <span className="text-xs text-green-600">Server configured</span>
                          ) : (
                            <span className="text-xs text-muted-foreground">Not configured</span>
                          )}
                        </div>
                        {agent.envVars.map((envVar) => {
                          const serverDefault = serverVars[envVar.name] ?? "";
                          const orgOverride = agentConfig[agent.key]?.[envVar.name] ?? "";
                          const displayValue = orgOverride || serverDefault;
                          const isServerDefault = !orgOverride && !!serverDefault;
                          return (
                            <div key={envVar.name} className="space-y-1">
                              <div className="flex items-center justify-between">
                                <Label htmlFor={`${agent.key}-${envVar.name}`} className="text-xs text-muted-foreground">
                                  {envVar.label}
                                </Label>
                                {isServerDefault && (
                                  <span className="text-[10px] text-muted-foreground">server default</span>
                                )}
                              </div>
                              <Input
                                id={`${agent.key}-${envVar.name}`}
                                type={envVar.sensitive ? "password" : "text"}
                                placeholder={envVar.placeholder ?? "Not set"}
                                value={displayValue}
                                className={isServerDefault ? "text-muted-foreground" : ""}
                                onChange={(e) => {
                                  setAgentConfig((prev) => ({
                                    ...prev,
                                    [agent.key]: {
                                      ...prev[agent.key],
                                      [envVar.name]: e.target.value,
                                    },
                                  }));
                                }}
                              />
                            </div>
                          );
                        })}
                      </div>
                    </CardContent>
                  </Card>
                );
              })}
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
