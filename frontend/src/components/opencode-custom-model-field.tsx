"use client";

import { useState } from "react";
import { ChevronDown, CircleHelp } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";

type OpenCodeCustomModelFieldProps = {
  id: string;
  value: string;
  onChange: (value: string) => void;
};

export function OpenCodeCustomModelField({ id, value, onChange }: OpenCodeCustomModelFieldProps) {
  const [open, setOpen] = useState(false);

  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      <CollapsibleTrigger asChild>
        <Button type="button" variant="outline" size="sm" className="w-fit">
          <ChevronDown className={`h-4 w-4 transition-transform ${open ? "rotate-180" : ""}`} />
          Advanced OpenCode model
        </Button>
      </CollapsibleTrigger>
      <CollapsibleContent className="mt-4">
        <div className="space-y-2 rounded-md border border-border bg-muted/20 p-3">
          <div className="flex items-center gap-2">
            <Label htmlFor={id}>Custom model override</Label>
            <TooltipProvider delayDuration={150}>
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    className="h-5 w-5 rounded-full text-muted-foreground hover:text-foreground"
                    aria-label="What custom OpenCode model override does"
                  >
                    <CircleHelp className="h-3.5 w-3.5" />
                  </Button>
                </TooltipTrigger>
                <TooltipContent side="top" sideOffset={6} className="max-w-80 space-y-1.5">
                  <p>Use this only when the model you need is not in the default list.</p>
                  <p>When set, this provider/model id wins over the selected default model for this OpenCode auth.</p>
                </TooltipContent>
              </Tooltip>
            </TooltipProvider>
          </div>
          <Input
            id={id}
            value={value}
            onChange={(event) => onChange(event.target.value)}
            placeholder="provider/model (e.g. xai/grok-code-fast)"
          />
          <p className="text-xs text-muted-foreground">
            The provider prefix must match how this key can run the model, such as openrouter, openai, anthropic, google, or opencode.
          </p>
        </div>
      </CollapsibleContent>
    </Collapsible>
  );
}
