import TeamSettingsPage from "../settings/team/page";
import { SettingsPageFrame } from "@/components/settings-page-frame";

export default function TeamPage() {
  return (
    <SettingsPageFrame
      title="Team"
      description="Manage members, roles, and invitations for your organization."
    >
      <TeamSettingsPage />
    </SettingsPageFrame>
  );
}
