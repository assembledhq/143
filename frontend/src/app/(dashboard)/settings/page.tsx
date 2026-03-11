"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import type { Organization, SingleResponse } from "@/lib/types";

export default function SettingsPage() {
  const { data: settings } = useQuery<SingleResponse<Organization>>({
    queryKey: ["settings"],
    queryFn: () => api.settings.get(),
  });

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="General Settings"
          description="Manage your organization."
        />
        <section className="space-y-3">
          <h2 className="text-[13px] font-medium text-foreground">General</h2>
          <Card>
            <CardContent>
              <div className="max-w-[560px] space-y-2">
                <Label htmlFor="org-name">Organization Name</Label>
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
