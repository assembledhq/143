"use client";

import type { ReactNode } from "react";
import { CodingAuthProviderCards, type CodingAuthProviderOption } from "@/components/coding-auth-provider-cards";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import {
  ResponsiveModal,
  ResponsiveModalBody,
  ResponsiveModalDescription,
  ResponsiveModalFooter,
  ResponsiveModalHeader,
  ResponsiveModalTitle,
} from "@/components/ui/responsive-modal";

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
    <ResponsiveModal open={open} onOpenChange={onOpenChange} desktopClassName="sm:max-w-2xl">
      <ResponsiveModalHeader>
        <ResponsiveModalTitle>{title}</ResponsiveModalTitle>
        <ResponsiveModalDescription>{description}</ResponsiveModalDescription>
      </ResponsiveModalHeader>

      <ResponsiveModalBody>
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
      </ResponsiveModalBody>

      <ResponsiveModalFooter>
        <Button variant="outline" onClick={onCancel}>{cancelLabel}</Button>
        <Button onClick={onPrimary} disabled={primaryDisabled}>{primaryLabel}</Button>
      </ResponsiveModalFooter>
    </ResponsiveModal>
  );
}
