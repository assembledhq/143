"use client";

import { PageHeader } from "@/components/page-header";

export default function SettingsLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-6">
      <PageHeader
        title="General Settings"
        description="Manage your organization."
      />
      {children}
    </div>
  );
}
