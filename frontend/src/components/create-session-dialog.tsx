"use client";

import { useRouter } from "next/navigation";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { ManualSessionComposer } from "@/components/manual-session-composer";
import { useMediaQuery } from "@/hooks/use-media-query";
import { cn } from "@/lib/utils";

interface CreateSessionDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export function CreateSessionDialog({ open, onOpenChange }: CreateSessionDialogProps) {
  const router = useRouter();
  const isMobile = useMediaQuery("(max-width: 767px)");

  const dialogContentClassName = cn(
    "p-0 gap-0 overflow-hidden sm:max-w-[640px]",
    isMobile && "inset-0 h-dvh max-h-dvh max-w-none translate-x-0 translate-y-0 rounded-none border-0 flex flex-col overflow-y-auto",
  );

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className={dialogContentClassName} showCloseButton={false}>
        <DialogHeader className={cn("px-5 pt-5 pb-3", isMobile && "shrink-0")}>
          <DialogTitle className="text-base font-semibold">New session</DialogTitle>
          <DialogDescription className="sr-only">Create a new coding agent session</DialogDescription>
        </DialogHeader>

        {/* Mounting the composer only while open avoids carrying stale state
            (message, attachments, model selection) across reopens. Draft
            persistence is shared with /sessions/new so a prompt typed here
            survives a reload, and switching surfaces preserves the draft. */}
        {open && (
          <div className="px-4 pb-4">
            <ManualSessionComposer
              autoFocus
              enableDrafts
              onCreated={(id) => {
                onOpenChange(false);
                router.push(`/sessions/${id}`);
              }}
            />
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
