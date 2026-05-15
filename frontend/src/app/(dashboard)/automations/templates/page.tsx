"use client";

import Link from "next/link";
import { ArrowRight } from "lucide-react";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { useAuth } from "@/hooks/use-auth";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  automationTemplateCategories,
  getAutomationTemplatesByCategory,
} from "@/lib/automation-templates";

export default function AutomationTemplatesPage() {
  const { user } = useAuth();
  const canManage = user?.role === "admin" || user?.role === "member";

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="Automation templates"
          description="Browse examples and richer prompts for recurring agent work. These templates favor clear scope, concrete outputs, and explicit verification."
          action={canManage ? (
            <Button asChild size="sm">
              <Link href="/automations/new">
                New automation
              </Link>
            </Button>
          ) : undefined}
        />

        <Card className="border-dashed">
          <CardHeader>
            <CardTitle>How these templates are written</CardTitle>
            <CardDescription>
              The strongest coding-agent workflows are structured like a good issue:
              clear task framing, expected outputs, and a way to verify the work.
            </CardDescription>
          </CardHeader>
        </Card>

        <Tabs defaultValue={automationTemplateCategories[0]?.id}>
          <TabsList className="overflow-x-auto overflow-y-hidden">
            {automationTemplateCategories.map((category) => (
              <TabsTrigger key={category.id} value={category.id}>
                {category.name}
              </TabsTrigger>
            ))}
          </TabsList>

          {automationTemplateCategories.map((category) => {
            const templates = getAutomationTemplatesByCategory(category.id);

            return (
              <TabsContent key={category.id} value={category.id} className="mt-4 space-y-4">
                <div className="space-y-1">
                  <h2 className="text-sm font-medium text-foreground">{category.name}</h2>
                  <p className="text-sm text-muted-foreground">{category.description}</p>
                </div>

                <div className="grid gap-4 lg:grid-cols-2">
                  {templates.map((template) => {
                    const Icon = template.icon;

                    return (
                      <Card key={template.id} className="h-full">
                        <CardHeader className="space-y-3">
                          <div className="flex items-start justify-between gap-3">
                            <div className="flex items-center gap-2">
                              <div className="rounded-md border border-border bg-muted/50 p-2">
                                <Icon className="h-4 w-4 text-foreground" />
                              </div>
                              <div>
                                <CardTitle className="text-base">{template.name}</CardTitle>
                                <CardDescription className="mt-1">
                                  {template.summary}
                                </CardDescription>
                              </div>
                            </div>
                            <span className="text-xs text-muted-foreground">
                              Every {template.defaultInterval}{" "}
                              {template.defaultInterval === 1
                                ? template.defaultUnit.replace(/s$/, "")
                                : template.defaultUnit}
                            </span>
                          </div>

                          <div className="flex flex-wrap gap-2">
                            {template.tags.map((tag) => (
                              <Badge key={tag} variant="secondary">
                                {tag}
                              </Badge>
                            ))}
                          </div>
                        </CardHeader>
                        <CardContent className="space-y-4">
                          <div className="space-y-2">
                            <h3 className="text-sm font-medium text-foreground">Expected outcomes</h3>
                            <ul className="space-y-1 text-sm text-muted-foreground">
                              {template.outcomes.map((outcome) => (
                                <li key={outcome}>• {outcome}</li>
                              ))}
                            </ul>
                          </div>

                          <div className="space-y-2">
                            <h3 className="text-sm font-medium text-foreground">Prompt preview</h3>
                            <div className="rounded-md border border-border bg-muted/30 p-3">
                              <p className="whitespace-pre-line text-xs leading-5 text-muted-foreground">
                                {template.goal}
                              </p>
                            </div>
                          </div>

                          {canManage ? (
                            <Button asChild variant="outline" size="sm">
                              <Link href={`/automations/new?template=${template.id}`}>
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
              </TabsContent>
            );
          })}
        </Tabs>
      </div>
    </PageContainer>
  );
}
