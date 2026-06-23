"use client";

import { useMemo } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { ManualSessionComposer } from "@/components/manual-session-composer";
import { MobileBackButton } from "@/components/mobile-back-button";
import { buildFilterSuffix } from "@/hooks/use-people-filter";
import { cn } from "@/lib/utils";

export function ManualSessionCreatePageContent() {
  const router = useRouter();
  const searchParams = useSearchParams();

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

  return (
    <div className="flex flex-col h-full">
      <div className="md:hidden flex items-center px-2 pt-2">
        <MobileBackButton to="/sessions" label="Back to sessions" />
      </div>
      <div className="flex flex-1 flex-col px-4 pb-4">
        <ManualSessionComposer
          enableDrafts
          autoFocus
          initialRepoId={repoId}
          showDropIndicator
          dataTestId="manual-session-dropzone"
          textareaAriaLabel="Manual session prompt"
          className={cn("mx-auto flex w-full max-w-3xl flex-1")}
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
