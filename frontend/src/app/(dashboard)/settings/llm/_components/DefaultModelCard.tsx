"use client";

import { AlertTriangle, Check } from "lucide-react";
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

export interface DefaultModelCardProps {
  value: string;
  reasoningEffort: string;
  modelGroups: { label: string; models: readonly string[] }[];
  ownerProvider: string | null;
  ownerProviderInfo?: { name: string } | null;
  ownerConfigured: boolean;
  ownerUsesPlatformDefault?: boolean;
  ownerHasModelRestriction?: boolean;
  onChange: (model: string) => void;
  onReasoningChange: (v: string) => void;
}

export function DefaultModelCard({
  value,
  reasoningEffort,
  modelGroups,
  ownerProvider,
  ownerProviderInfo,
  ownerConfigured,
  ownerUsesPlatformDefault = false,
  ownerHasModelRestriction = false,
  onChange,
  onReasoningChange,
}: DefaultModelCardProps) {
  const hasModels = modelGroups.length > 0;
  const ownerName = ownerProviderInfo?.name;

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
            <OwnerCaption
              ownerName={ownerProvider && ownerConfigured ? ownerName : null}
              usesPlatformDefault={ownerUsesPlatformDefault}
            />
            <p className="text-xs text-muted-foreground">
              Used for organization-level LLM features, separate from the coding agents configured
              on the Agent settings page.
              {ownerUsesPlatformDefault && ownerHasModelRestriction
                ? " 143's default key is capped at lower-cost models."
                : ""}
            </p>
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
        </div>
      </CardContent>
    </Card>
  );
}

interface OwnerCaptionProps {
  // null when no provider is configured for the current model. Otherwise the
  // provider's display name.
  ownerName: string | null | undefined;
  usesPlatformDefault: boolean;
}

function OwnerCaption({ ownerName, usesPlatformDefault }: OwnerCaptionProps) {
  if (!ownerName) {
    return (
      <p className="flex items-center gap-1.5 text-xs text-warning">
        <AlertTriangle className="h-3 w-3" />
        No provider key configured for this model
      </p>
    );
  }
  return (
    <p className="flex items-center gap-1.5 text-xs text-success">
      <Check className="h-3 w-3" />
      {usesPlatformDefault
        ? `Using 143's default ${ownerName} key`
        : `Uses your ${ownerName} key`}
    </p>
  );
}
