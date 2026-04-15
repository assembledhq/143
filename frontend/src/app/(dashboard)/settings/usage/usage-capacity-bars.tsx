"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { Card, CardContent } from "@/components/ui/card";

interface UsageCapacityBarsProps {
  start: string;
  end: string;
}

const tierColors = [
  "bg-primary",
  "bg-blue-500",
  "bg-emerald-500",
  "bg-amber-500",
  "bg-purple-500",
];

function formatTierLabel(tier: string): string {
  // Convert "2cpu_4096mb" -> "2 CPU / 4 GB"
  const match = tier.match(/^(\d+)cpu_(\d+)mb$/);
  if (!match) return tier;
  const cpu = match[1];
  const memGB = Math.round(parseInt(match[2], 10) / 1024);
  return `${cpu} CPU / ${memGB} GB`;
}

export function UsageCapacityBars({ start, end }: UsageCapacityBarsProps) {
  const { data, isLoading, isError } = useQuery({
    queryKey: queryKeys.usage.breakdown({ start, end, dimension: "capacity", sort: "minutes_desc" }),
    queryFn: () =>
      api.usage.getBreakdown({ start, end, dimension: "capacity", sort: "minutes_desc" }),
  });

  const rows = data?.data ?? [];

  if (isLoading) {
    return (
      <Card>
        <CardContent className="p-4">
          <h3 className="text-sm font-medium mb-3">Capacity Breakdown</h3>
          <div className="space-y-3">
            {Array.from({ length: 3 }).map((_, i) => (
              <div key={i} className="h-6 bg-muted animate-pulse rounded" />
            ))}
          </div>
        </CardContent>
      </Card>
    );
  }

  if (isError) {
    return (
      <Card>
        <CardContent className="p-4">
          <h3 className="text-sm font-medium mb-3">Capacity Breakdown</h3>
          <p className="text-sm text-destructive">Failed to load capacity data.</p>
        </CardContent>
      </Card>
    );
  }

  if (rows.length === 0) {
    return (
      <Card>
        <CardContent className="p-4">
          <h3 className="text-sm font-medium mb-3">Capacity Breakdown</h3>
          <p className="text-sm text-muted-foreground">No capacity data available</p>
        </CardContent>
      </Card>
    );
  }

  return (
    <Card>
      <CardContent className="p-4">
        <h3 className="text-sm font-medium mb-4">Capacity Breakdown</h3>
        <div className="space-y-3">
          {rows.map((row, i) => (
            <div key={row.key} className="space-y-1">
              <div className="flex items-center justify-between text-[13px]">
                <span className="font-medium">{formatTierLabel(row.label)}</span>
                <span className="text-muted-foreground tabular-nums">{row.percentage.toFixed(1)}%</span>
              </div>
              <div className="h-2 bg-muted rounded-full overflow-hidden">
                <div
                  className={`h-full rounded-full transition-all ${tierColors[i % tierColors.length]}`}
                  style={{ width: `${Math.min(row.percentage, 100)}%` }}
                />
              </div>
            </div>
          ))}
        </div>
      </CardContent>
    </Card>
  );
}
