"use client";

import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import { AuditLogTrigger } from "@/components/audit/audit-log-trigger";
import { ThemeSelect } from "@/components/theme-select";
import type { Organization, SingleResponse } from "@/lib/types";

function GitHubPRConnection() {
  const queryClient = useQueryClient();
  const { data: ghStatus, isLoading } = useQuery({
    queryKey: ["github-status"],
    queryFn: () => api.githubStatus.get(),
  });
  const disconnectMutation = useMutation({
    mutationFn: () => api.githubStatus.disconnect(),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["github-status"] }),
  });

  if (isLoading) return null;

  return (
    <section className="space-y-3">
      <h2 className="text-[13px] font-medium text-foreground">Pull requests</h2>
      <Card>
        <CardContent>
          <div className="flex items-center justify-between">
            <div className="space-y-0.5">
              <Label>GitHub connection for PRs</Label>
              <p className="text-[13px] text-muted-foreground">
                {ghStatus?.connected && ghStatus?.has_repo_scope
                  ? `Connected as @${ghStatus.github_login} — PRs will be authored by you`
                  : ghStatus?.connected && !ghStatus?.has_repo_scope
                    ? `Connected as @${ghStatus.github_login} — missing repo access, reconnect to author PRs`
                    : "Connect your GitHub account to create PRs under your name"}
              </p>
            </div>
            <div className="flex items-center gap-2">
              {ghStatus?.connected ? (
                <>
                  {!ghStatus.has_repo_scope && (
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => api.githubStatus.connect()}
                    >
                      Reconnect
                    </Button>
                  )}
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => disconnectMutation.mutate()}
                    disabled={disconnectMutation.isPending}
                  >
                    Disconnect
                  </Button>
                </>
              ) : (
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => api.githubStatus.connect()}
                >
                  Connect GitHub
                </Button>
              )}
            </div>
          </div>
        </CardContent>
      </Card>
    </section>
  );
}

export default function SettingsPage() {
  const { data: settings } = useQuery<SingleResponse<Organization>>({
    queryKey: ["settings"],
    queryFn: () => api.settings.get(),
  });

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="General settings"
          description="Manage your organization."
        />
        <AuditLogTrigger
          filters={{ resource_type: "settings" }}
          title="Settings activity"
        />
        <section className="space-y-3">
          <h2 className="text-[13px] font-medium text-foreground">Appearance</h2>
          <Card>
            <CardContent>
              <div className="flex items-center justify-between">
                <div className="space-y-0.5">
                  <Label>Theme</Label>
                  <p className="text-[13px] text-muted-foreground">
                    Select your preferred color scheme
                  </p>
                </div>
                <ThemeSelect />
              </div>
            </CardContent>
          </Card>
        </section>

        <GitHubPRConnection />

        <section className="space-y-3">
          <h2 className="text-[13px] font-medium text-foreground">General</h2>
          <Card>
            <CardContent>
              <div className="max-w-[560px] space-y-2">
                <Label htmlFor="org-name">Organization name</Label>
                <Input
                  id="org-name"
                  value={settings?.data?.name ?? ""}
                  disabled
                  className="bg-muted"
                />
              </div>
            </CardContent>
          </Card>
        </section>
      </div>
    </PageContainer>
  );
}
