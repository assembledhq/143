"use client";

import { useEffect } from "react";
import { usePathname, useRouter } from "next/navigation";
import { useAuth } from "@/hooks/use-auth";

// Pages that org-wide credentials, integrations, or settings live behind.
// Sidebar already hides these for non-admins; this guard redirects bookmarked
// or pasted URLs so a member doesn't land on a page that 403s every query.
const ADMIN_ONLY_PATHS = new Set([
  "/settings",
  "/settings/integrations",
  "/settings/agent",
  "/settings/llm",
  "/settings/autopilot",
  "/settings/usage",
  "/settings/audit-log",
]);

// Pages a viewer can't usefully load (the underlying API is admin+member only).
const VIEWER_BLOCKED_PATHS = new Set(["/settings/team"]);

function isAdminOnlyPath(pathname: string): boolean {
  if (ADMIN_ONLY_PATHS.has(pathname)) return true;
  for (const base of ADMIN_ONLY_PATHS) {
    if (base !== "/settings" && pathname.startsWith(base + "/")) return true;
  }
  return false;
}

function isViewerBlockedPath(pathname: string): boolean {
  if (VIEWER_BLOCKED_PATHS.has(pathname)) return true;
  for (const base of VIEWER_BLOCKED_PATHS) {
    if (pathname.startsWith(base + "/")) return true;
  }
  return false;
}

export default function SettingsLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const router = useRouter();
  const pathname = usePathname();
  const { user, isLoading } = useAuth();

  const role = user?.role;
  let restricted = false;
  if (!isLoading && role !== undefined) {
    if (role !== "admin" && isAdminOnlyPath(pathname)) {
      restricted = true;
    } else if (role === "viewer" && isViewerBlockedPath(pathname)) {
      restricted = true;
    }
  }

  useEffect(() => {
    if (restricted) {
      router.replace("/settings/account");
    }
  }, [restricted, router]);

  if (restricted) return null;

  return <>{children}</>;
}
