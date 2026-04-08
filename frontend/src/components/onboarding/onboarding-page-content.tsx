"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { Card, CardContent } from "@/components/ui/card";
import { SetupChecklist } from "@/components/setup-checklist";
import { useSetupStatus } from "@/hooks/use-setup-status";

export function OnboardingPageContent() {
  const router = useRouter();
  const { isLoading, isSetupComplete } = useSetupStatus();

  useEffect(() => {
    if (!isLoading && isSetupComplete) {
      router.replace("/autopilot");
    }
  }, [isLoading, isSetupComplete, router]);

  if (isLoading) {
    return (
      <PageContainer size="default">
        <div className="space-y-8">
          <PageHeader
            title="Welcome to 143"
            description="Set up your connections to get started with Autopilot."
          />
          <p className="text-sm text-muted-foreground">Loading setup status...</p>
        </div>
      </PageContainer>
    );
  }

  if (isSetupComplete) {
    return null;
  }

  return (
    <PageContainer size="default">
      <div className="space-y-8">
        <PageHeader
          title="Welcome to 143"
          description="Set up your connections to get started with Autopilot."
        />

        <Card className="border-border/70 shadow-sm">
          <CardContent className="space-y-4 py-8">
            <div className="space-y-2">
              <p className="text-sm font-medium text-foreground">
                Autopilot needs a few connections before it can start analyzing.
              </p>
              <p className="max-w-3xl text-lg leading-8 text-foreground">
                Connect a coding agent and GitHub repositories, then run the first analysis.
              </p>
            </div>
          </CardContent>
        </Card>

        <SetupChecklist />
      </div>
    </PageContainer>
  );
}
