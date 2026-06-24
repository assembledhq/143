"use client";

import { useCallback, useMemo, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Eye, EyeOff } from "lucide-react";
import { ManualSessionComposer } from "@/components/manual-session-composer";
import { ManualSessionPlaneCanvas } from "@/components/manual-session-plane-canvas";
import { MobileBackButton } from "@/components/mobile-back-button";
import { Button } from "@/components/ui/button";
import { buildFilterSuffix } from "@/hooks/use-people-filter";
import { useAuth } from "@/hooks/use-auth";
import { api } from "@/lib/api";
import { notify as toast } from "@/lib/notify";
import { cn } from "@/lib/utils";

export function ManualSessionCreatePageContent() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const storedPlanesHidden = user?.settings?.manual_session_planes_hidden ?? false;
  const [planesHiddenOverride, setPlanesHiddenOverride] = useState<boolean | null>(null);
  const planesHidden = planesHiddenOverride ?? storedPlanesHidden;

  const { mutate: persistPlanesHidden } = useMutation({
    mutationFn: (hidden: boolean) =>
      api.auth.updateSettings({ manual_session_planes_hidden: hidden }),
    onSuccess: (response) => {
      queryClient.setQueryData(["auth", "me"], { data: response.data });
      setPlanesHiddenOverride(null);
    },
    onError: () => {
      setPlanesHiddenOverride(null);
      toast.error("Couldn't save planes preference");
    },
  });

  // Read the currently selected repository from the URL query params
  // (set by the RepoContextSwitcher) so we clone the codebase into the sandbox.
  const repoId = searchParams.get("repo") ?? undefined;
  const filterSuffix = useMemo(
    () => buildFilterSuffix(
      searchParams.get("people") ?? searchParams.get("user"),
      searchParams.get("status"),
      searchParams.get("repo"),
      searchParams.get("search"),
    ),
    [searchParams],
  );

  const togglePlanes = useCallback(() => {
    const next = !planesHidden;
    setPlanesHiddenOverride(next);
    persistPlanesHidden(next);
  }, [persistPlanesHidden, planesHidden]);

  return (
    <div className="relative flex h-full flex-col">
      <div className="md:hidden flex items-center px-2 pt-2">
        <MobileBackButton to="/sessions" label="Back to sessions" />
      </div>
      <Button
        type="button"
        variant="ghost"
        size="icon"
        className="absolute right-2 top-2 z-20 h-8 w-8 text-muted-foreground hover:text-foreground md:right-4 md:top-4"
        aria-label={planesHidden ? "Show planes" : "Hide planes"}
        title={planesHidden ? "Show planes" : "Hide planes"}
        onClick={togglePlanes}
      >
        {planesHidden ? <Eye className="h-4 w-4" /> : <EyeOff className="h-4 w-4" />}
      </Button>
      <div className="relative flex flex-1 flex-col overflow-hidden px-4 pb-4">
        {!planesHidden ? <ManualSessionPlaneCanvas /> : null}
        <ManualSessionComposer
          enableDrafts
          autoFocus
          initialRepoId={repoId}
          showDropIndicator
          dataTestId="manual-session-dropzone"
          textareaAriaLabel="Manual session prompt"
          className={cn("relative z-10 mx-auto flex w-full max-w-3xl flex-1")}
          innerClassName="max-w-3xl"
          showOptimisticSidebarRow={false}
          onCreated={(id) => router.replace(`/sessions/${id}${filterSuffix}`)}
          heroSlot={
            <div className="flex-1 flex flex-col items-center justify-center px-2 pt-16 pb-4">
              <div className="text-center mb-8">
                <p className="text-3xl font-semibold tracking-tight bg-[image:var(--gradient-primary)] bg-clip-text text-transparent">
                  Let&apos;s build
                </p>
                <p className="mt-2 text-xs text-muted-foreground">
                  Start a manual session with text, files, photos, or a screenshot anywhere here.
                </p>
              </div>
            </div>
          }
        />
      </div>
    </div>
  );
}
