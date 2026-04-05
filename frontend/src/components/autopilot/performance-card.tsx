"use client";

import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { PMDecisionSummary } from "@/lib/types";

interface PerformanceCardProps {
  summary: PMDecisionSummary | undefined;
}

export function PerformanceCard({ summary }: PerformanceCardProps) {
  if (!summary || summary.total_delegated === 0) {
    return (
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">Performance</CardTitle>
        </CardHeader>
        <CardContent className="text-sm text-muted-foreground">
          No delegated tasks yet. Performance data will appear after the PM agent runs.
        </CardContent>
      </Card>
    );
  }

  const rate = Math.round((summary.succeeded / summary.total_delegated) * 100);

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-sm">Performance</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="flex items-baseline gap-2">
          <span className="text-2xl font-bold tabular-nums">{rate}%</span>
          <span className="text-sm text-muted-foreground">success rate</span>
        </div>
        <div className="flex flex-wrap gap-2 text-xs">
          <Badge variant="secondary" className="text-[11px]">
            {summary.succeeded} succeeded
          </Badge>
          <Badge variant="secondary" className="text-[11px]">
            {summary.failed} failed
          </Badge>
          {summary.still_open > 0 && (
            <Badge variant="outline" className="text-[11px]">
              {summary.still_open} still open
            </Badge>
          )}
        </div>
        <p className="text-xs text-muted-foreground">
          {summary.succeeded}/{summary.total_delegated} delegated tasks succeeded
        </p>
      </CardContent>
    </Card>
  );
}
