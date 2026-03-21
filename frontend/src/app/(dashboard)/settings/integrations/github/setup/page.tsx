"use client";

import { useEffect } from "react";
import { useSearchParams } from "next/navigation";

const API_BASE = process.env.NEXT_PUBLIC_API_URL || "";

/**
 * GitHub App Setup callback page.
 *
 * GitHub redirects here after a user installs or updates the GitHub App
 * (via the "Setup URL" configured in the App settings). We forward the
 * query parameters (installation_id, setup_action, etc.) to the backend
 * endpoint that records the installation and syncs repos.
 */
export default function GitHubSetupPage() {
  const searchParams = useSearchParams();

  useEffect(() => {
    const params = searchParams.toString();
    window.location.href = `${API_BASE}/api/v1/integrations/github/installed?${params}`;
  }, [searchParams]);

  return (
    <div className="flex h-[50vh] items-center justify-center">
      <p className="text-sm text-muted-foreground">Completing GitHub setup…</p>
    </div>
  );
}
