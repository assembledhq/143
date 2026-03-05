"use client";

import { useState, useRef, useCallback, useEffect } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { CalendarClock, RefreshCw, AlertCircle, X } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { EmptyState } from "@/components/empty-state";
import { PlanView } from "@/components/pm/plan-view";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { PMPlan } from "@/lib/types";

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
              <CardTitle className="text-sm">
                {formatDate(plan.created_at)}
              </CardTitle>
              <Badge variant="secondary" className="text-[11px]">
                {plan.status}
              </Badge>
            </div>
          </CardHeader>
          <CardContent className="text-sm text-muted-foreground space-y-1">
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
  const queryClient = useQueryClient();
  const [isAnalyzing, setIsAnalyzing] = useState(false);
  const [analyzeError, setAnalyzeError] = useState<string | null>(null);
  const planCountBeforeAnalyze = useRef<number | null>(null);
  const analyzeTimeoutRef = useRef<NodeJS.Timeout | null>(null);

  const { data: latestData } = useQuery({
    queryKey: ["pm", "latest"],
    queryFn: () => api.pm.latest(),
    retry: false,
    refetchInterval: isAnalyzing ? 2000 : false,
  });
  const { data: historyData } = useQuery({
    queryKey: ["pm", "plans"],
    queryFn: () => api.pm.list({ limit: 5 }),
    retry: false,
    refetchInterval: isAnalyzing ? 2000 : false,
  });

  const planCount = historyData?.data?.length ?? 0;

  // Detect when a new plan appears after triggering analysis
  useEffect(() => {
    if (isAnalyzing && planCountBeforeAnalyze.current !== null && planCount > planCountBeforeAnalyze.current) {
      setIsAnalyzing(false);
      planCountBeforeAnalyze.current = null;
      if (analyzeTimeoutRef.current) {
        clearTimeout(analyzeTimeoutRef.current);
        analyzeTimeoutRef.current = null;
      }
    }
  }, [isAnalyzing, planCount]);

  // Cleanup timeout on unmount
  useEffect(() => {
    return () => {
      if (analyzeTimeoutRef.current) clearTimeout(analyzeTimeoutRef.current);
    };
  }, []);

  const analyzeMutation = useMutation({
    mutationFn: () => api.pm.analyze(),
    onSuccess: () => {
      setIsAnalyzing(true);
      queryClient.invalidateQueries({ queryKey: ["pm", "latest"] });
      queryClient.invalidateQueries({ queryKey: ["pm", "plans"] });
      analyzeTimeoutRef.current = setTimeout(() => {
        setIsAnalyzing(false);
        setAnalyzeError("Analysis may have failed or is taking longer than expected. Check your server logs for details.");
        planCountBeforeAnalyze.current = null;
      }, 90000);
    },
    onError: () => {
      setAnalyzeError("Failed to start analysis. Make sure the backend is running.");
    },
  });

  const handleAnalyze = useCallback(() => {
    setAnalyzeError(null);
    planCountBeforeAnalyze.current = planCount;
    analyzeMutation.mutate();
  }, [planCount, analyzeMutation]);

  const latest = latestData?.data;
  const history = historyData?.data ?? [];

  return (
    <div className="space-y-6">
      <PageHeader
        title="PM Plans"
        description="See the PM agent's latest analysis and delegated tasks."
        action={
          <Button
            size="sm"
            onClick={handleAnalyze}
            disabled={analyzeMutation.isPending || isAnalyzing}
          >
            <RefreshCw className={`mr-2 h-4 w-4 ${analyzeMutation.isPending || isAnalyzing ? "animate-spin" : ""}`} />
            {analyzeMutation.isPending ? "Starting..." : isAnalyzing ? "Analyzing..." : "Run Analysis"}
          </Button>
        }
      />

      {isAnalyzing && (
        <Card className="border-blue-200 bg-blue-50 dark:border-blue-800 dark:bg-blue-950/30">
          <CardContent className="flex items-center gap-3 py-3">
            <RefreshCw className="h-4 w-4 animate-spin text-blue-600 dark:text-blue-400" />
            <p className="text-sm text-blue-800 dark:text-blue-300">
              Analysis in progress — reviewing issues and generating a plan. This may take a minute...
            </p>
          </CardContent>
        </Card>
      )}

      {analyzeError && (
        <Card className="border-red-200 bg-red-50 dark:border-red-800 dark:bg-red-950/30">
          <CardContent className="flex items-center gap-3 py-3">
            <AlertCircle className="h-4 w-4 shrink-0 text-red-600 dark:text-red-400" />
            <p className="text-sm text-red-800 dark:text-red-300 flex-1">{analyzeError}</p>
            <Button size="sm" variant="ghost" className="shrink-0 h-6 px-2" onClick={() => setAnalyzeError(null)}>
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
            <TabsTrigger value="latest">Latest Plan</TabsTrigger>
            <TabsTrigger value="history">History</TabsTrigger>
          </TabsList>
          <TabsContent value="latest" className="space-y-4">
            <Card>
              <CardContent className="py-4 flex flex-wrap gap-3 text-sm text-muted-foreground">
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
  );
}
