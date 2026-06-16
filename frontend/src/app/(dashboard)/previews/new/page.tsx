"use client";

import { useRouter, useSearchParams } from "next/navigation";

import { CreatePreviewDialog } from "@/components/preview/create-preview-dialog";

export default function NewPreviewPage() {
  const router = useRouter();
  const searchParams = useSearchParams();

  function closeToPreviews() {
    router.push("/previews");
  }

  return (
    <CreatePreviewDialog
      open
      onOpenChange={(open) => {
        if (!open) {
          closeToPreviews();
        }
      }}
      initialRepositoryId={searchParams.get("repo") ?? undefined}
      initialBranch={searchParams.get("branch") ?? undefined}
    />
  );
}
