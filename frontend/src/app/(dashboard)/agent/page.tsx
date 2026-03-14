"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { PageHeader } from "@/components/page-header";
import { AgentSettingsEditor } from "@/components/agent-settings-editor";
import { PageContainer } from "@/components/page-container";
import type { Organization, OrgSettings, SingleResponse } from "@/lib/types";

const DEFAULT_SETTINGS: Pick<
  Required<OrgSettings>,
  "autonomy_level" | "execution_aggressiveness" | "max_concurrent_runs"
> = {
  autonomy_level: "auto_simple",
  execution_aggressiveness: 2,
  max_concurrent_runs: 5,
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
      },
    });
  }

  return (
    <PageContainer size="default">
      <div className="space-y-6">
      <PageHeader
        title="Coding Agent"
        description="Configure how the coding agent runs and behaves."
      />

      {/* Agent Setup / Credentials */}
      <section className="space-y-3">
        <h2 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Setup</h2>
        <Card>
          <CardContent>
            <AgentSettingsEditor
              title="Coding agent provider & credentials"
              description="Choose your default coding agent and configure provider API keys."
            />
          </CardContent>
        </Card>
      </section>

      {/* Execution Behavior */}
      <section className="space-y-3">
        <h2 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Execution</h2>
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
                      className={`relative flex cursor-pointer flex-col rounded-lg border p-3 shadow-sm transition-all duration-150 ${
                        autonomyLevel === option.value
                          ? "border-primary bg-primary/5 ring-1 ring-primary/20 dark:shadow-[var(--glow-primary-sm)]"
                          : "border-input hover:bg-muted/40 hover:border-border"
                      }`}
                    >
                      <div className="flex items-center gap-2">
                        <RadioGroupItem value={option.value} />
                        <span className="text-[13px] font-medium">{option.label}</span>
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
                      className={`relative flex cursor-pointer flex-col rounded-lg border p-3 shadow-sm transition-all duration-150 ${
                        aggressiveness === option.value
                          ? "border-primary bg-primary/5 ring-1 ring-primary/20 dark:shadow-[var(--glow-primary-sm)]"
                          : "border-input hover:bg-muted/40 hover:border-border"
                      }`}
                    >
                      <div className="flex items-center gap-2">
                        <RadioGroupItem value={option.value} />
                        <span className="text-[13px] font-medium">{option.label}</span>
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

      <div className="flex items-center justify-end gap-3">
        <Button onClick={handleSave} disabled={mutation.isPending}>
          {mutation.isPending ? "Saving..." : "Save Settings"}
        </Button>
        {saveStatus === "success" && (
          <span className="text-[13px] text-emerald-600 dark:text-emerald-400">Settings saved.</span>
        )}
        {saveStatus === "error" && (
          <span className="text-[13px] text-destructive">Failed to save settings.</span>
        )}
      </div>
      </div>
    </PageContainer>
  );
}
