"use client";

import { ChevronLeft } from "lucide-react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { cn } from "@/lib/utils";

interface MobileBackButtonProps {
  /** Destination list path, e.g. "/sessions" or "/projects". Search params from
   * the current URL are preserved so filter state survives the round trip. */
  to: string;
  label: string;
  className?: string;
}

export function MobileBackButton({ to, label, className }: MobileBackButtonProps) {
  const searchParams = useSearchParams();
  const qs = searchParams.toString();
  const href = qs ? `${to}?${qs}` : to;

  return (
    <Link
      href={href}
      aria-label={label}
      className={cn(
        "md:hidden inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-surface-hover hover:text-foreground",
        className,
      )}
    >
      <ChevronLeft className="h-5 w-5" />
    </Link>
  );
}
