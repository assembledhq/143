import { useState, useRef, useCallback, useEffect } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";

const STORAGE_KEY = "143:analyze-started-at";
const TIMEOUT_MS = 90_000;

/**
 * Shared hook for the "Analyze Issues" button used by both the sessions and
 * plans pages.  It persists an "analyzing" flag in sessionStorage so the
 * spinner survives client-side navigations, and automatically clears when
 * either:
 *   - the supplied `hasActivePlanSession` turns true (backend created the plan)
 *   - a 90-second timeout fires
 */
export function useAnalyze(hasActivePlanSession: boolean) {
  const queryClient = useQueryClient();

  // Seed local state from sessionStorage so we pick it back up after nav.
  const [localAnalyzing, setLocalAnalyzing] = useState(() => {
    if (typeof window === "undefined") return false;
    const stored = sessionStorage.getItem(STORAGE_KEY);
    if (!stored) return false;
    const elapsed = Date.now() - Number(stored);
    if (elapsed > TIMEOUT_MS) {
      sessionStorage.removeItem(STORAGE_KEY);
      return false;
    }
    return true;
  });

  const [analyzeError, setAnalyzeError] = useState<string | null>(null);
  const timeoutRef = useRef<NodeJS.Timeout | null>(null);

  // Derive: we're "analyzing" if we triggered it locally OR the backend has an
  // active plan session (which means the worker is executing the analysis).
  const isAnalyzing = localAnalyzing || hasActivePlanSession;

  // Clear the local flag + storage when a plan session becomes active (backend
  // has picked up the job) or completes.
  useEffect(() => {
    if (!localAnalyzing) return;
    if (hasActivePlanSession) {
      // Backend created the plan — clear our local "enqueued" flag.  The
      // derived `isAnalyzing` will stay true via `hasActivePlanSession` until
      // the plan finishes.
      setLocalAnalyzing(false);
      sessionStorage.removeItem(STORAGE_KEY);
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
        timeoutRef.current = null;
      }
    }
  }, [localAnalyzing, hasActivePlanSession]);

  // Start the timeout when localAnalyzing becomes true.
  useEffect(() => {
    if (!localAnalyzing) return;
    const stored = sessionStorage.getItem(STORAGE_KEY);
    const startedAt = stored ? Number(stored) : Date.now();
    const remaining = TIMEOUT_MS - (Date.now() - startedAt);

    if (remaining <= 0) {
      setLocalAnalyzing(false);
      sessionStorage.removeItem(STORAGE_KEY);
      setAnalyzeError(
        "Analysis may have failed or is taking longer than expected. Check your server logs for details."
      );
      return;
    }

    timeoutRef.current = setTimeout(() => {
      setLocalAnalyzing(false);
      sessionStorage.removeItem(STORAGE_KEY);
      setAnalyzeError(
        "Analysis may have failed or is taking longer than expected. Check your server logs for details."
      );
    }, remaining);

    return () => {
      if (timeoutRef.current) clearTimeout(timeoutRef.current);
    };
  }, [localAnalyzing]);

  const mutation = useMutation({
    mutationFn: () => api.pm.analyze(),
    onSuccess: () => {
      sessionStorage.setItem(STORAGE_KEY, String(Date.now()));
      setLocalAnalyzing(true);
      queryClient.invalidateQueries({ queryKey: ["sessions"] });
      queryClient.invalidateQueries({ queryKey: ["pm", "latest"] });
      queryClient.invalidateQueries({ queryKey: ["pm", "plans"] });
    },
    onError: () => {
      setAnalyzeError("Failed to start analysis. Make sure the backend is running.");
    },
  });

  const handleAnalyze = useCallback(() => {
    setAnalyzeError(null);
    mutation.mutate();
  }, [mutation]);

  const dismissError = useCallback(() => setAnalyzeError(null), []);

  return {
    isAnalyzing,
    isPending: mutation.isPending,
    analyzeError,
    handleAnalyze,
    dismissError,
  };
}
