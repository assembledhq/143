"use client";

import * as React from "react";

import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Sheet, SheetContent } from "@/components/ui/sheet";
import { useMediaQuery } from "@/hooks/use-media-query";
import { cn } from "@/lib/utils";

const MOBILE_QUERY = "(max-width: 639px)";

type ResponsiveModalProps = React.ComponentProps<typeof Dialog> & {
  children: React.ReactNode;
  desktopClassName?: string;
  mobileClassName?: string;
};

function ResponsiveModal({
  children,
  desktopClassName,
  mobileClassName,
  ...props
}: ResponsiveModalProps) {
  const isMobile = useMediaQuery(MOBILE_QUERY);

  if (isMobile) {
    return (
      <Sheet {...props}>
        <SheetContent
          side="bottom"
          className={cn(
            "flex max-h-[100svh] min-h-[min(32rem,100svh)] flex-col gap-0 overflow-hidden rounded-t-xl p-0",
            mobileClassName,
          )}
        >
          {children}
        </SheetContent>
      </Sheet>
    );
  }

  return (
    <Dialog {...props}>
      <DialogContent
        className={cn(
          "flex max-h-[calc(100svh-2rem)] flex-col gap-0 overflow-hidden p-0",
          desktopClassName,
        )}
      >
        {children}
      </DialogContent>
    </Dialog>
  );
}

function ResponsiveModalHeader({
  className,
  ...props
}: React.ComponentProps<typeof DialogHeader>) {
  return (
    <DialogHeader
      data-slot="responsive-modal-header"
      className={cn("shrink-0 border-b border-border px-6 py-5 pr-12 text-left", className)}
      {...props}
    />
  );
}

function ResponsiveModalTitle({
  className,
  ...props
}: React.ComponentProps<typeof DialogTitle>) {
  return <DialogTitle className={cn("leading-snug", className)} {...props} />;
}

function ResponsiveModalDescription({
  className,
  ...props
}: React.ComponentProps<typeof DialogDescription>) {
  return <DialogDescription className={className} {...props} />;
}

function ResponsiveModalBody({
  className,
  ...props
}: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="responsive-modal-body"
      className={cn("flex-1 overflow-y-auto overscroll-contain px-6 py-5", className)}
      {...props}
    />
  );
}

function ResponsiveModalFooter({
  className,
  ...props
}: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="responsive-modal-footer"
      className={cn(
        "flex shrink-0 items-center justify-end gap-2 border-t border-border bg-background px-6 py-4 pb-[max(1rem,env(safe-area-inset-bottom))]",
        className,
      )}
      {...props}
    />
  );
}

export {
  ResponsiveModal,
  ResponsiveModalBody,
  ResponsiveModalDescription,
  ResponsiveModalFooter,
  ResponsiveModalHeader,
  ResponsiveModalTitle,
};
