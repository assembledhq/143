"use client";

import { useQuery } from "@tanstack/react-query";
import { CalendarClock, RefreshCw, AlertCircle, X } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { EmptyState } from "@/components/empty-state";
import { PlanView } from "@/components/pm/plan-view";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import { useAnalyze } from "@/hooks/use-analyze";
import type { PMPlan } from "@/lib/types";
import { PageContainer } from "@/components/page-container";

function formatDate(dateStr?: string) {
  if (!dateStr) return "-";
  return new Date(dateStr).toLocaleString();
}

function PlanHistory({ plans }: { plans: PMPlan[] }) {
  return (
    <div className="space-y-3">
      {plans.map((plan) => (
        <Card key={plan.id}>
          <CardHeader className="pb-2">
            <div className="flex items-center justify-between">
              <CardTitle className="text-[13px]">
                {formatDate(plan.created_at)}
              </CardTitle>
              <Badge variant="secondary" className="text-[11px]">
                {plan.status}
              </Badge>
            </div>
          </CardHeader>
          <CardContent className="text-[13px] text-muted-foreground space-y-1">
            <p>{plan.analysis || "No analysis summary available."}</p>
            <div className="flex flex-wrap gap-2">
              <Badge variant="outline" className="text-[11px]">
                {plan.tasks.length} tasks
              </Badge>
              <Badge variant="outline" className="text-[11px]">
                {plan.clusters.length} clusters
              </Badge>
              <Badge variant="outline" className="text-[11px]">
                {plan.skipped_issues.length} skipped
              </Badge>
            </div>
          </CardContent>
        </Card>
      ))}
    </div>
  );
}

export default function PlansPage() {
  const { data: latestData } = useQuery({
    queryKey: ["pm", "latest"],
    queryFn: () => api.pm.latest(),
    retry: false,
  });
  const { data: historyData } = useQuery({
    queryKey: ["pm", "plans"],
    queryFn: () => api.pm.list({ limit: 5 }),
    retry: false,
  });

  const latest = latestData?.data;
  const hasActivePlan = latest?.status === "executing";

  const { isAnalyzing, isPending, analyzeError, handleAnalyze, dismissError } = useAnalyze(hasActivePlan);

  const history = historyData?.data ?? [];

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="PM plans"
          description="See the PM agent's latest analysis and delegated tasks."
          action={
            <Button
              size="sm"
              onClick={handleAnalyze}
              disabled={isPending || isAnalyzing}
              title="Review open issues, prioritize them, and kick off agent runs"
            >
              <RefreshCw className={`mr-2 h-4 w-4 ${isPending || isAnalyzing ? "animate-spin" : ""}`} />
              {isPending ? "Starting..." : isAnalyzing ? "Analyzing..." : "Analyze issues"}
            </Button>
          }
        />

        {isAnalyzing && (
        <Card className="border-blue-200 bg-blue-50 dark:border-blue-800 dark:bg-blue-950/30">
          <CardContent className="flex items-center gap-3 py-3">
            <RefreshCw className="h-4 w-4 animate-spin text-blue-600 dark:text-blue-400" />
            <p className="text-[13px] text-blue-800 dark:text-blue-300">
              Analysis in progress — reviewing issues and generating a plan. This may take a minute...
            </p>
          </CardContent>
        </Card>
      )}

        {analyzeError && (
        <Card className="border-red-200 bg-red-50 dark:border-red-800 dark:bg-red-950/30">
          <CardContent className="flex items-center gap-3 py-3">
            <AlertCircle className="h-4 w-4 shrink-0 text-red-600 dark:text-red-400" />
            <p className="text-[13px] text-red-800 dark:text-red-300 flex-1">{analyzeError}</p>
            <Button size="sm" variant="ghost" className="shrink-0 h-6 px-2" onClick={dismissError}>
              <X className="h-3 w-3" />
            </Button>
          </CardContent>
        </Card>
      )}

        {!latest && !isAnalyzing && (
        <EmptyState
          icon={CalendarClock}
          title="No PM plans yet"
          description="Run the PM analysis to generate a prioritized plan."
        />
      )}

        {latest && (
        <Tabs defaultValue="latest" className="space-y-4">
          <TabsList>
            <TabsTrigger value="latest">Latest plan</TabsTrigger>
            <TabsTrigger value="history">History</TabsTrigger>
          </TabsList>
          <TabsContent value="latest" className="space-y-4">
            <Card>
              <CardContent className="py-4 flex flex-wrap gap-3 text-[13px] text-muted-foreground">
                <span>Created: {formatDate(latest.created_at)}</span>
                <span>Issues reviewed: {latest.issues_reviewed}</span>
                <span>Triggered by: {latest.triggered_by}</span>
              </CardContent>
            </Card>
            <PlanView plan={latest} />
          </TabsContent>
          <TabsContent value="history">
            <PlanHistory plans={history} />
          </TabsContent>
        </Tabs>
        )}
      </div>
    </PageContainer>
  );
}
