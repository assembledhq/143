"use client";

import { X } from "lucide-react";
import Image from "next/image";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";

type ImageLightboxProps = {
  open: boolean;
  src: string;
  alt: string;
  onOpenChange: (open: boolean) => void;
};

export function ImageLightbox({ open, src, alt, onOpenChange }: ImageLightboxProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        aria-label="Image preview"
        showCloseButton={false}
        className="pointer-events-none inset-0 flex max-w-none translate-x-0 translate-y-0 items-center justify-center border-none bg-transparent p-4 shadow-none sm:p-6"
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
            className="pointer-events-auto fixed right-4 top-4 z-10 rounded-full bg-background/90 shadow-lg backdrop-blur-sm hover:bg-background sm:right-6 sm:top-6"
          >
            <X className="h-4 w-4" />
          </Button>
        </DialogClose>
        <div className="pointer-events-auto flex max-h-full max-w-full items-center justify-center">
          <Image
            src={src}
            alt={alt}
            width={1600}
            height={1200}
            unoptimized
            className="max-h-[88vh] max-w-[92vw] rounded-xl object-contain shadow-2xl"
          />
        </div>
      </DialogContent>
    </Dialog>
  );
}
