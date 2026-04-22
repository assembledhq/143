"use client";

import Image from "next/image";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";

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
        className="max-w-[90vw] border-none bg-transparent p-0 shadow-none"
      >
        <DialogTitle className="sr-only">Image preview</DialogTitle>
        <DialogDescription className="sr-only">
          Enlarged preview of {alt}
        </DialogDescription>
        <Image
          src={src}
          alt={alt}
          width={1600}
          height={1200}
          unoptimized
          className="max-h-[90vh] max-w-[90vw] rounded-lg object-contain shadow-2xl"
        />
      </DialogContent>
    </Dialog>
  );
}
