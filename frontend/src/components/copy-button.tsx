"use client";

import { useEffect, useState } from "react";
import { Check, Copy } from "lucide-react";
import { Button } from "@/components/ui/button";
import { canCopyToClipboard, copyTextToClipboard } from "@/lib/clipboard";
import { cn } from "@/lib/utils";

type CopyButtonProps = {
  value: string | undefined | null;
  label: string;
  copiedLabel?: string;
  className?: string;
};

const COPIED_RESET_MS = 1500;

export function CopyButton({
  value,
  label,
  copiedLabel = label.replace(/^Copy\b/, "Copied"),
  className,
}: CopyButtonProps) {
  const [copied, setCopied] = useState(false);
  const disabled = !value || !canCopyToClipboard();

  useEffect(() => {
    if (!copied) return undefined;
    const timer = window.setTimeout(() => setCopied(false), COPIED_RESET_MS);
    return () => window.clearTimeout(timer);
  }, [copied]);

  const copyToClipboard = async () => {
    if (!value) return;
    try {
      await copyTextToClipboard(value);
      setCopied(true);
      navigator.vibrate?.(10);
    } catch (error) {
      console.error("Failed to copy to clipboard", error);
    }
  };

  return (
    <Button
      type="button"
      variant="ghost"
      size="icon-sm"
      className={cn(copied && "text-primary", className)}
      disabled={disabled}
      aria-label={copied ? copiedLabel : label}
      onClick={() => {
        void copyToClipboard();
      }}
    >
      {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
    </Button>
  );
}
