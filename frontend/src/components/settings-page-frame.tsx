import type { ReactNode } from "react";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";

interface SettingsPageFrameProps {
  title: string;
  description: string;
  children: ReactNode;
}

export function SettingsPageFrame({ title, description, children }: SettingsPageFrameProps) {
  return (
    <PageContainer size="narrow">
      <div className="space-y-6">
        <PageHeader title={title} description={description} />
        {children}
      </div>
    </PageContainer>
  );
}
