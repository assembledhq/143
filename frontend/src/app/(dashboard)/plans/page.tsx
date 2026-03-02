"use client";

import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { CalendarClock, RefreshCw } from "lucide-react";
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

  const analyzeMutation = useMutation({
    mutationFn: () => api.pm.analyze(),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["pm", "latest"] });
      queryClient.invalidateQueries({ queryKey: ["pm", "plans"] });
    },
  });

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
            onClick={() => analyzeMutation.mutate()}
            disabled={analyzeMutation.isPending}
          >
            <RefreshCw className={`mr-2 h-4 w-4 ${analyzeMutation.isPending ? "animate-spin" : ""}`} />
            {analyzeMutation.isPending ? "Running" : "Run Analysis"}
          </Button>
        }
      />

      {!latest && (
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
