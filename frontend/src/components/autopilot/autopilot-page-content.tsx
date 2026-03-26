"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { AutopilotControlStrip } from "./autopilot-control-strip";
import { AutopilotDirectionSummary } from "./autopilot-direction-summary";
import { AutopilotEvidenceRow } from "./autopilot-evidence-row";
import { AutopilotHero } from "./autopilot-hero";
import { AutopilotSetupChecklist } from "./autopilot-setup-checklist";
import { useAutopilotPageData } from "./use-autopilot-page-data";
import { DEFAULT_PRIORITY_WEIGHTS } from "./autopilot-helpers";
import { useAnalyze } from "@/hooks/use-analyze";
import { AutopilotSteeringSheet } from "./autopilot-steering-sheet";
import { AutopilotWeightsSheet } from "./autopilot-weights-sheet";
import { AutopilotDocumentsSheet } from "./autopilot-documents-sheet";

export function AutopilotPageContent() {
  const router = useRouter();
  const [showDirectionEditor, setShowDirectionEditor] = useState(false);
  const [showDocumentsEditor, setShowDocumentsEditor] = useState(false);
  const [showWeightsEditor, setShowWeightsEditor] = useState(false);
  const { isLoading, pmStatus, setup, settings, viewModel } = useAutopilotPageData();
  const { handleAnalyze, isAnalyzing, isPending } = useAnalyze(pmStatus.is_running);

  const secondaryText = viewModel.heroMode === "setup"
    ? `${setup.connectedCount} of ${setup.totalCount} connected`
    : pmStatus.last_run_at
      ? `${viewModel.autonomyLabel} · Last analyzed ${new Date(pmStatus.last_run_at).toLocaleString()}`
      : `${viewModel.autonomyLabel} · No analysis yet`;

  function handlePrimaryAction() {
    if (viewModel.heroMode === "setup") {
      document.getElementById("autopilot-setup")?.scrollIntoView({ behavior: "smooth", block: "start" });
      return;
    }
    handleAnalyze();
  }

  return (
    <PageContainer size="default">
      <div className="space-y-8">
        <PageHeader
          title="Autopilot"
          description="Autopilot helps you understand what matters, what needs attention, and how to steer the PM agent."
        />

        {isLoading ? (
          <p className="text-sm text-muted-foreground">Loading Autopilot...</p>
        ) : (
          <>
            <AutopilotControlStrip
              autonomyLabel={viewModel.autonomyLabel}
              secondaryText={secondaryText}
              primaryActionLabel={isAnalyzing || isPending ? "Running..." : viewModel.primaryActionLabel}
              onPrimaryAction={handlePrimaryAction}
            />

            <AutopilotHero title={viewModel.heroTitle} body={viewModel.heroBody} />
            <AutopilotEvidenceRow evidence={viewModel.evidence} />
            {viewModel.heroMode === "setup" && <AutopilotSetupChecklist />}

            <AutopilotDirectionSummary
              philosophySummary={viewModel.philosophySummary}
              directionSummary={viewModel.directionSummary}
              focusAreas={viewModel.focusAreas}
              avoidAreas={viewModel.avoidAreas}
              autonomyLabel={viewModel.autonomyLabel}
              documentsSummary={viewModel.documentsSummary}
              weightsSummary={viewModel.weightsSummary}
              onEditDirection={() => setShowDirectionEditor(true)}
              onManageDocuments={() => setShowDocumentsEditor(true)}
              onCustomizeWeights={() => setShowWeightsEditor(true)}
              onOpenSettings={() => router.push("/settings/autopilot")}
            />

            <AutopilotSteeringSheet
              open={showDirectionEditor}
              onOpenChange={setShowDirectionEditor}
              settings={settings}
            />
            <AutopilotWeightsSheet
              open={showWeightsEditor}
              onOpenChange={setShowWeightsEditor}
              weights={settings.priority_weights ?? DEFAULT_PRIORITY_WEIGHTS}
            />
            <AutopilotDocumentsSheet
              open={showDocumentsEditor}
              onOpenChange={setShowDocumentsEditor}
            />
          </>
        )}
      </div>
    </PageContainer>
  );
}
