"use client";

import { useMemo, useRef } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { RefreshCw, Plus, Pause, Play, MoreHorizontal, Trash2 } from "lucide-react";
import Link from "next/link";
import { api } from "@/lib/api";
import { cn, formatTimeAgo } from "@/lib/utils";
import { hoverSurface, raisedSurface } from "@/lib/surfaces";
import type { Automation } from "@/lib/types";
import { formatRunAtWithTimezone } from "./schedule-time";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { useAuth } from "@/hooks/use-auth";

function formatSchedule(a: Automation): string {
  const tz = a.timezone || "UTC";
  if (a.schedule_type === "cron" && a.cron_expression) {
    return `cron: ${a.cron_expression} (${tz})`;
  }
  const val = a.interval_value ?? 1;
  const unit = a.interval_unit ?? "days";
  const intervalText = `every ${val} ${val === 1 ? unit.replace(/s$/, "") : unit}`;
  if (!a.interval_run_at) {
    return intervalText;
  }
  return `${intervalText} at ${formatRunAtWithTimezone(a.interval_run_at, tz)}`;
}

function AutomationCard({ automation, canManage }: { automation: Automation; canManage: boolean }) {
  const queryClient = useQueryClient();

  const pauseMutation = useMutation({
    mutationFn: () => api.automations.pause(automation.id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["automations"] }),
  });

  const resumeMutation = useMutation({
    mutationFn: () => api.automations.resume(automation.id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["automations"] }),
  });

  // deleteInFlight closes the same render-tick race that runNowInFlight does on
  // the detail page: `disabled={isPending}` is only applied on the next render,
  // so a rapid double-click can fire mutate() twice before React catches up. A
  // synchronous ref rejects the second click immediately.
  const deleteInFlight = useRef(false);
  const deleteMutation = useMutation({
    mutationFn: () => api.automations.del(automation.id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["automations"] }),
    onSettled: () => {
      deleteInFlight.current = false;
    },
  });
  const handleDelete = () => {
    if (deleteInFlight.current || deleteMutation.isPending) return;
    deleteInFlight.current = true;
    deleteMutation.mutate();
  };

  // Any of the three mutations failing is worth surfacing inline; silent
  // failure leaves the user thinking the action worked.
  const mutationError =
    pauseMutation.isError ? "Failed to pause." :
    resumeMutation.isError ? "Failed to resume." :
    deleteMutation.isError ? "Failed to delete." :
    null;

  return (
    <div className={cn("rounded-lg border border-border/70 transition-colors", raisedSurface, hoverSurface)}>
      <div className="flex items-start gap-3 p-4 sm:gap-4">
        <Link href={`/automations/${automation.id}`} className="flex-1 min-w-0">
          <div className="flex items-start gap-2.5">
            <span
              className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-border bg-surface-pane text-lg leading-none"
              aria-label={`Automation icon for ${automation.name}`}
            >
              {automation.icon_value || "⚙️"}
            </span>
            <div className="min-w-0 flex-1 space-y-2">
              <div className="space-y-1 sm:flex sm:items-start sm:justify-between sm:gap-3 sm:space-y-0">
                <h3 className="break-words text-sm font-medium leading-5 text-foreground">
                  {automation.name}
                </h3>
                <span className="block break-words text-xs leading-5 text-muted-foreground sm:max-w-[18rem] sm:text-right">
                  {formatSchedule(automation)}
                </span>
              </div>
              <div className="flex flex-col gap-1 text-xs text-muted-foreground sm:flex-row sm:flex-wrap sm:gap-x-3 sm:gap-y-1">
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
            </div>
          </div>
        </Link>

        {canManage && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-8 self-start shrink-0"
                aria-label={`More options for ${automation.name}`}
              >
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
                onClick={handleDelete}
                disabled={deleteMutation.isPending}
                className="text-destructive focus:text-destructive"
              >
                <Trash2 className="h-3.5 w-3.5 mr-2" />
                Delete
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </div>
      {mutationError && (
        <p className="px-4 pb-3 text-xs text-destructive" role="alert">
          {mutationError}
        </p>
      )}
    </div>
  );
}

export default function AutomationsPage() {
  const { user } = useAuth();
  const canManage = user?.role === "admin" || user?.role === "member";
  const { data, isLoading } = useQuery({
    queryKey: ["automations"],
    queryFn: () => api.automations.list(),
    refetchInterval: 10000,
  });

  const automations = useMemo(() => data?.data ?? [], [data?.data]);
  const enabled = useMemo(() => automations.filter((a) => a.enabled), [automations]);
  const paused = useMemo(() => automations.filter((a) => !a.enabled), [automations]);

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="Automations"
          description="Recurring agents that run on a schedule for your team."
          action={canManage ? (
            <Button asChild size="sm">
              <Link href="/automations/new">
                <Plus className="h-4 w-4 mr-1.5" />
                New
              </Link>
            </Button>
          ) : undefined}
        />

        {isLoading && (
          <div className="text-center py-12 text-sm text-muted-foreground">
            Loading automations...
          </div>
        )}

        {!isLoading && automations.length === 0 && (
          <div className="text-center py-16 space-y-3">
            <RefreshCw className="h-8 w-8 mx-auto text-muted-foreground/40" />
            <p className="text-sm text-muted-foreground">No automations yet</p>
            {canManage && <Button asChild variant="outline" size="sm">
              <Link href="/automations/new">
                <Plus className="h-4 w-4 mr-1.5" />
                Create your first automation
              </Link>
            </Button>}
          </div>
        )}

        {enabled.length > 0 && (
          <section className="space-y-3">
            <h2 className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
              Enabled ({enabled.length})
            </h2>
            <div className="space-y-2">
              {enabled.map((a) => (
                <AutomationCard key={a.id} automation={a} canManage={canManage} />
              ))}
            </div>
          </section>
        )}

        {paused.length > 0 && (
          <section className="space-y-3">
            <h2 className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
              Paused ({paused.length})
            </h2>
            <div className="space-y-2">
              {paused.map((a) => (
                <AutomationCard key={a.id} automation={a} canManage={canManage} />
              ))}
            </div>
          </section>
        )}
      </div>
    </PageContainer>
  );
}
