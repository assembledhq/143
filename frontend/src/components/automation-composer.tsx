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
    <div
      data-testid="automation-composer"
      className="overflow-hidden rounded-xl border border-border bg-card shadow-sm"
    >
      <div
        data-testid="automation-identity-row"
        className="flex items-start gap-3 px-4 pb-2 pt-5 sm:px-6"
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
          placeholder="Untitled automation"
          className="h-auto min-h-10 rounded-none border-0 bg-transparent px-0 py-0 text-2xl font-semibold shadow-none placeholder:text-muted-foreground/60 focus-visible:ring-0 sm:text-2xl"
        />
      </div>

      <div ref={goalEditorContainerRef} className="space-y-2 px-4 pb-4 pt-1 sm:px-6">
        <Label htmlFor="goal" className="sr-only">Goal</Label>
        <AutomationGoalEditor
          id="goal"
          value={goal}
          onChange={onGoalChange}
          repositoryId={repositoryId}
          branch={branch}
          agentType={agentType}
          placeholder="Describe the recurring work this automation should handle..."
          rows={11}
          ariaInvalid={goalLength.isTooLong}
          className="min-h-[260px] resize-y border-0 bg-transparent px-0 py-1 text-sm shadow-none focus-visible:ring-0"
        />
        <div className="flex min-h-5 items-center justify-between gap-3">
          <p className={cn("text-xs", goalLength.isTooLong ? "text-destructive" : "text-muted-foreground")}>
            {goalLength.message ?? `Up to ${AUTOMATION_GOAL_MAX_LENGTH.toLocaleString("en-US")} characters.`}
          </p>
          <span
            className={cn(
              "text-xs tabular-nums",
              goalLength.isTooLong ? "text-destructive" : "text-muted-foreground",
            )}
          >
            {goalLength.countText}
          </span>
        </div>
      </div>

      <div className="flex flex-col gap-3 border-t border-border px-4 py-3 sm:px-6">
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
