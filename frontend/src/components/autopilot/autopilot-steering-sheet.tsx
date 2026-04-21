"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { AutosaveIndicator } from "@/components/AutosaveIndicator";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { queryKeys } from "@/lib/query-keys";
import { useAutosave } from "@/hooks/useAutosave";
import { useDebouncedTextField } from "@/hooks/useDebouncedTextField";
import { applyOrgSettingsPatch, coalesceSettingsPatch, type SettingsPatch } from "@/lib/settings-autosave";
import type { OrgSettings } from "@/lib/types";

const TEXT_DEBOUNCE_MS = 400;

function parseTagList(value: string): string[] {
  return value
    .split(",")
    .map((entry) => entry.trim())
    .filter(Boolean);
}

export function AutopilotSteeringSheet({
  open,
  onOpenChange,
  settings,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  settings: OrgSettings;
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      {open && (
        <AutopilotSteeringSheetBody
          onOpenChange={onOpenChange}
          settings={settings}
        />
      )}
    </Sheet>
  );
}

function AutopilotSteeringSheetBody({
  onOpenChange,
  settings,
}: {
  onOpenChange: (open: boolean) => void;
  settings: OrgSettings;
}) {
  const serverPhilosophy = settings.product_context?.philosophy ?? "";
  const serverDirection =
    settings.product_context?.direction ?? settings.product_direction ?? "";
  const serverFocusAreas = (settings.product_context?.focus_areas ?? []).join(", ");
  const serverAvoidAreas = (settings.product_context?.avoid_areas ?? []).join(", ");
  const serverAutonomy: NonNullable<OrgSettings["autonomy_level"]> =
    settings.autonomy_level ?? "auto_simple";

  const autosave = useAutosave<SettingsPatch>({
    queryKey: queryKeys.settings.all,
    mutationFn: (payload) => api.settings.update(payload),
    applyOptimistic: applyOrgSettingsPatch,
    coalesce: coalesceSettingsPatch,
  });

  // "Done" must not drop the sheet mid-save: flushing the debounce only
  // dispatches the request, it doesn't wait for the server. Track a
  // pending-close intent and close once the autosave queue leaves "saving".
  const [pendingClose, setPendingClose] = useState(false);
  useEffect(() => {
    if (pendingClose && autosave.status !== "saving") {
      onOpenChange(false);
    }
  }, [pendingClose, autosave.status, onOpenChange]);

  // Build a product_context patch by merging the proposed field change against
  // the currently displayed values. The server shallow-merges at the top-level
  // `product_context` key, so sending a partial object would wipe siblings.
  const saveProductContext = (patch: {
    philosophy?: string;
    direction?: string;
    focus_areas?: string[];
    avoid_areas?: string[];
  }) => {
    const next = {
      philosophy: patch.philosophy ?? serverPhilosophy,
      direction: patch.direction ?? serverDirection,
      focus_areas: patch.focus_areas ?? parseTagList(serverFocusAreas),
      avoid_areas: patch.avoid_areas ?? parseTagList(serverAvoidAreas),
    };
    const payload: SettingsPatch = {
      settings: {
        product_context: next,
        // Mirror `direction` to the legacy `product_direction` for consumers
        // that still read it.
        ...(patch.direction !== undefined ? { product_direction: patch.direction } : {}),
      },
    };
    autosave.save(payload);
  };

  return (
    <SheetContent className="w-full sm:max-w-xl">
      <SheetHeader>
        <div className="flex items-center justify-between">
          <div>
            <SheetTitle>Edit direction</SheetTitle>
            <SheetDescription>
              Adjust the product context that guides Autopilot recommendations.
            </SheetDescription>
          </div>
          <AutosaveIndicator status={autosave.status} />
        </div>
      </SheetHeader>
      <div className="mt-6 space-y-5">
        <div className="space-y-2">
          <Label htmlFor="autopilot-philosophy">Philosophy</Label>
          <DebouncedTextarea
            id="autopilot-philosophy"
            rows={4}
            serverValue={serverPhilosophy}
            onCommit={(value) => saveProductContext({ philosophy: value })}
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="autopilot-direction">Current direction</Label>
          <DebouncedTextarea
            id="autopilot-direction"
            rows={3}
            serverValue={serverDirection}
            onCommit={(value) => saveProductContext({ direction: value })}
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="autopilot-focus-areas">Focus areas</Label>
          <DebouncedInput
            id="autopilot-focus-areas"
            serverValue={serverFocusAreas}
            placeholder="auth, incidents, checkout"
            onCommit={(value) => saveProductContext({ focus_areas: parseTagList(value) })}
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="autopilot-avoid-areas">Avoid areas</Label>
          <DebouncedInput
            id="autopilot-avoid-areas"
            serverValue={serverAvoidAreas}
            placeholder="redesigns, polish"
            onCommit={(value) => saveProductContext({ avoid_areas: parseTagList(value) })}
          />
        </div>
        <div className="space-y-3">
          <Label>Autonomy level</Label>
          <RadioGroup
            value={serverAutonomy}
            onValueChange={(value) =>
              autosave.save({
                settings: {
                  autonomy_level: value as NonNullable<OrgSettings["autonomy_level"]>,
                },
              })
            }
          >
            <label className="flex items-center gap-3 rounded-lg border p-3">
              <RadioGroupItem value="manual" aria-label="Suggest" />
              <div>
                <p className="text-sm font-medium">Suggest</p>
                <p className="text-xs text-muted-foreground">Autopilot recommends, you decide.</p>
              </div>
            </label>
            <label className="flex items-center gap-3 rounded-lg border p-3">
              <RadioGroupItem value="auto_simple" aria-label="Act on low-risk" />
              <div>
                <p className="text-sm font-medium">Act on low-risk</p>
                <p className="text-xs text-muted-foreground">Auto-create sessions for bounded work.</p>
              </div>
            </label>
            <label className="flex items-center gap-3 rounded-lg border p-3">
              <RadioGroupItem value="auto_all" aria-label="Operate broadly" />
              <div>
                <p className="text-sm font-medium">Operate broadly</p>
                <p className="text-xs text-muted-foreground">Autopilot runs automatically on eligible work.</p>
              </div>
            </label>
          </RadioGroup>
        </div>
        <div className="flex justify-end">
          <Button
            variant="outline"
            disabled={pendingClose && autosave.status === "saving"}
            onClick={() => {
              autosave.flush();
              setPendingClose(true);
            }}
          >
            {pendingClose && autosave.status === "saving" ? "Saving…" : "Done"}
          </Button>
        </div>
      </div>
    </SheetContent>
  );
}

interface DebouncedTextareaProps {
  id: string;
  rows: number;
  placeholder?: string;
  serverValue: string;
  onCommit: (value: string) => void;
}

function DebouncedTextarea({ id, rows, placeholder, serverValue, onCommit }: DebouncedTextareaProps) {
  const field = useDebouncedTextField({
    serverValue,
    onCommit,
    debounceMs: TEXT_DEBOUNCE_MS,
  });
  return (
    <Textarea
      id={id}
      rows={rows}
      placeholder={placeholder}
      value={field.value}
      onChange={(event) => field.onChange(event.target.value)}
      onBlur={field.onBlur}
    />
  );
}

interface DebouncedInputProps {
  id: string;
  placeholder?: string;
  serverValue: string;
  onCommit: (value: string) => void;
}

function DebouncedInput({ id, placeholder, serverValue, onCommit }: DebouncedInputProps) {
  const field = useDebouncedTextField({
    serverValue,
    onCommit,
    debounceMs: TEXT_DEBOUNCE_MS,
  });
  return (
    <Input
      id={id}
      placeholder={placeholder}
      value={field.value}
      onChange={(event) => field.onChange(event.target.value)}
      onBlur={field.onBlur}
    />
  );
}
