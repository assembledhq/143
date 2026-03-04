"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Slider } from "@/components/ui/slider";
import { PageHeader } from "@/components/page-header";
import { AgentSettingsEditor } from "@/components/agent-settings-editor";
import type { Organization, OrgSettings, SingleResponse } from "@/lib/types";

const DEFAULT_SETTINGS: Pick<
  Required<OrgSettings>,
  "autonomy_level" | "execution_aggressiveness" | "max_concurrent_runs" | "confidence_thresholds"
> = {
  autonomy_level: "manual",
  execution_aggressiveness: 2,
  max_concurrent_runs: 3,
  confidence_thresholds: {
    auto_proceed: 0.8,
    human_review: 0.5,
  },
};

export default function AgentPage() {
  const queryClient = useQueryClient();

  const { data: settings } = useQuery<SingleResponse<Organization>>({
    queryKey: ["settings"],
    queryFn: () => api.settings.get(),
  });

  const orgSettings = (settings?.data?.settings ?? {}) as OrgSettings;

  const [autonomyLevel, setAutonomyLevel] = useState(DEFAULT_SETTINGS.autonomy_level);
  const [aggressiveness, setAggressiveness] = useState(String(DEFAULT_SETTINGS.execution_aggressiveness));
  const [maxConcurrent, setMaxConcurrent] = useState(String(DEFAULT_SETTINGS.max_concurrent_runs));
  const [autoProceed, setAutoProceed] = useState(String(DEFAULT_SETTINGS.confidence_thresholds.auto_proceed));
  const [humanReview, setHumanReview] = useState(String(DEFAULT_SETTINGS.confidence_thresholds.human_review));
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
    setAutoProceed(String(s.confidence_thresholds?.auto_proceed ?? DEFAULT_SETTINGS.confidence_thresholds.auto_proceed));
    setHumanReview(String(s.confidence_thresholds?.human_review ?? DEFAULT_SETTINGS.confidence_thresholds.human_review));
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

  function handleSave() {
    mutation.mutate({
      settings: {
        autonomy_level: autonomyLevel,
        execution_aggressiveness: parseInt(aggressiveness, 10),
        max_concurrent_runs: parseInt(maxConcurrent, 10),
        confidence_thresholds: {
          auto_proceed: parseFloat(autoProceed),
          human_review: parseFloat(humanReview),
        },
      },
    });
  }

  return (
    <div className="space-y-8">
      <PageHeader
        title="Agent"
        description="Configure how the AI agent runs and behaves."
      />

      {/* Agent Setup / Credentials */}
      <section className="space-y-3">
        <h2 className="text-[13px] font-medium text-foreground">Setup</h2>
        <Card>
          <CardContent>
            <AgentSettingsEditor
              title="Agent provider & credentials"
              description="Choose your default agent and configure provider API keys."
            />
          </CardContent>
        </Card>
      </section>

      {/* Execution Behavior */}
      <section className="space-y-3">
        <h2 className="text-[13px] font-medium text-foreground">Execution</h2>
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

      {/* Confidence Thresholds */}
      <section className="space-y-3">
        <h2 className="text-[13px] font-medium text-foreground">Confidence Thresholds</h2>
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
      </section>

      <div className="flex items-center gap-3">
        <Button onClick={handleSave} disabled={mutation.isPending}>
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
