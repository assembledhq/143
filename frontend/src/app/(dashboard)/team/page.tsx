import TeamSettingsPage from "../settings/team/page";
import { PageHeader } from "@/components/page-header";

export default function TeamPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Team"
        description="Manage members, roles, and invitations for your organization."
      />
      <TeamSettingsPage />
    </div>
  );
}
