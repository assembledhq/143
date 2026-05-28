"use client";

import type { ReactNode } from "react";
import { AutomationGoalEditor } from "@/components/automation-goal-editor";
import { AutomationEmojiPicker } from "@/components/automation-emoji-picker";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { AUTOMATION_GOAL_MAX_LENGTH, automationGoalLengthState } from "@/lib/automation-validation";
import { cn } from "@/lib/utils";

interface AutomationComposerProps {
  name: string;
  onNameChange: (value: string) => void;
  iconValue: string;
  onIconChange: (value: string) => void;
  goal: string;
  onGoalChange: (value: string) => void;
  repositoryId?: string;
  branch?: string;
  agentType: string;
  footerControls: ReactNode;
  secondaryControls: ReactNode;
  submitArea: ReactNode;
  goalEditorContainerRef?: React.RefObject<HTMLDivElement | null>;
}

export function AutomationComposer({
  name,
  onNameChange,
  iconValue,
  onIconChange,
  goal,
  onGoalChange,
  repositoryId,
  branch,
  agentType,
  footerControls,
  secondaryControls,
  submitArea,
  goalEditorContainerRef,
}: AutomationComposerProps) {
  const goalLength = automationGoalLengthState(goal);

  return (
    <div className="overflow-hidden rounded-lg border border-border bg-surface-raised shadow-sm">
      <div
        data-testid="automation-identity-row"
        className="grid grid-cols-[4.75rem_minmax(0,1fr)] items-center gap-3 border-b border-border px-4 py-3 sm:px-5"
      >
        <AutomationEmojiPicker
          value={iconValue}
          onChange={onIconChange}
          className="shrink-0"
        />
        <Input
          id="name"
          aria-label="Name"
          value={name}
          onChange={(e) => onNameChange(e.target.value)}
          placeholder="Name this automation"
          className="border-0 bg-transparent px-0 text-base font-medium shadow-none focus-visible:ring-0 sm:text-base"
        />
      </div>

      <div ref={goalEditorContainerRef} className="space-y-2 px-4 py-4 sm:px-5">
        <div className="flex items-center justify-between gap-3">
          <Label htmlFor="goal" className="sr-only">Goal</Label>
          <span className="text-xs font-medium uppercase tracking-wide text-muted-foreground">Goal</span>
          <span
            className={cn(
              "text-xs tabular-nums",
              goalLength.isTooLong ? "text-destructive" : "text-muted-foreground",
            )}
          >
            {goalLength.countText}
          </span>
        </div>
        <AutomationGoalEditor
          id="goal"
          value={goal}
          onChange={onGoalChange}
          repositoryId={repositoryId}
          branch={branch}
          agentType={agentType}
          placeholder="Describe what this automation should do every run..."
          rows={11}
          ariaInvalid={goalLength.isTooLong}
          className="min-h-[280px] resize-y border-0 bg-transparent px-0 text-base shadow-none focus-visible:ring-0 sm:text-sm"
        />
        <p className={cn("text-xs", goalLength.isTooLong ? "text-destructive" : "text-muted-foreground")}>
          {goalLength.message ?? `Up to ${AUTOMATION_GOAL_MAX_LENGTH.toLocaleString("en-US")} characters.`}
        </p>
      </div>

      <div className="flex flex-col gap-3 border-t border-border bg-muted/15 px-4 py-3 sm:px-5">
        <div className="flex flex-wrap items-center gap-2">
          {footerControls}
        </div>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex flex-wrap items-center gap-2">
            {secondaryControls}
          </div>
          {submitArea}
        </div>
      </div>
    </div>
  );
}
