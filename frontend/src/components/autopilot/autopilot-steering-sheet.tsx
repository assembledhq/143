"use client";

import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
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
import type { OrgSettings } from "@/lib/types";

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
          key={JSON.stringify(settings)}
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
  const queryClient = useQueryClient();
  const [philosophy, setPhilosophy] = useState(settings.product_context?.philosophy ?? "");
  const [direction, setDirection] = useState(settings.product_context?.direction ?? settings.product_direction ?? "");
  const [focusAreas, setFocusAreas] = useState((settings.product_context?.focus_areas ?? []).join(", "));
  const [avoidAreas, setAvoidAreas] = useState((settings.product_context?.avoid_areas ?? []).join(", "));
  const [autonomyLevel, setAutonomyLevel] = useState<NonNullable<OrgSettings["autonomy_level"]>>(settings.autonomy_level ?? "auto_simple");
  const [saveError, setSaveError] = useState<string | null>(null);

  const mutation = useMutation({
    mutationFn: (payload: Record<string, unknown>) => api.settings.update(payload),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.settings.all });
      onOpenChange(false);
    },
    onError: (error) => {
      console.error("failed to save autopilot steering", error);
      setSaveError("Failed to save changes.");
    },
  });

  function handleSave() {
    mutation.mutate({
      settings: {
        autonomy_level: autonomyLevel,
        product_direction: direction,
        product_context: {
          philosophy,
          direction,
          focus_areas: parseTagList(focusAreas),
          avoid_areas: parseTagList(avoidAreas),
        },
      },
    });
  }

  return (
    <SheetContent className="w-full sm:max-w-xl">
      <SheetHeader>
        <SheetTitle>Edit direction</SheetTitle>
        <SheetDescription>Adjust the product context that guides Autopilot recommendations.</SheetDescription>
      </SheetHeader>
      <div className="mt-6 space-y-5">
        <div className="space-y-2">
          <Label htmlFor="autopilot-philosophy">Philosophy</Label>
          <Textarea
            id="autopilot-philosophy"
            rows={4}
            value={philosophy}
            onChange={(event) => setPhilosophy(event.target.value)}
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="autopilot-direction">Current direction</Label>
          <Textarea
            id="autopilot-direction"
            rows={3}
            value={direction}
            onChange={(event) => setDirection(event.target.value)}
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="autopilot-focus-areas">Focus areas</Label>
          <Input
            id="autopilot-focus-areas"
            value={focusAreas}
            onChange={(event) => setFocusAreas(event.target.value)}
            placeholder="auth, incidents, checkout"
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="autopilot-avoid-areas">Avoid areas</Label>
          <Input
            id="autopilot-avoid-areas"
            value={avoidAreas}
            onChange={(event) => setAvoidAreas(event.target.value)}
            placeholder="redesigns, polish"
          />
        </div>
        <div className="space-y-3">
          <Label>Autonomy level</Label>
          <RadioGroup value={autonomyLevel} onValueChange={(value) => setAutonomyLevel(value as NonNullable<OrgSettings["autonomy_level"]>)}>
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
        {saveError && <p className="text-sm text-destructive">{saveError}</p>}
        <div className="flex justify-end gap-2">
          <Button variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button onClick={handleSave} disabled={mutation.isPending}>
            {mutation.isPending ? "Saving..." : "Save"}
          </Button>
        </div>
      </div>
    </SheetContent>
  );
}
