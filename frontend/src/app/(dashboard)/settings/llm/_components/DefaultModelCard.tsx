"use client";

import { AlertTriangle, Check } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

export type DefaultModelSaveStatus = "idle" | "success" | "error";

export interface DefaultModelCardProps {
  value: string;
  reasoningEffort: string;
  modelGroups: { label: string; models: readonly string[] }[];
  ownerProvider: string | null;
  ownerProviderInfo?: { name: string } | null;
  ownerConfigured: boolean;
  saving: boolean;
  saveStatus: DefaultModelSaveStatus;
  onChange: (model: string) => void;
  onReasoningChange: (v: string) => void;
  onSave: () => void;
}

export function DefaultModelCard({
  value,
  reasoningEffort,
  modelGroups,
  ownerProvider,
  ownerProviderInfo,
  ownerConfigured,
  saving,
  saveStatus,
  onChange,
  onReasoningChange,
  onSave,
}: DefaultModelCardProps) {
  const hasModels = modelGroups.length > 0;

  return (
    <Card>
      <CardContent>
        <div className="space-y-3">
          <div className="space-y-2">
            <Label htmlFor="llm-model">Default model</Label>
            <Select value={value} onValueChange={onChange} disabled={!hasModels}>
              <SelectTrigger id="llm-model" aria-label="LLM Model">
                <SelectValue placeholder="Select a model" />
              </SelectTrigger>
              <SelectContent>
                {!hasModels ? (
                  <SelectItem value="__no_providers__" disabled>
                    No providers configured
                  </SelectItem>
                ) : (
                  modelGroups.map((group) => (
                    <SelectGroup key={group.label}>
                      <SelectLabel>{group.label}</SelectLabel>
                      {group.models.map((model) => (
                        <SelectItem key={model} value={model}>
                          {model}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  ))
                )}
              </SelectContent>
            </Select>
            {ownerProvider && ownerProviderInfo && ownerConfigured ? (
              <p className="flex items-center gap-1.5 text-xs text-emerald-600 dark:text-emerald-400">
                <Check className="h-3 w-3" />
                Uses your {ownerProviderInfo.name} key
              </p>
            ) : (
              <p className="flex items-center gap-1.5 text-xs text-amber-600 dark:text-amber-400">
                <AlertTriangle className="h-3 w-3" />
                No provider key configured for this model
              </p>
            )}
          </div>

          <div className="space-y-2">
            <Label htmlFor="reasoning-effort">Reasoning effort</Label>
            <Select
              value={reasoningEffort || "none"}
              onValueChange={(v) => onReasoningChange(v === "none" ? "" : v)}
            >
              <SelectTrigger id="reasoning-effort" aria-label="Reasoning effort">
                <SelectValue placeholder="Default (none)" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="none">Default (none)</SelectItem>
                <SelectItem value="low">Low</SelectItem>
                <SelectItem value="medium">Medium</SelectItem>
                <SelectItem value="high">High</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <div className="flex items-center justify-end gap-3 pt-1">
            {saveStatus === "success" && (
              <span className="text-xs text-emerald-600 dark:text-emerald-400">Model saved.</span>
            )}
            {saveStatus === "error" && (
              <span className="text-xs text-destructive">Failed to save model.</span>
            )}
            <Button
              onClick={onSave}
              disabled={saving || !ownerConfigured}
              aria-label="Save default model"
            >
              {saving ? "Saving..." : "Save"}
            </Button>
          </div>
        </div>
      </CardContent>
    </Card>
  );
}
