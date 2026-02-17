"use client";

import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { PageHeader } from "@/components/page-header";

export default function SettingsPage() {
  return (
    <div className="space-y-8">
      <PageHeader
        title="Settings"
        description="Manage your organization and integrations."
      />

      <section className="space-y-3">
        <h2 className="text-[13px] font-medium text-foreground">General</h2>
        <Card>
          <CardContent>
            <div className="space-y-2">
              <Label htmlFor="org-name">Organization Name</Label>
              <Input id="org-name" placeholder="My Organization" />
            </div>
          </CardContent>
        </Card>
      </section>

      <section className="space-y-3">
        <h2 className="text-[13px] font-medium text-foreground">Integrations</h2>
        <Card className="gap-0 py-0">
          <div className="flex items-center justify-between p-5">
            <div>
              <p className="text-sm font-medium text-foreground">GitHub</p>
              <p className="mt-0.5 text-sm text-muted-foreground">
                Connect your GitHub account to sync repositories and open PRs.
              </p>
            </div>
            <Button size="sm" onClick={() => api.auth.login()}>
              Connect
            </Button>
          </div>
          <div className="border-t border-border" />
          <div className="flex items-center justify-between p-5">
            <div>
              <p className="text-sm font-medium text-foreground">Sentry</p>
              <p className="mt-0.5 text-sm text-muted-foreground">
                Pull production errors and auto-generate fixes.
              </p>
            </div>
            <Badge variant="secondary">Coming soon</Badge>
          </div>
          <div className="border-t border-border" />
          <div className="flex items-center justify-between p-5">
            <div>
              <p className="text-sm font-medium text-foreground">Linear</p>
              <p className="mt-0.5 text-sm text-muted-foreground">
                Sync issues from Linear and auto-assign fixes.
              </p>
            </div>
            <Badge variant="secondary">Coming soon</Badge>
          </div>
        </Card>
      </section>
    </div>
  );
}
