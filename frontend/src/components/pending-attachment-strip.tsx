"use client";

import { useMemo, useState } from "react";
import { Loader2, X } from "lucide-react";
import Image from "next/image";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { HoverCard, HoverCardContent, HoverCardTrigger } from "@/components/ui/hover-card";
import { ImageLightbox } from "@/components/image-lightbox";
import { cn, fileNameFromURL, isImageURL } from "@/lib/utils";

type PendingAttachmentStripProps = {
  attachments: string[];
  isUploading?: boolean;
  onRemove: (attachment: string) => void;
  size?: "sm" | "md";
  className?: string;
};

const sizeStyles = {
  sm: {
    tile: "h-14 w-14",
    removeButton: "size-4",
    removeIcon: "h-2.5 w-2.5",
    fileTile: "h-14 px-3 text-xs",
    spinnerTile: "h-14 w-14",
  },
  md: {
    tile: "h-16 w-16",
    removeButton: "size-5",
    removeIcon: "h-3 w-3",
    fileTile: "h-16 px-3 text-xs",
    spinnerTile: "h-16 w-16",
  },
} as const;

function AttachmentImageTile({
  url,
  fileName,
  size,
}: {
  url: string;
  fileName: string;
  size: keyof typeof sizeStyles;
}) {
  const [lightboxOpen, setLightboxOpen] = useState(false);
  const styles = sizeStyles[size];

  return (
    <>
      <ImageLightbox
        open={lightboxOpen}
        src={url}
        alt={fileName}
        onOpenChange={setLightboxOpen}
      />
      <HoverCard openDelay={200} closeDelay={100}>
        <HoverCardTrigger asChild>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={() => setLightboxOpen(true)}
            aria-label={`Preview ${fileName}`}
            className={cn(
              "rounded-md border border-border bg-transparent p-0 hover:bg-transparent focus-visible:ring-2",
              styles.tile,
            )}
          >
            <Image
              src={url}
              alt={fileName}
              width={128}
              height={128}
              unoptimized
              className={cn("h-full w-full rounded-md object-cover", styles.tile)}
            />
          </Button>
        </HoverCardTrigger>
        <HoverCardContent
          side="top"
          align="center"
          className="w-auto max-w-[min(72vw,58rem)] border-border/80 bg-popover/95 p-2 shadow-xl backdrop-blur-sm"
        >
          <Image
            src={url}
            alt={`Preview of ${fileName}`}
            width={1200}
            height={900}
            unoptimized
            className="max-h-[70vh] max-w-[min(70vw,56rem)] rounded-lg object-contain"
          />
        </HoverCardContent>
      </HoverCard>
    </>
  );
}

export function PendingAttachmentStrip({
  attachments,
  isUploading = false,
  onRemove,
  size = "md",
  className,
}: PendingAttachmentStripProps) {
  const styles = sizeStyles[size];
  const normalizedAttachments = useMemo(
    () => attachments.map((url) => ({
      url,
      isImage: isImageURL(url),
      fileName: url.startsWith("data:") ? "photo" : fileNameFromURL(url),
    })),
    [attachments],
  );

  if (normalizedAttachments.length === 0 && !isUploading) {
    return null;
  }

  return (
    <div className={cn("flex flex-wrap items-center gap-2", className)}>
      {normalizedAttachments.map(({ url, isImage, fileName }) => (
        <div key={url} className="relative">
          {isImage ? (
            <AttachmentImageTile url={url} fileName={fileName} size={size} />
          ) : (
            <Badge
              variant="secondary"
              className={cn(
                "justify-center rounded-md border border-border bg-muted text-muted-foreground",
                styles.fileTile,
              )}
            >
              {fileName}
            </Badge>
          )}
          <Button
            type="button"
            variant="outline"
            size="icon-xs"
            onClick={() => onRemove(url)}
            aria-label={`Remove ${fileName}`}
            className={cn(
              "absolute -top-1.5 -right-1.5 rounded-full bg-surface-raised p-0 shadow-sm",
              styles.removeButton,
            )}
          >
            <X className={styles.removeIcon} />
          </Button>
        </div>
      ))}
      {isUploading && (
        <div
          className={cn(
            "flex items-center justify-center rounded-md border border-border bg-muted",
            styles.spinnerTile,
          )}
        >
          <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
        </div>
      )}
    </div>
  );
}
