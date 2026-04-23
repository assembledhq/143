"use client";

import type { ReactNode } from "react";
import { CodingAuthProviderCards, type CodingAuthProviderOption } from "@/components/coding-auth-provider-cards";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";

interface CodingAuthDialogProps<T extends string> {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: string;
  providerOptions: Array<CodingAuthProviderOption<T>>;
  provider: T;
  onProviderChange: (value: T) => void;
  children: ReactNode;
  primaryLabel: string;
  onPrimary: () => void;
  primaryDisabled?: boolean;
  cancelLabel?: string;
  onCancel: () => void;
}

export function CodingAuthDialog<T extends string>({
  open,
  onOpenChange,
  title,
  description,
  providerOptions,
  provider,
  onProviderChange,
  children,
  primaryLabel,
  onPrimary,
  primaryDisabled,
  cancelLabel = "Cancel",
  onCancel,
}: CodingAuthDialogProps<T>) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>

        <div className="space-y-6">
          <div className="space-y-2">
            <Label>Provider</Label>
            <CodingAuthProviderCards
              options={providerOptions}
              value={provider}
              onValueChange={onProviderChange}
            />
          </div>

          {children}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={onCancel}>{cancelLabel}</Button>
          <Button onClick={onPrimary} disabled={primaryDisabled}>{primaryLabel}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
