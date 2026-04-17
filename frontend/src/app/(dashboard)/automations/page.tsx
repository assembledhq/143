"use client";

import { useMemo } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { RefreshCw, Plus, Pause, Play, MoreHorizontal, Trash2 } from "lucide-react";
import Link from "next/link";
import { api } from "@/lib/api";
import { formatTimeAgo } from "@/lib/utils";
import type { Automation } from "@/lib/types";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

function formatSchedule(a: Automation): string {
  if (a.schedule_type === "cron" && a.cron_expression) {
    return `cron: ${a.cron_expression}`;
  }
  const val = a.interval_value ?? 1;
  const unit = a.interval_unit ?? "days";
  return `every ${val} ${val === 1 ? unit.replace(/s$/, "") : unit}`;
}

function AutomationCard({ automation }: { automation: Automation }) {
  const queryClient = useQueryClient();

  const pauseMutation = useMutation({
    mutationFn: () => api.automations.pause(automation.id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["automations"] }),
  });

  const resumeMutation = useMutation({
    mutationFn: () => api.automations.resume(automation.id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["automations"] }),
  });

  const deleteMutation = useMutation({
    mutationFn: () => api.automations.del(automation.id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["automations"] }),
  });

  return (
    <div className="flex items-start justify-between gap-4 rounded-lg border border-border bg-background p-4 transition-colors hover:bg-muted/30">
      <Link href={`/automations/${automation.id}`} className="flex-1 min-w-0">
        <div className="flex items-center gap-2.5">
          {automation.enabled ? (
            <RefreshCw className="h-4 w-4 text-blue-500 shrink-0" />
          ) : (
            <Pause className="h-4 w-4 text-muted-foreground shrink-0" />
          )}
          <h3 className="text-sm font-medium text-foreground truncate">
            {automation.name}
          </h3>
          <span className="text-xs text-muted-foreground shrink-0">
            {formatSchedule(automation)}
          </span>
        </div>
        <div className="mt-1 ml-6.5 flex items-center gap-3 text-xs text-muted-foreground">
          {automation.last_run_at && (
            <span>Last run: {formatTimeAgo(automation.last_run_at)}</span>
          )}
          {automation.next_run_at && automation.enabled && (
            <span>Next: {new Date(automation.next_run_at).toLocaleString()}</span>
          )}
          {!automation.enabled && automation.paused_at && (
            <span>Paused {formatTimeAgo(automation.paused_at)}</span>
          )}
        </div>
      </Link>

      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon" className="h-8 w-8">
            <MoreHorizontal className="h-4 w-4 text-muted-foreground" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {automation.enabled ? (
            <DropdownMenuItem
              onClick={() => pauseMutation.mutate()}
              disabled={pauseMutation.isPending}
            >
              <Pause className="h-3.5 w-3.5 mr-2" />
              Pause
            </DropdownMenuItem>
          ) : (
            <DropdownMenuItem
              onClick={() => resumeMutation.mutate()}
              disabled={resumeMutation.isPending}
            >
              <Play className="h-3.5 w-3.5 mr-2" />
              Resume
            </DropdownMenuItem>
          )}
          <DropdownMenuItem
            onClick={() => deleteMutation.mutate()}
            disabled={deleteMutation.isPending}
            className="text-destructive focus:text-destructive"
          >
            <Trash2 className="h-3.5 w-3.5 mr-2" />
            Delete
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}

export default function AutomationsPage() {
  const { data, isLoading } = useQuery({
    queryKey: ["automations"],
    queryFn: () => api.automations.list(),
    refetchInterval: 10000,
  });

  const automations = data?.data ?? [];
  const enabled = useMemo(() => automations.filter((a) => a.enabled), [automations]);
  const paused = useMemo(() => automations.filter((a) => !a.enabled), [automations]);

  return (
    <div className="max-w-4xl mx-auto px-6 py-8">
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-lg font-semibold text-foreground">Automations</h1>
          <p className="text-sm text-muted-foreground mt-0.5">
            Recurring agents that run on a schedule for your team.
          </p>
        </div>
        <Button asChild size="sm">
          <Link href="/automations/new">
            <Plus className="h-4 w-4 mr-1.5" />
            New
          </Link>
        </Button>
      </div>

      {isLoading && (
        <div className="text-center py-12 text-sm text-muted-foreground">
          Loading automations...
        </div>
      )}

      {!isLoading && automations.length === 0 && (
        <div className="text-center py-16 space-y-3">
          <RefreshCw className="h-8 w-8 mx-auto text-muted-foreground/40" />
          <p className="text-sm text-muted-foreground">No automations yet</p>
          <Button asChild variant="outline" size="sm">
            <Link href="/automations/new">
              <Plus className="h-4 w-4 mr-1.5" />
              Create your first automation
            </Link>
          </Button>
        </div>
      )}

      {enabled.length > 0 && (
        <div className="mb-6">
          <h2 className="text-xs font-medium text-muted-foreground uppercase tracking-wider mb-3">
            Enabled ({enabled.length})
          </h2>
          <div className="space-y-2">
            {enabled.map((a) => (
              <AutomationCard key={a.id} automation={a} />
            ))}
          </div>
        </div>
      )}

      {paused.length > 0 && (
        <div>
          <h2 className="text-xs font-medium text-muted-foreground uppercase tracking-wider mb-3">
            Paused ({paused.length})
          </h2>
          <div className="space-y-2">
            {paused.map((a) => (
              <AutomationCard key={a.id} automation={a} />
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
