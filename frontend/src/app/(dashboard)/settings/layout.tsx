"use client";

import { useEffect } from "react";
import { usePathname, useRouter } from "next/navigation";
import { useAuth } from "@/hooks/use-auth";

// Pages that org-wide credentials, integrations, or settings live behind.
// Sidebar already hides these for non-admins; this guard redirects bookmarked
// or pasted URLs so a member doesn't land on a page that 403s every query.
const ADMIN_ONLY_PATHS = new Set([
  "/settings",
  "/settings/llm",
  "/settings/autopilot",
  "/settings/usage",
  "/settings/audit-log",
]);

// Pages a viewer can't usefully load (the underlying APIs are admin+member only).
// Members can view these pages in read-only mode; viewers get redirected.
const VIEWER_BLOCKED_PATHS = new Set([
  "/settings/team",
  "/settings/integrations",
  "/settings/agent",
  "/settings/evals",
]);

// Builders can access personal coding-agent setup, but they do not inherit the
// broader member settings surface for team/evals/integrations management.
const BUILDER_BLOCKED_PATHS = new Set([
  "/settings/team",
  "/settings/integrations",
  "/settings/evals",
]);

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

function isBuilderBlockedPath(pathname: string): boolean {
  if (BUILDER_BLOCKED_PATHS.has(pathname)) return true;
  for (const base of BUILDER_BLOCKED_PATHS) {
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

  const roleGuardedPath = isAdminOnlyPath(pathname) || isViewerBlockedPath(pathname) || isBuilderBlockedPath(pathname);
  const role = user?.role;
  let restricted = false;
  if (!isLoading && role !== undefined) {
    if (role !== "admin" && isAdminOnlyPath(pathname)) {
      restricted = true;
    } else if (role === "viewer" && isViewerBlockedPath(pathname)) {
      restricted = true;
    } else if (role === "builder" && isBuilderBlockedPath(pathname)) {
      restricted = true;
    }
  }
  const waitForRole = isLoading && roleGuardedPath;

  useEffect(() => {
    if (restricted) {
      router.replace("/settings/account");
    }
  }, [restricted, router]);

  if (waitForRole || restricted) return null;

  return <>{children}</>;
}
