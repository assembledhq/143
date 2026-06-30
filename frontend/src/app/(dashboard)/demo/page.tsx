"use client";

import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import {
  ArrowRight,
  Bot,
  Code2,
  ExternalLink,
  GitPullRequest,
  MonitorPlay,
  Play,
} from "lucide-react";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { ErrorText } from "@/components/ui/error-notice";
import { api } from "@/lib/api";

const replaySteps = [
  {
    title: "Start from production context",
    description: "Open the seeded session to see the issue, repository, and agent instructions 143 carries into a coding run.",
    icon: Bot,
  },
  {
    title: "Watch the agent trail",
    description: "Read the transcript, logs, review comments, and validation rows that explain what the agent did and why.",
    icon: Play,
  },
  {
    title: "Inspect the resulting change",
    description: "Use the captured diff and review views to evaluate the code before anything merges.",
    icon: Code2,
  },
  {
    title: "Review preview and PR health",
    description: "Follow the seeded preview state and pull request health signals that would normally gate shipping.",
    icon: MonitorPlay,
  },
];

export default function DemoPage() {
  const { data, isLoading, error } = useQuery({
    queryKey: ["demo", "manifest"],
    queryFn: () => api.demo.manifest(),
  });
  const manifest = data?.data;

  return (
    <PageContainer size="wide" className="flex flex-col gap-6">
      <PageHeader
        title="143 demo"
        description="A read-only replay of a seeded coding-agent workflow."
      />

      {isLoading && (
        <div className="grid gap-3 md:grid-cols-4">
          {Array.from({ length: 4 }).map((_, index) => (
            <Card key={index} className="h-28 bg-muted/35 animate-pulse" />
          ))}
        </div>
      )}

      {error && (
        <ErrorText className="rounded-md bg-destructive/10 px-3 py-2 text-sm" role="alert">
          Demo metadata could not be loaded.
        </ErrorText>
      )}

      {manifest && (
        <>
          <Card>
            <CardContent className="flex flex-col gap-4 md:flex-row md:items-center md:justify-between">
              <div className="min-w-0 space-y-2">
                <div className="flex flex-wrap items-center gap-2">
                  <Badge variant="secondary">{manifest.org.name}</Badge>
                  {manifest.read_only ? <Badge variant="outline">Read-only</Badge> : null}
                </div>
                <h2 className="text-lg font-semibold text-foreground">Follow a complete agent run</h2>
                <p className="max-w-2xl text-sm text-muted-foreground">
                  This demo uses fixed seed data so you can inspect the product without starting workers,
                  previews, pull requests, or LLM calls.
                </p>
              </div>
              <div className="flex flex-wrap gap-2">
                <Button asChild>
                  <Link href={manifest.routes.primary_session}>
                    Open session
                    <ArrowRight className="ml-2 h-4 w-4" />
                  </Link>
                </Button>
                <Button asChild variant="outline">
                  <Link href={manifest.routes.primary_preview}>
                    Open preview state
                    <MonitorPlay className="ml-2 h-4 w-4" />
                  </Link>
                </Button>
              </div>
            </CardContent>
          </Card>

          <section className="grid gap-3 md:grid-cols-4">
            {replaySteps.map((step, index) => (
              <Card key={step.title}>
                <CardContent>
                  <div className="mb-4 flex items-center justify-between">
                    <div className="flex h-9 w-9 items-center justify-center rounded-md bg-muted text-foreground">
                      <step.icon className="h-4 w-4" />
                    </div>
                    <span className="text-xs font-medium text-muted-foreground tabular-nums">
                      {String(index + 1).padStart(2, "0")}
                    </span>
                  </div>
                  <h3 className="text-sm font-semibold text-foreground">{step.title}</h3>
                  <p className="mt-2 text-sm text-muted-foreground">{step.description}</p>
                </CardContent>
              </Card>
            ))}
          </section>

          <section className="grid gap-3 md:grid-cols-3">
            <Button asChild variant="outline" className="h-auto justify-between gap-4 p-4">
              <Link href={manifest.routes.sessions}>
                <span className="text-left">
                  <span className="block text-sm font-medium">Browse seeded sessions</span>
                  <span className="mt-1 block text-xs text-muted-foreground">Compare completed, active, blocked, and failed runs.</span>
                </span>
                <ArrowRight className="h-4 w-4 shrink-0" />
              </Link>
            </Button>
            <Button asChild variant="outline" className="h-auto justify-between gap-4 p-4">
              <Link href={manifest.routes.primary_preview}>
                <span className="text-left">
                  <span className="block text-sm font-medium">Inspect preview metadata</span>
                  <span className="mt-1 block text-xs text-muted-foreground">See the seeded preview group and health state.</span>
                </span>
                <MonitorPlay className="h-4 w-4 shrink-0" />
              </Link>
            </Button>
            <Button asChild variant="outline" className="h-auto justify-between gap-4 p-4">
              <a href={manifest.routes.pull_request} target="_blank" rel="noopener noreferrer">
                <span className="text-left">
                  <span className="block text-sm font-medium">
                    PR #{manifest.pull_request.number} in {manifest.pull_request.repository}
                  </span>
                  <span className="mt-1 block text-xs text-muted-foreground">Open the public GitHub PR used by the seed story.</span>
                </span>
                <ExternalLink className="h-4 w-4 shrink-0" />
              </a>
            </Button>
          </section>

          <Card>
            <CardContent>
              <div className="flex items-center gap-2 text-sm font-medium text-foreground">
                <GitPullRequest className="h-4 w-4" />
                Seeded identifiers
              </div>
              <dl className="mt-3 grid gap-3 text-xs md:grid-cols-3">
                <div>
                  <dt className="text-muted-foreground">Session</dt>
                  <dd className="mt-1 truncate font-mono text-foreground">{manifest.primary.session_id}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">Preview group</dt>
                  <dd className="mt-1 truncate font-mono text-foreground">{manifest.primary.preview_group_id}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">Preview target</dt>
                  <dd className="mt-1 truncate font-mono text-foreground">{manifest.primary.preview_target_id}</dd>
                </div>
              </dl>
            </CardContent>
          </Card>
        </>
      )}
    </PageContainer>
  );
}
