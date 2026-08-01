import type { Metadata } from "next";

import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { ControlGallery } from "@/components/ui/control-gallery";

export const metadata: Metadata = {
  title: "Control gallery",
  robots: { index: false, follow: false },
};

export default function DesignSystemPage() {
  return (
    <PageContainer size="wide">
      <div className="space-y-6">
        <PageHeader
          title="Control gallery"
          description="Internal reference for responsive control densities and cross-component alignment."
        />
        <ControlGallery />
      </div>
    </PageContainer>
  );
}
