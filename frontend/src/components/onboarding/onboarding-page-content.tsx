"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { Mail } from "lucide-react";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { notify as toast } from "@/lib/notify";
import { useAuth } from "@/hooks/use-auth";

import { SetupChecklist } from "@/components/setup-checklist";
import { useSetupStatus } from "@/hooks/use-setup-status";

// VerifyEmailBanner closes the post-signup feedback loop for password
// accounts: Register sends a verification link, and without this banner
// nothing in the product explains the email that just landed in their
// inbox. OAuth accounts (github_id / google_id set) are provider-attested
// and never see it.
function VerifyEmailBanner() {
  const { user } = useAuth();
  const [sending, setSending] = useState(false);

  const isPasswordAccount = !!user && !user.github_id && !user.google_id;
  if (!isPasswordAccount || user.email_verified !== false) {
    return null;
  }

  const resend = async () => {
    setSending(true);
    try {
      await api.auth.sendEmailVerification();
      toast.success("Verification email sent — check your inbox.");
    } catch {
      toast.error("Couldn't send the verification email. Please try again.");
    } finally {
      setSending(false);
    }
  };

  return (
    <div
      className="flex flex-col gap-2 rounded-md border border-border bg-muted/50 px-4 py-3 sm:flex-row sm:items-center"
      data-testid="onboarding-verify-email-banner"
    >
      <Mail className="hidden h-4 w-4 shrink-0 text-muted-foreground sm:block" />
      <div className="flex-1 text-sm text-muted-foreground">
        We sent a verification link to{" "}
        <span className="font-medium text-foreground">{user.email}</span>. Verifying secures
        your account — and if your company has verified its domain, it adds you to your
        team&apos;s workspace automatically.
      </div>
      <Button
        size="sm"
        variant="outline"
        className="shrink-0"
        disabled={sending}
        onClick={() => void resend()}
        data-testid="onboarding-verify-email-resend"
      >
        Resend email
      </Button>
    </div>
  );
}

export function OnboardingPageContent() {
  const router = useRouter();
  const { isLoading, isSetupComplete } = useSetupStatus();

  useEffect(() => {
    if (!isLoading && isSetupComplete) {
      router.replace("/sessions");
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

        <VerifyEmailBanner />

        <p className="text-sm text-muted-foreground">
          Autopilot needs a few connections before it can start analyzing.
          Connect a coding agent and GitHub repositories, then run the first analysis.
        </p>

        <SetupChecklist />
      </div>
    </PageContainer>
  );
}
