"use client";

import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { Card, CardContent } from "@/components/ui/card";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { Activity, Layers, Gauge, Cpu, Info } from "lucide-react";
import { formatNumber, formatTokenCount, formatEstimatedCost } from "./usage-helpers";
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
  labelTooltip?: ReactNode;
}

function KPICard({ icon: Icon, label, value, subtitle, loading, labelTooltip }: KPICardProps) {
  return (
    <Card>
      <CardContent className="p-4">
        <div className="flex items-center gap-2 text-muted-foreground mb-2">
          <Icon className="h-4 w-4" />
          <span className="text-xs font-medium">{label}</span>
          {labelTooltip && (
            <TooltipProvider>
              <Tooltip>
                <TooltipTrigger asChild>
                  <Info className="h-3 w-3 cursor-help" aria-label="More info" />
                </TooltipTrigger>
                <TooltipContent>{labelTooltip}</TooltipContent>
              </Tooltip>
            </TooltipProvider>
          )}
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
  const { data, isLoading, isError } = useQuery({
    queryKey: queryKeys.usage.summary({ start, end }),
    queryFn: () => api.usage.getSummary({ start, end }),
  });

  const summary = data?.data;

  const tokenTotals = {
    input: summary?.total_input_tokens ?? 0,
    output: summary?.total_output_tokens ?? 0,
    cost: summary?.total_llm_cost_usd ?? 0,
  };
  const totalTokens = tokenTotals.input + tokenTotals.output;
  const tokenCostSubtitle =
    tokenTotals.cost > 0
      ? `Est. API cost: ${formatEstimatedCost(tokenTotals.cost, totalTokens)}`
      : totalTokens > 0
        ? "Cost unavailable"
        : undefined;

  if (isError) {
    return (
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Card className="col-span-full">
          <CardContent className="p-4 text-sm text-destructive">
            Failed to load usage summary. Please try again later.
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
      <KPICard
        icon={Activity}
        label="Container Hours"
        value={isLoading ? "" : `${((summary?.total_container_minutes ?? 0) / 60).toFixed(1)}h`}
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
        value={isLoading ? "" : formatTokenCount(totalTokens)}
        subtitle={isLoading ? undefined : tokenCostSubtitle}
        loading={isLoading}
        labelTooltip={
          isLoading ? null : (
            <div className="space-y-0.5">
              <div>Input: {formatTokenCount(tokenTotals.input)}</div>
              <div>Output: {formatTokenCount(tokenTotals.output)}</div>
            </div>
          )
        }
      />
    </div>
  );
}
