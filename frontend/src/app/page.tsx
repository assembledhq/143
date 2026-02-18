"use client";

import { GitBranch, AlertCircle, RectangleEllipsis } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { PageHeader } from "@/components/page-header";

export default function Overview() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Overview"
        description="Get started by connecting your tools."
      />

      <div className="space-y-3">
        <Card className="py-0">
          <CardContent className="flex items-start gap-4 py-4">
            <div className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-muted">
              <GitBranch className="h-4 w-4 text-muted-foreground" />
            </div>
            <div className="flex-1 min-w-0">
              <p className="text-sm font-medium text-foreground">Connect GitHub</p>
              <p className="mt-0.5 text-sm text-muted-foreground">
                Link your GitHub account so 143 can open pull requests on your repositories.
              </p>
            </div>
            <Button size="sm" onClick={() => api.auth.login()} aria-label="Connect GitHub">
              Connect
            </Button>
          </CardContent>
        </Card>
        <Card className="py-0">
          <CardContent className="flex items-start gap-4 py-4">
            <div className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-muted">
              <AlertCircle className="h-4 w-4 text-muted-foreground" />
            </div>
            <div className="flex-1 min-w-0">
              <p className="text-sm font-medium text-foreground">Connect Sentry</p>
              <p className="mt-0.5 text-sm text-muted-foreground">
                Connect Sentry so 143 can pull production errors and auto-generate fixes.
              </p>
            </div>
            <Button size="sm" onClick={() => api.auth.loginSentry()} aria-label="Connect Sentry">
              Connect
            </Button>
          </CardContent>
        </Card>
        <Card className="py-0">
          <CardContent className="flex items-start gap-4 py-4">
            <div className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-muted">
              <RectangleEllipsis className="h-4 w-4 text-muted-foreground" />
            </div>
            <div className="flex-1 min-w-0">
              <p className="text-sm font-medium text-foreground">Connect Linear</p>
              <p className="mt-0.5 text-sm text-muted-foreground">
                Sync issues from Linear and auto-assign fixes.
              </p>
            </div>
            <Badge variant="secondary">Coming soon</Badge>
          </CardContent>
        </Card>
      </div>

      <p className="text-sm text-muted-foreground">
        Once integrations are connected, 143 picks up issues, generates fixes, and opens PRs automatically.
      </p>
    </div>
  );
}
