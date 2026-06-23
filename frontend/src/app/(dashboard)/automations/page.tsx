"use client";

import { useMemo, useRef, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, Plus, Pause, Play, MoreHorizontal, Search, Trash2 } from "lucide-react";
import Link from "next/link";
import { api } from "@/lib/api";
import { formatTimeAgo } from "@/lib/utils";
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
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
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

function EmptyAutomationTemplateGallery({ canManage }: { canManage: boolean }) {
  const [query, setQuery] = useState("");
  const categories = [popularCategory, ...automationTemplateCategories];

  return (
    <section className="space-y-4">
      <div className="space-y-4 rounded-lg border border-dashed border-border bg-muted/10 p-4">
        <div className="grid gap-4 sm:grid-cols-[1fr_auto] sm:items-start">
          <div className="space-y-1.5">
            <h2 className="text-sm font-medium text-foreground">Create your first automation</h2>
            <p className="text-sm text-muted-foreground">
              Start with a recurring agent template, then customize the goal, repository, schedule, and model before it runs.
            </p>
          </div>
          {canManage ? (
            <Button asChild variant="outline" size="sm" className="w-full shrink-0 sm:w-auto sm:justify-self-end">
              <Link href="/automations/new">
                <Plus className="mr-1.5 h-4 w-4" />
                Start from blank
              </Link>
            </Button>
          ) : null}
        </div>

        <div className="space-y-4">
          <div className="relative">
            <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="Search templates..."
              className="pl-9"
            />
          </div>

          <Tabs defaultValue={popularCategory.id}>
            <TabsList className="max-w-full overflow-x-auto overflow-y-hidden">
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
                <TabsContent key={category.id} value={category.id} className="mt-4 space-y-4">
                  <div className="space-y-1">
                    <h3 className="text-sm font-medium text-foreground">{category.name}</h3>
                    <p className="text-sm text-muted-foreground">{category.description}</p>
                  </div>

                  {templates.length > 0 ? (
                    <div className="grid gap-3 lg:grid-cols-2">
                      {templates.map((template) => {
                        const Icon = template.icon;

                        return (
                          <Card key={template.id} className="h-full">
                            <CardHeader className="space-y-3">
                              <div className="flex items-start gap-3">
                                <div className="rounded-md border border-border bg-muted/50 p-2">
                                  <Icon className="h-4 w-4 text-foreground" />
                                </div>
                                <div className="min-w-0 flex-1 space-y-1">
                                  <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
                                    <CardTitle className="text-base leading-5">
                                      <h4>{template.name}</h4>
                                    </CardTitle>
                                    <Badge variant="secondary" className="w-fit shrink-0">
                                      {formatTemplateCadence(template)}
                                    </Badge>
                                  </div>
                                  <CardDescription className="leading-5">
                                    {template.summary}
                                  </CardDescription>
                                </div>
                              </div>
                              <div className="flex flex-wrap gap-2">
                                {template.tags.slice(0, 3).map((tag) => (
                                  <Badge key={tag} variant="outline">
                                    {tag}
                                  </Badge>
                                ))}
                              </div>
                            </CardHeader>
                            <CardContent className="space-y-4">
                              <div className="space-y-2">
                                <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
                                  Expected output
                                </p>
                                <ul className="space-y-1 text-sm text-muted-foreground">
                                  {template.outcomes.slice(0, 2).map((outcome) => (
                                    <li key={outcome}>• {outcome}</li>
                                  ))}
                                </ul>
                              </div>

                              {canManage ? (
                                <Button asChild variant="outline" size="sm">
                                  <Link
                                    href={`/automations/new?template=${template.id}`}
                                    aria-label={`Use ${template.name}`}
                                  >
                                    Use template
                                    <ArrowRight className="ml-1.5 h-3.5 w-3.5" />
                                  </Link>
                                </Button>
                              ) : null}
                            </CardContent>
                          </Card>
                        );
                      })}
                    </div>
                  ) : (
                    <div className="rounded-lg border border-border bg-muted/20 px-4 py-8 text-center text-sm text-muted-foreground">
                      No templates match your search.
                    </div>
                  )}
                </TabsContent>
              );
            })}
          </Tabs>
        </div>
      </div>
    </section>
  );
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
    <div className="rounded-lg border border-border bg-background transition-colors hover:bg-muted/30">
      <div className="flex items-start gap-3 p-4 sm:gap-4">
        <Link href={`/automations/${automation.id}`} className="flex-1 min-w-0">
          <div className="flex items-start gap-2.5">
            <span
              className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-border bg-card text-lg leading-none"
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
                  {formatAutomationSchedule(automation)}
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
          <EmptyAutomationTemplateGallery canManage={canManage} />
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
