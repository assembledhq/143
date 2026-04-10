"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { AutopilotConfigFooter } from "./autopilot-config-footer";
import { AutopilotEvidenceRow } from "./autopilot-evidence-row";
import { useAutopilotPageData } from "./use-autopilot-page-data";
import { useAnalyze } from "@/hooks/use-analyze";
import { AutopilotSteeringSheet } from "./autopilot-steering-sheet";
import { AutopilotDocumentsSheet } from "./autopilot-documents-sheet";
import { AutopilotProposalCard } from "@/components/autopilot-proposal-card";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { ArrowRight, ExternalLink } from "lucide-react";
import Link from "next/link";

export function AutopilotPageContent() {
  const router = useRouter();
  const [showDirectionEditor, setShowDirectionEditor] = useState(false);
  const [showDocumentsEditor, setShowDocumentsEditor] = useState(false);
  const { isLoading, isSetupComplete, pmStatus, settings, viewModel } = useAutopilotPageData();
  const { handleAnalyze, isAnalyzing, isPending } = useAnalyze(pmStatus.is_running);

  // Redirect to onboarding if setup is not complete
  useEffect(() => {
    if (!isLoading && !isSetupComplete) {
      router.replace("/onboarding");
    }
  }, [isLoading, isSetupComplete, router]);

  if (isLoading || !isSetupComplete) {
    return (
      <PageContainer size="default">
        <p className="text-sm text-muted-foreground">Loading Autopilot...</p>
      </PageContainer>
    );
  }

  const isFirstAnalysis = viewModel.heroMode === "first_analysis";
  const isAttention = viewModel.heroMode === "attention";

  return (
    <PageContainer size="default">
      <div className="space-y-8">
        {/* Page header: title + status subtitle + CTA */}
        <PageHeader
          title="Autopilot"
          subtitle={viewModel.statusLine}
          action={
            <Button
              onClick={handleAnalyze}
              disabled={isAnalyzing || isPending}
            >
              {isAnalyzing || isPending ? "Running..." : viewModel.primaryActionLabel}
            </Button>
          }
        />

        {/* State 1: First analysis — single card + direction nudge */}
        {isFirstAnalysis && (
          <div className="space-y-4">
            <Card className="border-border/70">
              <CardContent className="py-8">
                <h2 className="text-lg font-semibold text-foreground">
                  {viewModel.heroTitle}
                </h2>
                <p className="mt-2 max-w-2xl text-sm leading-relaxed text-muted-foreground">
                  {viewModel.heroBody}
                </p>
              </CardContent>
            </Card>
            {!viewModel.directionSummary && (
              <button
                onClick={() => setShowDirectionEditor(true)}
                className="flex w-full items-center justify-between rounded-lg px-1 py-2 text-sm text-muted-foreground transition-colors hover:text-foreground"
              >
                <span>Set your product direction for better results.</span>
                <ArrowRight className="h-4 w-4" />
              </button>
            )}
          </div>
        )}

        {/* State 2 & 3: Active / Attention — headline + brief */}
        {!isFirstAnalysis && (
          <div className="space-y-1">
            <h2 className={`text-lg font-semibold ${isAttention ? "text-amber-600 dark:text-amber-400" : "text-foreground"}`}>
              {isAttention && "\u26A0 "}{viewModel.heroTitle}
            </h2>
            {viewModel.heroBody && (
              <p className="max-w-3xl text-sm leading-relaxed text-muted-foreground">
                {viewModel.heroBody}
              </p>
            )}
            {isAttention && pmStatus.last_failed_session_id && (
              <Link
                href={`/sessions/${pmStatus.last_failed_session_id}`}
                className="inline-flex items-center gap-1 text-sm text-primary hover:underline mt-1"
              >
                <ExternalLink className="h-3 w-3" />
                View agent logs
              </Link>
            )}
          </div>
        )}

        {/* Evidence row — hidden when all zeros */}
        {viewModel.hasEvidence && (
          <AutopilotEvidenceRow evidence={viewModel.evidence} />
        )}

        {/* Proposals — conditional, only when proposals exist */}
        <AutopilotProposalCard />

        {/* Config footer — quiet, below a separator */}
        <AutopilotConfigFooter
          directionSummary={viewModel.directionSummary}
          focusAreas={viewModel.focusAreas}
          documentsSummary={viewModel.documentsSummary}
          weightsSummary={viewModel.weightsSummary}
          onEditDirection={() => setShowDirectionEditor(true)}
          onManageDocuments={() => setShowDocumentsEditor(true)}
          onOpenSettings={() => router.push("/settings/autopilot")}
        />

        {/* Side sheets — no visual change */}
        <AutopilotSteeringSheet
          open={showDirectionEditor}
          onOpenChange={setShowDirectionEditor}
          settings={settings}
        />
        <AutopilotDocumentsSheet
          open={showDocumentsEditor}
          onOpenChange={setShowDocumentsEditor}
        />
      </div>
    </PageContainer>
  );
}
