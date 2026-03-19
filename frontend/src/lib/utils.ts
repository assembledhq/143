import { clsx, type ClassValue } from "clsx"
import { twMerge } from "tailwind-merge"
import type { Session } from "@/lib/types"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

export function sessionTitle(session: Session): string {
  if (session.title) return session.title;
  if (session.pm_approach) return session.pm_approach;
  if (session.result_summary) return session.result_summary;
  return `Session ${session.id.slice(0, 8)}`;
}

export function formatTimeAgo(dateStr: string): string {
  const date = new Date(dateStr);
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffMins = Math.floor(diffMs / 60000);
  if (diffMins < 1) return "just now";
  if (diffMins < 60) return `${diffMins}m ago`;
  const diffHours = Math.floor(diffMins / 60);
  if (diffHours < 24) return `${diffHours}h ago`;
  const diffDays = Math.floor(diffHours / 24);
  if (diffDays < 30) return `${diffDays}d ago`;
  return date.toLocaleDateString();
}
