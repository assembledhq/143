"use client";

import { AgentSettingsEditor } from "@/components/agent-settings-editor";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import type { OrgSettings } from "@/lib/types";

interface AgentSettingsModalProps {
  onClose: () => void;
  initialAgentType?: OrgSettings["default_agent_type"];
}

export function AgentSettingsModal({ onClose, initialAgentType }: AgentSettingsModalProps) {
  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent>
        <DialogTitle className="sr-only">Configure coding agent</DialogTitle>
        <DialogDescription className="sr-only">
          Set your default agent and configure credentials.
        </DialogDescription>
        <AgentSettingsEditor
          title="Configure coding agent"
          description="Set your default agent and configure credentials."
          initialAgentType={initialAgentType}
          setupMode
          onClose={onClose}
        />
      </DialogContent>
    </Dialog>
  );
}
