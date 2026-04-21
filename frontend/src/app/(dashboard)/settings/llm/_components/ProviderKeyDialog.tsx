"use client";

import { useEffect, useLayoutEffect, useRef, useState } from "react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { PasswordField } from "./PasswordField";

export type SaveStatus = "idle" | "saving" | "success" | "error";

export interface ProviderKeyDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  info: { name: string; description: string; keyPlaceholder: string };
  existingMaskedKey?: string;
  saveStatus: SaveStatus;
  errorMessage?: string;
  onSave: (key: string) => void;
  onRemove?: () => void;
}

export function ProviderKeyDialog({
  open,
  onOpenChange,
  info,
  existingMaskedKey,
  saveStatus,
  errorMessage,
  onSave,
  onRemove,
}: ProviderKeyDialogProps) {
  const [draftKey, setDraftKey] = useState("");

  // Keep onOpenChange in a ref so the close-on-success effect depends only on
  // saveStatus. Parents pass a fresh arrow function each render, which would
  // otherwise re-fire the effect on every render while saveStatus is "success".
  const onOpenChangeRef = useRef(onOpenChange);
  useLayoutEffect(() => {
    onOpenChangeRef.current = onOpenChange;
  }, [onOpenChange]);

  // Close on successful save. The draft resets naturally because the parent
  // conditionally renders this dialog (it unmounts when editingProvider is null).
  useEffect(() => {
    if (saveStatus === "success") {
      onOpenChangeRef.current(false);
    }
  }, [saveStatus]);

  const saving = saveStatus === "saving";
  const configured = Boolean(existingMaskedKey);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{info.name} API key</DialogTitle>
          <DialogDescription>{info.description}</DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          {configured && (
            <p className="text-xs text-muted-foreground">
              Current key: <span className="font-mono">{existingMaskedKey}</span>
            </p>
          )}
          <PasswordField
            value={draftKey}
            onChange={setDraftKey}
            placeholder={configured ? "Replace existing key..." : info.keyPlaceholder}
            ariaLabel={`${info.name} API key`}
            autoFocus
            disabled={saving}
          />
          {saveStatus === "error" && (
            <p className="text-xs text-destructive">
              {errorMessage ?? "Failed to save key."}
            </p>
          )}
        </div>

        <DialogFooter className="sm:justify-between">
          <div>
            {onRemove && (
              <Button
                variant="ghost"
                size="sm"
                className="text-xs text-destructive hover:text-destructive"
                onClick={onRemove}
                disabled={saving}
              >
                Remove
              </Button>
            )}
          </div>
          <div className="flex gap-2">
            <Button variant="outline" onClick={() => onOpenChange(false)} disabled={saving}>
              Cancel
            </Button>
            <Button
              onClick={() => onSave(draftKey.trim())}
              disabled={!draftKey.trim() || saving}
            >
              {saving ? "Saving..." : "Save"}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
