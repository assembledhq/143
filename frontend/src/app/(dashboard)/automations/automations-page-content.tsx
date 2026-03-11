"use client";

import { useEffect, useMemo, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { Bot, Play, ShieldCheck, TestTube2, Wrench } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { EmptyState } from "@/components/empty-state";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { api } from "@/lib/api";

type AutomationRunMode = "manual_session" | "pm_analysis";
type AutomationRunStatus = "idle" | "running" | "success" | "failed";

interface Automation {
  id: string;
  name: string;
  instructions: string;
  run_mode: AutomationRunMode;
  created_at: string;
  last_run_at?: string;
  last_run_status?: Exclude<AutomationRunStatus, "idle" | "running">;
}

interface AutomationTemplate {
  id: string;
  name: string;
  instructions: string;
  run_mode: AutomationRunMode;
  description: string;
  icon: React.ComponentType<{ className?: string }>;
}

const STORAGE_KEY = "143:automations:v1";

const SUGGESTED_TEMPLATES: AutomationTemplate[] = [
  {
    id: "flaky-tests",
    name: "Find flaky tests",
    description: "Detect nondeterministic tests and make them stable.",
    instructions:
      "Identify flaky tests from recent failures, reproduce nondeterminism, and propose or implement deterministic test fixes with minimal behavior change.",
    run_mode: "manual_session",
    icon: TestTube2,
  },
  {
    id: "security-sweep",
    name: "Find security fixes",
    description: "Scan for exploit paths and prioritize high-risk remediations.",
    instructions:
      "Review recent changes and open issues to identify concrete security vulnerabilities, then propose high-confidence remediation steps and tests.",
    run_mode: "manual_session",
    icon: ShieldCheck,
  },
  {
    id: "codebase-maintenance",
    name: "Improve codebase quality",
    description: "Pay down technical debt with targeted, low-risk improvements.",
    instructions:
      "Identify high-leverage maintenance opportunities that reduce operational risk, improve reliability, or reduce long-term complexity without broad rewrites.",
    run_mode: "manual_session",
    icon: Wrench,
  },
  {
    id: "linear-triage",
    name: "Triage Linear backlog",
    description: "Run PM analysis to reprioritize and cluster incoming work.",
    instructions:
      "Analyze current issue context, prioritize work by impact and urgency, and cluster related items into actionable follow-up tasks.",
    run_mode: "pm_analysis",
    icon: Bot,
  },
];

function slugify(value: string): string {
  return value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 80);
}

function readAutomationsFromStorage(): Automation[] {
  if (typeof window === "undefined") {
    return [];
  }
  const raw = window.localStorage.getItem(STORAGE_KEY);
  if (!raw) {
    return [];
  }
  try {
    const parsed = JSON.parse(raw) as Automation[];
    if (!Array.isArray(parsed)) {
      return [];
    }
    return parsed;
  } catch {
    return [];
  }
}

function writeAutomationsToStorage(automations: Automation[]) {
  window.localStorage.setItem(STORAGE_KEY, JSON.stringify(automations));
}

function formatShortTime(dateString?: string): string {
  if (!dateString) return "-";
  return new Date(dateString).toLocaleString();
}

export function AutomationsPageContent() {
  const [automations, setAutomations] = useState<Automation[]>([]);
  const [name, setName] = useState("");
  const [instructions, setInstructions] = useState("");
  const [runMode, setRunMode] = useState<AutomationRunMode>("manual_session");
  const [createError, setCreateError] = useState<string | null>(null);
  const [statusMessage, setStatusMessage] = useState<string | null>(null);
  const [runningAutomationID, setRunningAutomationID] = useState<string | null>(null);

  useEffect(() => {
    setAutomations(readAutomationsFromStorage());
  }, []);

  const stats = useMemo(() => {
    const total = automations.length;
    const success = automations.filter((a) => a.last_run_status === "success").length;
    const failed = automations.filter((a) => a.last_run_status === "failed").length;
    return { total, success, failed };
  }, [automations]);

  const runMutation = useMutation({
    mutationFn: async (automation: Automation) => {
      if (automation.run_mode === "pm_analysis") {
        await api.pm.analyze();
        return "PM analysis job queued";
      }
      await api.sessions.createManual({
        message: automation.instructions,
        agent_type: "codex",
        autonomy_level: "semi",
        token_mode: "high",
      });
      return "Last run succeeded";
    },
  });

  function persist(next: Automation[]) {
    setAutomations(next);
    writeAutomationsToStorage(next);
  }

  function addAutomation(input: Omit<Automation, "id" | "created_at">) {
    const trimmedName = input.name.trim();
    const trimmedInstructions = input.instructions.trim();
    if (!trimmedName || !trimmedInstructions) {
      setCreateError("Name and instructions are required.");
      return;
    }

    const nowISO = new Date().toISOString();
    const automation: Automation = {
      id: `${slugify(trimmedName)}-${Date.now()}`,
      name: trimmedName,
      instructions: trimmedInstructions,
      run_mode: input.run_mode,
      created_at: nowISO,
    };

    const next = [automation, ...automations];
    persist(next);
    setName("");
    setInstructions("");
    setRunMode("manual_session");
    setCreateError(null);
    setStatusMessage("Automation created");
  }

  async function runAutomation(automation: Automation) {
    setStatusMessage(null);
    setRunningAutomationID(automation.id);
    try {
      const message = await runMutation.mutateAsync(automation);
      const nowISO = new Date().toISOString();
      const next = automations.map((item) =>
        item.id === automation.id
          ? {
              ...item,
              last_run_at: nowISO,
              last_run_status: "success" as const,
            }
          : item
      );
      persist(next);
      setStatusMessage(message);
    } catch {
      const nowISO = new Date().toISOString();
      const next = automations.map((item) =>
        item.id === automation.id
          ? {
              ...item,
              last_run_at: nowISO,
              last_run_status: "failed" as const,
            }
          : item
      );
      persist(next);
      setStatusMessage("Automation run failed");
    } finally {
      setRunningAutomationID(null);
    }
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title="Automations"
        description="Create reusable automations and run them on command."
      />

      <div className="grid gap-3 md:grid-cols-3">
        <Card>
          <CardContent className="py-4">
            <p className="text-xs text-muted-foreground">Total Automations</p>
            <p className="mt-1 text-xl font-semibold text-foreground">{stats.total}</p>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="py-4">
            <p className="text-xs text-muted-foreground">Successful Runs</p>
            <p className="mt-1 text-xl font-semibold text-foreground">{stats.success}</p>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="py-4">
            <p className="text-xs text-muted-foreground">Failed Runs</p>
            <p className="mt-1 text-xl font-semibold text-foreground">{stats.failed}</p>
          </CardContent>
        </Card>
      </div>

      {statusMessage && (
        <Card>
          <CardContent className="py-3">
            <p className="text-sm text-foreground">{statusMessage}</p>
          </CardContent>
        </Card>
      )}

      <Card>
        <CardHeader className="pb-4">
          <CardTitle className="text-sm">Create automation</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-4 md:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="automation-name">Automation Name</Label>
              <Input
                id="automation-name"
                aria-label="Automation Name"
                value={name}
                onChange={(event) => setName(event.target.value)}
                placeholder="Flaky test sweeper"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="automation-run-mode">Execution Mode</Label>
              <Select value={runMode} onValueChange={(value) => setRunMode(value as AutomationRunMode)}>
                <SelectTrigger id="automation-run-mode" aria-label="Execution Mode">
                  <SelectValue placeholder="Select mode" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="manual_session">Manual Session</SelectItem>
                  <SelectItem value="pm_analysis">PM Analysis</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <div className="space-y-2">
            <Label htmlFor="automation-instructions">Automation Instructions</Label>
            <Textarea
              id="automation-instructions"
              aria-label="Automation Instructions"
              rows={4}
              value={instructions}
              onChange={(event) => setInstructions(event.target.value)}
              placeholder="Describe what the automation should do when run on command."
            />
          </div>
          {createError && <p className="text-xs text-red-600">{createError}</p>}
          <div className="flex justify-end">
            <Button
              onClick={() =>
                addAutomation({
                  name,
                  instructions,
                  run_mode: runMode,
                })
              }
            >
              Create Automation
            </Button>
          </div>
        </CardContent>
      </Card>

      <section className="space-y-3">
        <h2 className="text-[13px] font-medium text-foreground">Saved automations</h2>
        {automations.length === 0 ? (
          <EmptyState
            icon={Play}
            title="No automations yet"
            description="Create an automation or use a template to start running workflows on command."
          />
        ) : (
          <Card>
            <CardContent className="p-0">
              {automations.map((automation, index) => {
                const rowSlug = slugify(automation.name);
                return (
                  <div
                    key={automation.id}
                    data-testid={`automation-row-${rowSlug}`}
                    className={`flex flex-col gap-3 px-4 py-4 md:flex-row md:items-start md:justify-between ${index === automations.length - 1 ? "" : "border-b border-border"}`}
                  >
                    <div className="space-y-1">
                      <p className="text-sm font-medium text-foreground">{automation.name}</p>
                      <p className="text-xs text-muted-foreground">{automation.instructions}</p>
                      <div className="flex items-center gap-2 text-xs text-muted-foreground">
                        <Badge variant="outline" className="text-[11px]">
                          {automation.run_mode === "pm_analysis" ? "PM Analysis" : "Manual Session"}
                        </Badge>
                        <span>Created: {formatShortTime(automation.created_at)}</span>
                        {automation.last_run_at && (
                          <span>Last run: {formatShortTime(automation.last_run_at)}</span>
                        )}
                      </div>
                    </div>
                    <div className="flex items-center gap-2">
                      {automation.last_run_status === "failed" && (
                        <Badge variant="secondary">Needs attention</Badge>
                      )}
                      <Button
                        size="sm"
                        onClick={() => runAutomation(automation)}
                        disabled={runningAutomationID === automation.id}
                      >
                        {runningAutomationID === automation.id ? "Running..." : "Run Now"}
                      </Button>
                    </div>
                  </div>
                );
              })}
            </CardContent>
          </Card>
        )}
      </section>

      <section className="space-y-3">
        <h2 className="text-[13px] font-medium text-foreground">Suggested templates</h2>
        <div className="grid gap-3 md:grid-cols-2">
          {SUGGESTED_TEMPLATES.map((template) => (
            <Card key={template.id} data-testid={`template-${template.id}`}>
              <CardContent className="py-4">
                <div className="flex items-start justify-between gap-3">
                  <div className="space-y-1.5">
                    <div className="flex items-center gap-2">
                      <template.icon className="h-4 w-4 text-muted-foreground" />
                      <p className="text-sm font-medium text-foreground">{template.name}</p>
                    </div>
                    <p className="text-xs text-muted-foreground">{template.description}</p>
                    <Badge variant="outline" className="text-[11px]">
                      {template.run_mode === "pm_analysis" ? "PM Analysis" : "Manual Session"}
                    </Badge>
                  </div>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() =>
                      addAutomation({
                        name: template.name,
                        instructions: template.instructions,
                        run_mode: template.run_mode,
                      })
                    }
                  >
                    Use Template
                  </Button>
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      </section>
    </div>
  );
}
