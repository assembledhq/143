"use client";

import { ChevronDown } from "lucide-react";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Command, CommandInput } from "@/components/ui/command";
import {
  controlDensities,
  type ControlDensity,
} from "@/components/ui/control-sizing";
import { ControlTrigger } from "@/components/ui/control-trigger";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

const densityLabels: Record<ControlDensity, string> = {
  default: "Default",
  compact: "Compact",
  dense: "Dense",
};

function GalleryField({
  label,
  htmlFor,
  children,
}: {
  label: string;
  htmlFor: string;
  children: React.ReactNode;
}) {
  return (
    <div className="min-w-0 space-y-2">
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
    </div>
  );
}

export function ControlGallery() {
  return (
    <div data-slot="control-gallery" className="space-y-4">
      {controlDensities.map((density) => {
        const label = densityLabels[density];
        const inputId = `${density}-gallery-input`;
        const selectId = `${density}-gallery-select`;
        const commandId = `${density}-gallery-command`;
        const pickerId = `${density}-gallery-picker`;

        return (
          <Card key={density} data-density={density}>
            <CardHeader>
              <CardTitle>{label} controls</CardTitle>
            </CardHeader>
            <CardContent className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
              <GalleryField label="Text input" htmlFor={inputId}>
                <Input
                  id={inputId}
                  density={density}
                  aria-label={`${label} text input`}
                  placeholder={`${label} input`}
                />
              </GalleryField>

              <GalleryField label="Select" htmlFor={selectId}>
                <Select defaultValue="alpha">
                  <SelectTrigger id={selectId} density={density} aria-label={`${label} select`}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="alpha">Alpha</SelectItem>
                    <SelectItem value="beta">Beta</SelectItem>
                  </SelectContent>
                </Select>
              </GalleryField>

              <GalleryField label="Search command" htmlFor={commandId}>
                <Command className="h-auto border border-border">
                  <CommandInput
                    id={commandId}
                    density={density}
                    aria-label={`${label} command search`}
                  />
                </Command>
              </GalleryField>

              <GalleryField label="Button-based picker" htmlFor={pickerId}>
                <ControlTrigger
                  id={pickerId}
                  type="button"
                  role="combobox"
                  variant="outline"
                  density={density}
                  aria-label={`${label} custom picker`}
                  className="w-full justify-between font-normal"
                >
                  Pick an option
                  <ChevronDown className="size-4 text-muted-foreground" />
                </ControlTrigger>
              </GalleryField>
            </CardContent>
          </Card>
        );
      })}
    </div>
  );
}
