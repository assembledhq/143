import { clsx, type ClassValue } from "clsx"
import { twMerge } from "tailwind-merge"
import type { Session } from "@/lib/types"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

export function capitalizeWords(value: string): string {
  return value
    .replaceAll("_", " ")
    .replace(/\b\w/g, (match) => match.toUpperCase())
}

export function sessionTitle(session: Session): string {
  if (session.title) return session.title;
  if (session.pm_approach) return session.pm_approach;
  if (session.result_summary) return session.result_summary;
  return `Session ${session.id.slice(0, 8)}`;
}

/**
 * Check whether a URL points to an image, handling query params/fragments
 * from S3 presigned URLs.
 */
export function isImageURL(url: string): boolean {
  if (url.startsWith("data:image/")) return true;
  const pathname = url.split("?")[0].split("#")[0];
  return /\.(png|jpe?g|gif|webp|svg)$/i.test(pathname);
}

/**
 * Extract a clean file name from a URL, stripping query params.
 */
export function fileNameFromURL(url: string): string {
  return url.split("?")[0].split("#")[0].split("/").pop() || "file";
}

/**
 * Validate that a URL from an untrusted source (e.g. API response) uses an
 * allowed protocol before using it in an href or window.open call.
 * Returns the URL unchanged when safe, or undefined when the protocol is not
 * http(s) — preventing javascript: / data: XSS.
 */
export function safeExternalUrl(url: string | undefined | null): string | undefined {
  if (!url) return undefined;
  try {
    const parsed = new URL(url);
    if (parsed.protocol === "https:" || parsed.protocol === "http:") {
      return url;
    }
  } catch {
    // Not a parseable absolute URL.
  }
  return undefined;
}

type FormatTimeAgoOptions = {
  fallback?: string;
  includeSeconds?: boolean;
  nowMs?: number;
};

type FormatDateTimeOptions = {
  fallback?: string;
  year?: boolean;
  seconds?: boolean;
  weekday?: boolean;
  timeZoneName?: boolean;
};

export function formatDateTime(
  dateStr: string | null | undefined,
  options?: FormatDateTimeOptions,
): string {
  const fallback = options?.fallback ?? "—";
  if (!dateStr) return fallback;
  const date = new Date(dateStr);
  if (Number.isNaN(date.getTime())) return fallback;
  return new Intl.DateTimeFormat(undefined, {
    weekday: options?.weekday ? "short" : undefined,
    month: "short",
    day: "numeric",
    year: options?.year ? "numeric" : undefined,
    hour: "numeric",
    minute: "2-digit",
    second: options?.seconds ? "2-digit" : undefined,
    timeZoneName: options?.timeZoneName ? "short" : undefined,
  }).format(date);
}

export function formatTimeAgo(
  dateStr: string | null | undefined,
  options?: FormatTimeAgoOptions,
): string {
  const fallback = options?.fallback ?? "—";
  if (!dateStr) return fallback;
  const date = new Date(dateStr);
  if (Number.isNaN(date.getTime())) return fallback;
  const diffMs = Math.max(0, (options?.nowMs ?? Date.now()) - date.getTime());
  if (options?.includeSeconds && diffMs < 60_000) {
    return `${Math.floor(diffMs / 1000)}s ago`;
  }
  const diffMins = Math.floor(diffMs / 60000);
  if (diffMins < 1) return "just now";
  if (diffMins < 60) return `${diffMins}m ago`;
  const diffHours = Math.floor(diffMins / 60);
  if (diffHours < 24) return `${diffHours}h ago`;
  const diffDays = Math.floor(diffHours / 24);
  if (diffDays < 30) return `${diffDays}d ago`;
  return date.toLocaleDateString();
}
