"use client";

import { useMemo, useRef, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, Pause, Play, MoreHorizontal, Plus, Search, Trash2 } from "lucide-react";
import Link from "next/link";
import { api } from "@/lib/api";
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
import { useAuth } from "@/hooks/use-auth";

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

function AutomationTemplateGallery({ canManage }: { canManage: boolean }) {
  const [query, setQuery] = useState("");
  const categories = [popularCategory, ...automationTemplateCategories];

  return (
    <section className="border-t border-border/70 pt-8">
      <div className="mb-4 flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
        <div className="space-y-1.5">
          <h2 className="text-sm font-medium text-foreground">Template library</h2>
          <p className="max-w-2xl text-sm text-muted-foreground">
            Optional starting points for new recurring agents. Templates stay separate from the automations already running.
          </p>
        </div>
      </div>

      <div className="space-y-4 rounded-lg border border-border/70 bg-muted/20 p-3 sm:p-4">
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
                        <div
                          key={template.id}
                          className="flex min-h-24 items-start gap-3 rounded-md border border-border/60 bg-background p-3"
                        >
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
                        </div>
                      );
                    })}
                  </div>
                ) : (
                  <div className="rounded-md border border-border/60 bg-background px-4 py-8 text-center text-sm text-muted-foreground">
                    No templates match your search.
                  </div>
                )}
              </TabsContent>
            );
          })}
        </Tabs>
      </div>
    </section>
  );
}

function AutomationSection({
  title,
  automations,
  canManage,
}: {
  title: string;
  automations: Automation[];
  canManage: boolean;
}) {
  if (automations.length === 0) return null;

  return (
    <section className="space-y-2">
      <h3 className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
        {title} ({automations.length})
      </h3>
      <div className="overflow-hidden rounded-lg border border-border/70 bg-background">
        {automations.map((automation) => (
          <AutomationCard key={automation.id} automation={automation} canManage={canManage} />
        ))}
      </div>
    </section>
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
  const total = enabled.length + paused.length;

  return (
    <section className="space-y-4" aria-labelledby="your-automations-heading">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-end sm:justify-between">
        <div className="space-y-1">
          <h2 id="your-automations-heading" className="text-sm font-medium text-foreground">
            Your automations
          </h2>
          <p className="text-sm text-muted-foreground">
            {total > 0
              ? "Recurring agents currently configured for this team."
              : "No recurring agents are configured yet."}
          </p>
        </div>
        {total > 0 ? (
          <div className="flex gap-2 text-xs text-muted-foreground">
            <span>{enabled.length} enabled</span>
            <span aria-hidden="true">/</span>
            <span>{paused.length} paused</span>
          </div>
        ) : null}
      </div>

      {total > 0 ? (
        <div className="space-y-5">
          <AutomationSection title="Enabled" automations={enabled} canManage={canManage} />
          <AutomationSection title="Paused" automations={paused} canManage={canManage} />
        </div>
      ) : (
        <div className="rounded-lg border border-dashed border-border/80 bg-muted/20 px-4 py-8 text-center">
          <p className="text-sm font-medium text-foreground">Create an automation when you are ready.</p>
          <p className="mt-1 text-sm text-muted-foreground">
            Start from a blank setup or use a template below.
          </p>
        </div>
      )}
    </section>
  );
}

function AutomationCard({ automation, canManage }: { automation: Automation; canManage: boolean }) {
  const queryClient = useQueryClient();
  const schedule = formatAutomationSchedule(automation);

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
    <div className="border-b border-border/60 bg-background transition-colors last:border-b-0 hover:bg-muted/30">
      <div className="flex items-start gap-3 p-4 sm:gap-4">
        <Link href={`/automations/${automation.id}`} className="min-w-0 flex-1">
          <div className="flex items-start gap-3">
            <span
              className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border border-border/80 bg-card text-lg leading-none shadow-sm"
              aria-label={`Automation icon for ${automation.name}`}
            >
              {automation.icon_value || "⚙️"}
            </span>
            <div className="min-w-0 flex-1 space-y-2.5">
              <div className="space-y-1.5">
                <h3 className="break-words text-sm font-medium leading-5 text-foreground">
                  {automation.name}
                </h3>
                <span className="inline-flex max-w-full items-center rounded-md bg-muted/45 px-2 py-0.5 text-xs leading-5 text-muted-foreground">
                  <span className="truncate">{schedule}</span>
                </span>
              </div>
              <div className="flex flex-col gap-1 text-xs leading-5 text-muted-foreground sm:flex-row sm:flex-wrap sm:gap-x-3 sm:gap-y-1">
                {automation.last_run_at && (
                  <span>Last run: {formatTimeAgo(automation.last_run_at)}</span>
                )}
                {automation.next_run_at && automation.enabled && (
                  <span title={formatDateTime(automation.next_run_at, { year: true, seconds: true })}>
                    Next: {formatDateTime(automation.next_run_at)}
                  </span>
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
      <div className="space-y-8">
        <PageHeader
          title="Automations"
          description="Recurring agents that run on a schedule for your team."
          action={canManage ? (
            <Button asChild>
              <Link href="/automations/new">
                <Plus className="h-4 w-4" />
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
