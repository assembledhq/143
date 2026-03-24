"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";

/**
 * Hook to fetch a directory listing from a session's sandbox.
 */
export function useSessionFileList(sessionId: string, path: string, enabled = true) {
  return useQuery({
    queryKey: ["session", sessionId, "files", path],
    queryFn: () => api.sessions.listFiles(sessionId, path || undefined),
    enabled,
    staleTime: 30_000, // directory listings don't change often
  });
}

/**
 * Hook to fetch file content from a session's sandbox.
 */
export function useSessionFileContent(sessionId: string, path: string, enabled = true) {
  return useQuery({
    queryKey: ["session", sessionId, "files", "content", path],
    queryFn: () => api.sessions.getFileContent(sessionId, path),
    enabled: enabled && path !== "",
    staleTime: 60_000, // file content is stable while viewing
  });
}
