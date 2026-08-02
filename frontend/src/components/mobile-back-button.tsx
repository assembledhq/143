"use client";

import { ChevronLeft, Loader2 } from "lucide-react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

const NAVIGATION_FEEDBACK_TIMEOUT_MS = 8_000;

interface MobileBackButtonProps {
  /** Destination list path, e.g. "/sessions" or "/previews". Search params from
   * the current URL are preserved so filter state survives the round trip. */
  to: string;
  label: string;
  className?: string;
}

export function MobileBackButton({ to, label, className }: MobileBackButtonProps) {
  const searchParams = useSearchParams();
  const [isNavigating, setIsNavigating] = useState(false);
  const qs = searchParams.toString();
  const href = qs ? `${to}?${qs}` : to;

  // A route transition can wait on an RSC response when connectivity is poor.
  // Keep the acknowledgement immediate, but release the control if navigation
  // cannot finish so a failed request never leaves the UI looking frozen.
  useEffect(() => {
    if (!isNavigating) return;

    const timeout = window.setTimeout(
      () => setIsNavigating(false),
      NAVIGATION_FEEDBACK_TIMEOUT_MS,
    );
    return () => window.clearTimeout(timeout);
  }, [isNavigating]);

  return (
    <Button
      asChild
      variant="ghost"
      size="icon"
      className={cn(
        "size-11 touch-manipulation text-muted-foreground active:scale-[0.94] sm:size-11 md:hidden",
        isNavigating && "bg-accent text-foreground",
        className,
      )}
    >
      <Link
        href={href}
        aria-label={isNavigating ? `${label}, loading` : label}
        aria-busy={isNavigating || undefined}
        onClick={(event) => {
          if (
            event.defaultPrevented ||
            event.button !== 0 ||
            event.metaKey ||
            event.ctrlKey ||
            event.shiftKey ||
            event.altKey
          ) {
            return;
          }
          setIsNavigating(true);
        }}
      >
        {isNavigating ? (
          <Loader2 className="size-5 animate-spin" aria-hidden="true" />
        ) : (
          <ChevronLeft className="size-5" aria-hidden="true" />
        )}
      </Link>
    </Button>
  );
}
