"use client";

import { ChevronDown, Loader2, Sparkles } from "lucide-react";

import type { SessionReviewCapabilities, SessionReviewMode } from "@/lib/types";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

const MODE_LABELS: Record<SessionReviewMode, string> = {
  default: "Code review",
  security: "Security review",
};

type ReviewButtonProps = {
  capabilities: SessionReviewCapabilities | undefined;
  pendingMode: SessionReviewMode | null;
  onReview: (mode: SessionReviewMode) => void;
};

// ReviewButton renders the session-native Review action when the agent
// supports a curated review surface (e.g. Claude Code's /review skill).
// Hidden entirely for agents without native review support — by design we
// don't fall back to a hand-rolled prompt.
export function ReviewButton({ capabilities, pendingMode, onReview }: ReviewButtonProps) {
  if (!capabilities || capabilities.modes.length === 0) {
    return null;
  }

  const disabled = pendingMode !== null || !capabilities.can_review;
  const isPending = pendingMode !== null;

  if (capabilities.modes.length === 1) {
    const [only] = capabilities.modes;
    return (
      <Button
        size="sm"
        variant="outline"
        disabled={disabled}
        onClick={() => onReview(only)}
        title={!capabilities.can_review ? capabilities.reason : undefined}
      >
        {isPending ? (
          <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
        ) : (
          <Sparkles className="mr-1.5 h-3.5 w-3.5" />
        )}
        {isPending ? "Reviewing…" : MODE_LABELS[only] ?? only}
      </Button>
    );
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          size="sm"
          variant="outline"
          disabled={disabled}
          title={!capabilities.can_review ? capabilities.reason : undefined}
        >
          {isPending ? (
            <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
          ) : (
            <Sparkles className="mr-1.5 h-3.5 w-3.5" />
          )}
          {isPending ? "Reviewing…" : "Review"}
          {!isPending && <ChevronDown className="ml-1 h-3.5 w-3.5" />}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {capabilities.modes.map((mode) => (
          <DropdownMenuItem
            key={mode}
            onSelect={() => onReview(mode)}
            disabled={disabled}
          >
            {MODE_LABELS[mode] ?? mode}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
