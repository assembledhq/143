"use client";

import { X } from "lucide-react";
import Image from "next/image";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogDescription,
  DialogOverlay,
  DialogPortal,
  DialogTitle,
} from "@/components/ui/dialog";
import { cn } from "@/lib/utils";
import { Dialog as DialogPrimitive } from "radix-ui";

type ImageLightboxProps = {
  open: boolean;
  src: string;
  alt: string;
  onOpenChange: (open: boolean) => void;
};

export function ImageLightbox({ open, src, alt, onOpenChange }: ImageLightboxProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPortal>
        <DialogOverlay className="bg-black/80 backdrop-blur-sm" />
        <DialogPrimitive.Content
          aria-label="Image preview"
          className={cn(
            "fixed inset-0 z-50 flex h-screen w-screen items-center justify-center border-none bg-transparent p-4 outline-none sm:p-6",
          )}
        >
          <DialogTitle className="sr-only">Image preview</DialogTitle>
          <DialogDescription className="sr-only">
            Enlarged preview of {alt}
          </DialogDescription>
          <DialogClose asChild>
            <Button
              type="button"
              variant="secondary"
              size="icon"
              aria-label="Close image preview"
              className="absolute right-4 top-4 z-10 rounded-full bg-surface-raised/90 shadow-lg backdrop-blur-sm hover:bg-surface-raised sm:right-6 sm:top-6"
            >
              <X className="h-4 w-4" />
            </Button>
          </DialogClose>
          <div className="flex max-h-full max-w-full items-center justify-center">
            <Image
              src={src}
              alt={alt}
              width={1600}
              height={1200}
              unoptimized
              className="max-h-[88vh] max-w-[92vw] rounded-xl object-contain shadow-2xl"
            />
          </div>
        </DialogPrimitive.Content>
      </DialogPortal>
    </Dialog>
  );
}
