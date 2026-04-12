"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { Card, CardContent } from "@/components/ui/card";
import { Activity, Layers, Gauge, Cpu } from "lucide-react";
import { formatMinutes, formatNumber, formatTokenCount, formatCost } from "./usage-helpers";
import type { LucideIcon } from "lucide-react";

interface UsageSummaryCardsProps {
  start: string;
  end: string;
}

interface KPICardProps {
  icon: LucideIcon;
  label: string;
  value: string;
  subtitle?: string;
  loading?: boolean;
}

function KPICard({ icon: Icon, label, value, subtitle, loading }: KPICardProps) {
  return (
    <Card>
      <CardContent className="p-4">
        <div className="flex items-center gap-2 text-muted-foreground mb-2">
          <Icon className="h-4 w-4" />
          <span className="text-xs font-medium">{label}</span>
        </div>
        {loading ? (
          <div className="space-y-2">
            <div className="h-7 w-24 bg-muted animate-pulse rounded" />
            <div className="h-4 w-16 bg-muted animate-pulse rounded" />
          </div>
        ) : (
          <>
            <p className="text-2xl font-semibold tracking-tight">{value}</p>
            {subtitle && (
              <p className="text-xs text-muted-foreground mt-1">{subtitle}</p>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}

export function UsageSummaryCards({ start, end }: UsageSummaryCardsProps) {
  const { data, isLoading } = useQuery({
    queryKey: queryKeys.usage.summary({ start, end }),
    queryFn: () => api.usage.getSummary({ start, end }),
  });

  const summary = data?.data;

  const tokenTotals = {
    input: summary?.total_input_tokens ?? 0,
    output: summary?.total_output_tokens ?? 0,
    cost: summary?.total_llm_cost_usd ?? 0,
  };

  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
      <KPICard
        icon={Activity}
        label="Container Minutes"
        value={isLoading ? "" : formatMinutes(summary?.total_container_minutes ?? 0)}
        subtitle={`${formatNumber(summary?.total_container_minutes ?? 0)} total min`}
        loading={isLoading}
      />
      <KPICard
        icon={Layers}
        label="Total Sessions"
        value={isLoading ? "" : formatNumber(summary?.total_sessions ?? 0)}
        loading={isLoading}
      />
      <KPICard
        icon={Gauge}
        label="Peak Concurrent"
        value={isLoading ? "" : String(summary?.peak_concurrent ?? 0)}
        loading={isLoading}
      />
      <KPICard
        icon={Cpu}
        label="LLM Tokens"
        value={isLoading ? "" : `${formatTokenCount(tokenTotals.input)} / ${formatTokenCount(tokenTotals.output)}`}
        subtitle={`~${formatCost(tokenTotals.cost)} est. cost`}
        loading={isLoading}
      />
    </div>
  );
}
