"use client";

import { SettingsPageFrame } from "@/components/settings-page-frame";

export default function SettingsLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <SettingsPageFrame
      title="General Settings"
      description="Manage your organization."
    >
      {children}
    </SettingsPageFrame>
  );
}
