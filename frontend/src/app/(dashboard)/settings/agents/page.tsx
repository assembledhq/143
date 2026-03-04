"use client";

import { Card, CardContent } from "@/components/ui/card";
import { AgentSettingsEditor } from "@/components/agent-settings-editor";

export default function AgentSettingsPage() {
  return (
    <section className="space-y-3">
      <h2 className="text-[13px] font-medium text-foreground">Agent Setup</h2>
      <Card>
        <CardContent>
          <AgentSettingsEditor
            title="Advanced agent settings"
            description="Configure your default agent and provider credentials for your organization."
          />
        </CardContent>
      </Card>
    </section>
  );
}
