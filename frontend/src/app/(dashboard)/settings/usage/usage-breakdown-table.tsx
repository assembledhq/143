"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { Card, CardContent } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatCost, formatMinutes, formatNumber, formatTokenCount } from "./usage-helpers";
import { cn } from "@/lib/utils";

export type UsageBreakdownDimension = "capacity" | "agent" | "model" | "reasoning";

interface UsageBreakdownTableProps {
  start: string;
  end: string;
  dimension: UsageBreakdownDimension;
  filters?: {
    agent?: string | null;
    model?: string | null;
    reasoning?: string | null;
  };
  onRowClick?: (row: { dimension: UsageBreakdownDimension; key: string }) => void;
  selectedKey?: string | null;
}

function leadingLabel(dimension: UsageBreakdownDimension): string {
  switch (dimension) {
    case "capacity":
      return "Capacity";
    case "agent":
      return "Agent";
    case "model":
      return "Model";
    case "reasoning":
      return "Reasoning";
  }
}

export function UsageBreakdownTable({ start, end, dimension, filters, onRowClick, selectedKey }: UsageBreakdownTableProps) {
  const { data, isLoading, isError } = useQuery({
    queryKey: queryKeys.usage.breakdown({
      start,
      end,
      dimension,
      sort: dimension === "model" ? "tokens_desc" : "minutes_desc",
      ...(filters?.agent ? { agent: filters.agent } : {}),
      ...(filters?.model ? { model: filters.model } : {}),
      ...(filters?.reasoning ? { reasoning: filters.reasoning } : {}),
    }),
    queryFn: () =>
      api.usage.getBreakdown({
        start,
        end,
        dimension,
        sort: dimension === "model" ? "tokens_desc" : "minutes_desc",
        ...(filters?.agent ? { agent: filters.agent } : {}),
        ...(filters?.model ? { model: filters.model } : {}),
        ...(filters?.reasoning ? { reasoning: filters.reasoning } : {}),
      }),
  });

  const rows = data?.data ?? [];

  return (
    <Card>
      <CardContent className="p-4">
        <div className="mb-4 flex items-center justify-between">
          <h3 className="text-sm font-medium">Breakdown</h3>
          <p className="text-xs text-muted-foreground">By {leadingLabel(dimension)}</p>
        </div>

        {isLoading ? (
          <div className="space-y-2">
            {Array.from({ length: 4 }).map((_, i) => (
              <div key={i} className="h-10 animate-pulse rounded bg-muted" />
            ))}
          </div>
        ) : isError ? (
          <div className="py-8 text-center text-sm text-destructive">
            Failed to load breakdown data. Please try again later.
          </div>
        ) : rows.length === 0 ? (
          <div className="py-8 text-center text-sm text-muted-foreground">
            No breakdown data available
          </div>
        ) : (
          <div className="-mx-4 overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="pl-4 text-xs">{leadingLabel(dimension)}</TableHead>
                  <TableHead className="text-right text-xs">Sessions</TableHead>
                  <TableHead className="text-right text-xs">Container Minutes</TableHead>
                  <TableHead className="text-right text-xs">Total Tokens</TableHead>
                  <TableHead className="text-right text-xs">Est. Cost</TableHead>
                  <TableHead className="pr-4 text-right text-xs">Share of Tokens</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((row) => (
                  <TableRow
                    key={row.key}
                    className={cn(onRowClick && "cursor-pointer hover:bg-muted/50", selectedKey === row.key && "bg-muted/50")}
                    onClick={() => onRowClick?.({ dimension, key: row.key })}
                  >
                    <TableCell className="pl-4 text-[13px] font-medium">{row.label}</TableCell>
                    <TableCell className="text-right text-[13px] tabular-nums">{formatNumber(row.total_sessions)}</TableCell>
                    <TableCell className="text-right text-[13px] tabular-nums">{formatMinutes(row.total_container_minutes)}</TableCell>
                    <TableCell className="text-right text-[13px] tabular-nums">{formatTokenCount(row.total_tokens)}</TableCell>
                    <TableCell className="text-right text-[13px] tabular-nums">{formatCost(row.total_llm_cost_usd)}</TableCell>
                    <TableCell className="pr-4 text-right text-[13px] tabular-nums">
                      {(row.share_of_tokens ?? row.percentage).toFixed(1)}%
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
