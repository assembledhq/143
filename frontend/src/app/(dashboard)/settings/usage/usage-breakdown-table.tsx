"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { Card, CardContent } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { formatMinutes, formatNumber, formatTokenCount, formatCost } from "./usage-helpers";
import { cn } from "@/lib/utils";
import { Info } from "lucide-react";

type Dimension = "user" | "capacity";

interface UsageBreakdownTableProps {
  start: string;
  end: string;
  dimension: Dimension;
  onDimensionChange: (dimension: Dimension) => void;
  onRowClick?: (key: string) => void;
  selectedKey?: string | null;
}

export function UsageBreakdownTable({
  start,
  end,
  dimension,
  onDimensionChange,
  onRowClick,
  selectedKey,
}: UsageBreakdownTableProps) {
  const { data, isLoading } = useQuery({
    queryKey: queryKeys.usage.breakdown({ start, end, dimension }),
    queryFn: () =>
      api.usage.getBreakdown({ start, end, dimension, sort: "minutes_desc" }),
  });

  const rows = data?.data ?? [];

  return (
    <Card>
      <CardContent className="p-4">
        <div className="flex items-center justify-between mb-4">
          <h3 className="text-sm font-medium">Breakdown</h3>
          <Select
            value={dimension}
            onValueChange={(v) => onDimensionChange(v as Dimension)}
          >
            <SelectTrigger className="h-8 w-36 text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="user" className="text-xs">By User</SelectItem>
              <SelectItem value="capacity" className="text-xs">By Capacity</SelectItem>
            </SelectContent>
          </Select>
        </div>

        {isLoading ? (
          <div className="space-y-2">
            {Array.from({ length: 4 }).map((_, i) => (
              <div key={i} className="h-10 bg-muted animate-pulse rounded" />
            ))}
          </div>
        ) : rows.length === 0 ? (
          <div className="py-8 text-center text-sm text-muted-foreground">
            No breakdown data available
          </div>
        ) : (
          <TooltipProvider>
          <div className="overflow-x-auto -mx-4">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="pl-4 text-xs">
                    {dimension === "user" ? "User" : "Capacity Tier"}
                  </TableHead>
                  <TableHead className="text-xs text-right">Minutes</TableHead>
                  <TableHead className="text-xs text-right">Sessions</TableHead>
                  <TableHead className="text-xs text-right">Tokens</TableHead>
                  <TableHead className="text-xs text-right">
                    <Tooltip>
                      <TooltipTrigger className="inline-flex items-center gap-1">
                        Est. Cost
                        <Info className="h-3 w-3 text-muted-foreground" />
                      </TooltipTrigger>
                      <TooltipContent className="max-w-[200px]">
                        Estimated cost at standard provider rates. Your actual billing may differ based on your plan.
                      </TooltipContent>
                    </Tooltip>
                  </TableHead>
                  <TableHead className="text-xs text-right pr-4">%</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((row) => (
                  <TableRow
                    key={row.key}
                    className={cn(
                      onRowClick && "cursor-pointer hover:bg-muted/50",
                      selectedKey === row.key && "bg-muted/50"
                    )}
                    onClick={() => onRowClick?.(row.key)}
                  >
                    <TableCell className="pl-4 text-[13px] font-medium">
                      {row.label}
                    </TableCell>
                    <TableCell className="text-[13px] text-right tabular-nums">
                      {formatMinutes(row.total_container_minutes)}
                    </TableCell>
                    <TableCell className="text-[13px] text-right tabular-nums">
                      {formatNumber(row.total_sessions)}
                    </TableCell>
                    <TableCell className="text-[13px] text-right tabular-nums">
                      {formatTokenCount(row.total_input_tokens + row.total_output_tokens)}
                    </TableCell>
                    <TableCell className="text-[13px] text-right tabular-nums">
                      {formatCost(row.total_llm_cost_usd)}
                    </TableCell>
                    <TableCell className="text-[13px] text-right tabular-nums pr-4">
                      <div className="flex items-center justify-end gap-2">
                        <div className="w-16 h-1.5 bg-muted rounded-full overflow-hidden">
                          <div
                            className="h-full bg-primary rounded-full"
                            style={{ width: `${Math.min(row.percentage, 100)}%` }}
                          />
                        </div>
                        <span className="w-10 text-right">{row.percentage.toFixed(1)}%</span>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
          </TooltipProvider>
        )}
      </CardContent>
    </Card>
  );
}
