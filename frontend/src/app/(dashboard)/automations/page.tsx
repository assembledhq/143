"use client";

import { useMemo, useRef, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, Pause, Play, MoreHorizontal, Plus, Search, Trash2 } from "lucide-react";
import Link from "next/link";
import { api } from "@/lib/api";
import {
  removeAutomationFromListCaches,
  upsertAutomationInListCaches,
} from "@/lib/automation-list-cache";
import { queryKeys } from "@/lib/query-keys";
import { formatDateTime, formatTimeAgo } from "@/lib/utils";
import type { Automation } from "@/lib/types";
import {
  automationTemplateCategories,
  automationTemplates,
  featuredAutomationTemplateIDs,
  getAutomationTemplatesByCategory,
  type AutomationTemplate,
  type AutomationTemplateCategoryID,
} from "@/lib/automation-templates";
import { formatAutomationSchedule } from "./schedule-time";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { EmptyState } from "@/components/empty-state";
import { InteractiveCard } from "@/components/interactive-card";
import { ResourceRow } from "@/components/resource-row";
import { ResponsiveResourceList, type ResponsiveResourceListColumn } from "@/components/responsive-resource-list";
import { SectionGroup } from "@/components/section-group";
import { useAuth } from "@/hooks/use-auth";
import { Card, CardContent } from "@/components/ui/card";
import { StatusLabel } from "@/components/status-label";

const popularCategory = {
  id: "popular",
  name: "Popular",
  description: "Good first automations for teams setting up recurring agent work.",
} as const;

type TemplateGalleryCategoryID = AutomationTemplateCategoryID | typeof popularCategory.id;

function templateMatchesSearch(template: AutomationTemplate, query: string) {
  const normalizedQuery = query.trim().toLowerCase();
  if (!normalizedQuery) return true;

  const searchable = [
    template.name,
    template.summary,
    ...template.tags,
    ...template.outcomes,
  ].join(" ").toLowerCase();

  return searchable.includes(normalizedQuery);
}

function templatesForCategory(categoryID: TemplateGalleryCategoryID) {
  if (categoryID === "popular") {
    return automationTemplates.filter((template) =>
      featuredAutomationTemplateIDs.includes(template.id),
    );
  }

  return getAutomationTemplatesByCategory(categoryID);
}

function formatTemplateCadence(template: AutomationTemplate) {
  const unit = template.defaultInterval === 1
    ? template.defaultUnit.replace(/s$/, "")
    : template.defaultUnit;

  return `Every ${template.defaultInterval} ${unit}`;
}

type AutomationFilter = "all" | "enabled" | "paused";

function formatAutomationRunDate(value?: string) {
  return formatDateTime(value, { fallback: "Not scheduled" });
}

function automationStatus(automation: Automation) {
  return automation.enabled ? "Enabled" : "Paused";
}

// formatAutomationSchedule already appends "(timezone)" for cron and run-at
// schedules, so only surface a standalone timezone line when it isn't already
// part of the schedule label (e.g. sub-24h interval cadences).
function automationScheduleTimezone(automation: Automation) {
  if (!automation.timezone) return null;
  return formatAutomationSchedule(automation).includes(automation.timezone)
    ? null
    : automation.timezone;
}

function automationStatusBadge(automation: Automation) {
  return (
    <StatusLabel
      label={automationStatus(automation)}
      tone={automation.enabled ? "success" : "neutral"}
    />
  );
}

function automationMatchesQuery(automation: Automation, query: string) {
  const normalizedQuery = query.trim().toLowerCase();
  if (!normalizedQuery) return true;

  return [
    automation.name,
    automation.goal,
    automation.base_branch,
    automation.timezone,
    formatAutomationSchedule(automation),
    automationStatus(automation),
  ].join(" ").toLowerCase().includes(normalizedQuery);
}

function AutomationTemplateGallery({ canManage }: { canManage: boolean }) {
  const [query, setQuery] = useState("");
  const categories = [popularCategory, ...automationTemplateCategories];

  return (
    <SectionGroup
      className="border-t border-border/70 pt-8"
      title="Template library"
      description="Optional starting points for new recurring agents. Templates stay separate from the automations already running."
    >
      <Card variant="quiet" className="bg-surface-recessed/35">
        <CardContent className="space-y-4 p-3 sm:p-4">
          <div className="relative">
            <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="Search templates..."
              className="h-8 bg-background pl-9"
            />
          </div>

          <Tabs defaultValue={popularCategory.id}>
            <TabsList size="sm" className="max-w-full overflow-x-auto overflow-y-hidden">
              {categories.map((category) => (
                <TabsTrigger key={category.id} value={category.id}>
                  {category.name}
                </TabsTrigger>
              ))}
            </TabsList>

            {categories.map((category) => {
              const templates = templatesForCategory(category.id)
                .filter((template) => templateMatchesSearch(template, query));

              return (
                <TabsContent key={category.id} value={category.id} className="mt-3 space-y-3">
                  <div className="space-y-0.5">
                    <h3 className="text-xs font-medium text-foreground">{category.name}</h3>
                    <p className="text-xs text-muted-foreground">{category.description}</p>
                  </div>

                  {templates.length > 0 ? (
                    <div className="grid gap-2 lg:grid-cols-2">
                      {templates.map((template) => {
                        const Icon = template.icon;

                        return (
                          <InteractiveCard
                            key={template.id}
                            className="min-h-24 rounded-xl bg-card"
                          >
                            <CardContent className="flex items-start gap-3 p-3">
                              <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-border/70 bg-muted/40">
                                <Icon className="h-4 w-4 text-muted-foreground" />
                              </div>
                              <div className="min-w-0 flex-1 space-y-2">
                                <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
                                  <div className="min-w-0 space-y-1">
                                    <h4 className="text-sm font-medium leading-5 text-foreground">
                                      {template.name}
                                    </h4>
                                    <p className="line-clamp-2 text-xs leading-5 text-muted-foreground">
                                      {template.summary}
                                    </p>
                                  </div>
                                  <Badge variant="outline" className="w-fit shrink-0 bg-background text-muted-foreground">
                                    {formatTemplateCadence(template)}
                                  </Badge>
                                </div>

                                <div className="flex items-center justify-between gap-3">
                                  <div className="hidden min-w-0 flex-wrap gap-1.5 sm:flex">
                                    {template.tags.slice(0, 2).map((tag) => (
                                      <Badge key={tag} variant="ghost" className="px-0 text-muted-foreground">
                                        {tag}
                                      </Badge>
                                    ))}
                                  </div>

                                  {canManage ? (
                                    <Button asChild variant="ghost" size="sm" className="ml-auto">
                                      <Link
                                        href={`/automations/new?template=${template.id}`}
                                        aria-label={`Use ${template.name}`}
                                      >
                                        Use
                                        <ArrowRight className="h-3.5 w-3.5" />
                                      </Link>
                                    </Button>
                                  ) : null}
                                </div>
                              </div>
                            </CardContent>
                          </InteractiveCard>
                        );
                      })}
                    </div>
                  ) : (
                    <EmptyState
                      variant="inline"
                      icon={Search}
                      title="No templates found"
                      description="Try a different search or choose another category."
                    />
                  )}
                </TabsContent>
              );
            })}
          </Tabs>
        </CardContent>
      </Card>
    </SectionGroup>
  );
}

function AutomationsWorkspace({
  enabled,
  paused,
  canManage,
}: {
  enabled: Automation[];
  paused: Automation[];
  canManage: boolean;
}) {
  const [filter, setFilter] = useState<AutomationFilter>("all");
  const [query, setQuery] = useState("");
  const total = enabled.length + paused.length;
  const automations = useMemo(() => [...enabled, ...paused], [enabled, paused]);
  const filteredAutomations = useMemo(() => {
    const byStatus = automations.filter((automation) => {
      if (filter === "enabled") return automation.enabled;
      if (filter === "paused") return !automation.enabled;
      return true;
    });

    return byStatus.filter((automation) => automationMatchesQuery(automation, query));
  }, [automations, filter, query]);

  const columns: ResponsiveResourceListColumn<Automation>[] = [
    {
      id: "automation",
      header: "Automation",
      className: "w-[34%]",
      cellClassName: "min-w-0",
      render: (automation) => (
        <Link href={`/automations/${automation.id}`} className="block min-w-0 space-y-1">
          <div className="flex min-w-0 items-center gap-2">
            <span
              className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md border border-border bg-card text-base leading-none"
              aria-label={`Automation icon for ${automation.name}`}
            >
              {automation.icon_value || "⚙️"}
            </span>
            <span className="truncate text-sm font-medium text-foreground">{automation.name}</span>
          </div>
          {automation.goal ? (
            <p className="line-clamp-2 pl-9 text-xs leading-5 text-muted-foreground">
              {automation.goal}
            </p>
          ) : null}
        </Link>
      ),
    },
    {
      id: "status",
      header: "Status",
      className: "w-28",
      render: automationStatusBadge,
    },
    {
      id: "schedule",
      header: "Schedule",
      className: "w-[22%]",
      render: (automation) => {
        const timezone = automationScheduleTimezone(automation);
        return (
          <div className="space-y-1">
            <div className="text-xs text-foreground">{formatAutomationSchedule(automation)}</div>
            {timezone ? (
              <div className="text-xs text-muted-foreground">{timezone}</div>
            ) : null}
          </div>
        );
      },
    },
    {
      id: "next",
      header: "Next run",
      className: "w-32",
      render: (automation) => (
        <span className="text-xs text-muted-foreground">
          {automation.enabled ? formatAutomationRunDate(automation.next_run_at) : "Paused"}
        </span>
      ),
    },
    {
      id: "last",
      header: "Last run",
      className: "w-28",
      render: (automation) => (
        <span className="text-xs text-muted-foreground">
          {automation.last_run_at ? formatTimeAgo(automation.last_run_at) : "Never"}
        </span>
      ),
    },
    {
      id: "actions",
      header: <span className="sr-only">Actions</span>,
      className: "w-12 text-right",
      cellClassName: "text-right",
      render: (automation) => <AutomationActions automation={automation} canManage={canManage} />,
    },
  ];

  return (
    <SectionGroup
      aria-label="Your automations"
      title="Your automations"
      description={total > 0
        ? "Recurring agents currently configured for this team."
        : "No recurring agents are configured yet."}
    >
      {total > 0 ? (
        <div className="space-y-3">
          <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
            <Tabs value={filter} onValueChange={(value) => setFilter(value as AutomationFilter)}>
              <TabsList size="sm" className="w-full justify-start overflow-x-auto lg:w-auto">
                <TabsTrigger value="all">All {total}</TabsTrigger>
                <TabsTrigger value="enabled">Enabled {enabled.length}</TabsTrigger>
                <TabsTrigger value="paused">Paused {paused.length}</TabsTrigger>
              </TabsList>
            </Tabs>
            <div className="relative w-full lg:max-w-72">
              <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Search automations..."
                className="h-8 bg-background pl-9"
              />
            </div>
          </div>

          <ResponsiveResourceList
            ariaLabel="Automations"
            items={filteredAutomations}
            getItemKey={(automation) => automation.id}
            columns={columns}
            emptyState={
              query.trim()
                ? "No automations match your search."
                : "No automations match this filter."
            }
            renderMobileItem={(automation) => (
              <AutomationMobileRow automation={automation} canManage={canManage} />
            )}
          />
        </div>
      ) : (
        <EmptyState
          icon={Plus}
          title="Create an automation when you are ready."
          description="Start from a blank setup or use a template below."
        />
      )}
    </SectionGroup>
  );
}

function AutomationActions({ automation, canManage }: { automation: Automation; canManage: boolean }) {
  const queryClient = useQueryClient();

  const pauseMutation = useMutation({
    mutationFn: () => api.automations.pause(automation.id),
    onSuccess: (res) => {
      upsertAutomationInListCaches(queryClient, res.data);
      queryClient.setQueryData(queryKeys.automations.detail(res.data.id), res);
      queryClient.invalidateQueries({ queryKey: queryKeys.automations.all });
    },
  });

  const resumeMutation = useMutation({
    mutationFn: () => api.automations.resume(automation.id),
    onSuccess: (res) => {
      upsertAutomationInListCaches(queryClient, res.data);
      queryClient.setQueryData(queryKeys.automations.detail(res.data.id), res);
      queryClient.invalidateQueries({ queryKey: queryKeys.automations.all });
    },
  });

  // deleteInFlight closes the same render-tick race that runNowInFlight does on
  // the detail page: `disabled={isPending}` is only applied on the next render,
  // so a rapid double-click can fire mutate() twice before React catches up. A
  // synchronous ref rejects the second click immediately.
  const deleteInFlight = useRef(false);
  const deleteMutation = useMutation({
    mutationFn: () => api.automations.del(automation.id),
    onSuccess: () => {
      removeAutomationFromListCaches(queryClient, automation.id);
      queryClient.removeQueries({
        queryKey: queryKeys.automations.detail(automation.id),
      });
      queryClient.invalidateQueries({ queryKey: queryKeys.automations.all });
    },
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

  if (!canManage) return null;

  return (
    <div className="flex flex-col items-end gap-2">
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size="icon"
            className="size-11 shrink-0 md:size-8"
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
      {mutationError && (
        <p className="text-right text-xs text-destructive" role="alert">
          {mutationError}
        </p>
      )}
    </div>
  );
}

function AutomationMobileRow({ automation, canManage }: { automation: Automation; canManage: boolean }) {
  const timezone = automationScheduleTimezone(automation);
  return (
    <ResourceRow
      leading={(
        <span
          className="flex h-8 w-8 items-center justify-center rounded-md border border-border bg-card text-lg leading-none"
          aria-label={`Automation icon for ${automation.name}`}
        >
          {automation.icon_value || "⚙️"}
        </span>
      )}
      title={(
        <Link href={`/automations/${automation.id}`} className="break-words text-sm leading-5 hover:underline">
          {automation.name}
        </Link>
      )}
      status={automationStatusBadge(automation)}
      metadata={(
        <span>
          {formatAutomationSchedule(automation)}
          {timezone ? ` · ${timezone}` : ""}
        </span>
      )}
      detail={(
        <span>
          {automation.enabled
            ? `Next ${formatAutomationRunDate(automation.next_run_at)}`
            : automation.paused_at
              ? `Paused ${formatTimeAgo(automation.paused_at)}`
              : "Paused"}
          <span aria-hidden="true"> · </span>
          Last {automation.last_run_at ? formatTimeAgo(automation.last_run_at) : "never"}
        </span>
      )}
      actions={<AutomationActions automation={automation} canManage={canManage} />}
    />
  );
}

export default function AutomationsPage() {
  const { user } = useAuth();
  const canManage = user?.role === "admin" || user?.role === "member";
  const { data, isLoading } = useQuery({
    queryKey: queryKeys.automations.all,
    queryFn: () => api.automations.list(),
    refetchInterval: 10000,
  });

  const automations = useMemo(() => data?.data ?? [], [data?.data]);
  const enabled = useMemo(() => automations.filter((a) => a.enabled), [automations]);
  const paused = useMemo(() => automations.filter((a) => !a.enabled), [automations]);

  return (
    <PageContainer size="default">
      <div className="space-y-8">
        <PageHeader
          title="Automations"
          description="Recurring agents that run on a schedule for your team."
          action={canManage ? (
            <Button asChild>
              <Link href="/automations/new">
                <Plus className="h-4 w-4" />
                New automation
              </Link>
            </Button>
          ) : undefined}
        />

        {isLoading && (
          <div className="text-center py-12 text-sm text-muted-foreground">
            Loading automations...
          </div>
        )}

        {!isLoading && (
          <AutomationsWorkspace enabled={enabled} paused={paused} canManage={canManage} />
        )}

        {!isLoading && (
          <AutomationTemplateGallery canManage={canManage} />
        )}
      </div>
    </PageContainer>
  );
}
