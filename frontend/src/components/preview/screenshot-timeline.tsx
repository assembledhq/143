"use client";

import { useRef, useState } from "react";
import {
  Camera,
  ChevronLeft,
  ChevronRight,
  X,
  AlertTriangle,
  FileCode2,
  Clock,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ScrollArea, ScrollBar } from "@/components/ui/scroll-area";
import { cn, formatTimeAgo } from "@/lib/utils";
import type { PreviewSnapshot, PreviewTrigger } from "@/lib/preview-types";

interface ScreenshotTimelineProps {
  snapshots: PreviewSnapshot[];
}

const TRIGGER_LABELS: Record<PreviewTrigger, string> = {
  baseline: "Baseline",
  agent_change: "Agent Change",
  user_request: "Manual",
  hmr_update: "HMR",
  periodic: "Auto",
};

const TRIGGER_COLORS: Record<PreviewTrigger, string> = {
  baseline: "bg-blue-500/15 text-blue-600 dark:text-blue-400",
  agent_change: "bg-primary/15 text-primary",
  user_request: "bg-amber-500/15 text-amber-600 dark:text-amber-400",
  hmr_update: "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400",
  periodic: "bg-muted text-muted-foreground",
};

export function ScreenshotTimeline({
  snapshots,
}: ScreenshotTimelineProps) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const [expandedSnapshot, setExpandedSnapshot] =
    useState<PreviewSnapshot | null>(null);

  if (snapshots.length === 0) {
    return null;
  }

  const scrollBy = (direction: "left" | "right") => {
    const container = scrollRef.current?.querySelector(
      "[data-slot='scroll-area-viewport']"
    );
    if (container) {
      container.scrollBy({
        left: direction === "left" ? -200 : 200,
        behavior: "smooth",
      });
    }
  };

  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2">
        <Camera className="size-3.5 text-muted-foreground" />
        <span className="text-xs font-medium text-muted-foreground">
          Screenshots ({snapshots.length})
        </span>
        <div className="flex-1" />
        <Button
          size="icon-xs"
          variant="ghost"
          onClick={() => scrollBy("left")}
        >
          <ChevronLeft className="size-3" />
        </Button>
        <Button
          size="icon-xs"
          variant="ghost"
          onClick={() => scrollBy("right")}
        >
          <ChevronRight className="size-3" />
        </Button>
      </div>

      <ScrollArea ref={scrollRef} className="w-full">
        <div className="flex gap-2 pb-2">
          {snapshots.map((snapshot) => (
            <button
              key={snapshot.id}
              onClick={() => setExpandedSnapshot(snapshot)}
              className={cn(
                "group relative flex-shrink-0 rounded-md border overflow-hidden transition-all hover:ring-2 hover:ring-primary/30",
                expandedSnapshot?.id === snapshot.id && "ring-2 ring-primary"
              )}
              style={{ width: 140 }}
            >
              {/* Thumbnail */}
              <div className="relative h-20 bg-muted">
                {snapshot.thumbnail_url || snapshot.screenshot_url ? (
                  <img
                    src={snapshot.thumbnail_url || snapshot.screenshot_url}
                    alt={`Screenshot - ${TRIGGER_LABELS[snapshot.trigger]}`}
                    className="w-full h-full object-cover object-top"
                    onError={(e) => { (e.target as HTMLImageElement).style.display = 'none'; }}
                  />
                ) : (
                  <div className="w-full h-full flex items-center justify-center">
                    <Camera className="size-4 text-muted-foreground/50" />
                  </div>
                )}

                {/* Console error indicator */}
                {snapshot.console_error_count > 0 && (
                  <div className="absolute top-1 right-1 flex items-center gap-0.5 rounded bg-destructive/90 px-1 py-0.5 text-xs text-white">
                    <AlertTriangle className="size-2.5" />
                    {snapshot.console_error_count}
                  </div>
                )}
              </div>

              {/* Info */}
              <div className="p-1.5 space-y-1">
                <Badge
                  variant="secondary"
                  className={cn(
                    "text-xs px-1 py-0",
                    TRIGGER_COLORS[snapshot.trigger]
                  )}
                >
                  {TRIGGER_LABELS[snapshot.trigger]}
                </Badge>
                <div className="flex items-center gap-1 text-xs text-muted-foreground">
                  <Clock className="size-2.5" />
                  {formatTimeAgo(snapshot.created_at)}
                </div>
              </div>
            </button>
          ))}
        </div>
        <ScrollBar orientation="horizontal" />
      </ScrollArea>

      {/* Expanded screenshot view */}
      {expandedSnapshot && (
        <div className="rounded-lg border bg-background overflow-hidden">
          <div className="flex items-center justify-between p-2 border-b">
            <div className="flex items-center gap-2">
              <Badge
                variant="secondary"
                className={TRIGGER_COLORS[expandedSnapshot.trigger]}
              >
                {TRIGGER_LABELS[expandedSnapshot.trigger]}
              </Badge>
              <span className="text-xs text-muted-foreground">
                {expandedSnapshot.viewport_width} x{" "}
                {expandedSnapshot.viewport_height}
              </span>
              <span className="text-xs text-muted-foreground">
                {formatTimeAgo(expandedSnapshot.created_at)}
              </span>
            </div>
            <Button
              size="icon-xs"
              variant="ghost"
              onClick={() => setExpandedSnapshot(null)}
            >
              <X className="size-3" />
            </Button>
          </div>

          {/* Full screenshot */}
          <div className="max-h-[400px] overflow-auto bg-muted/30">
            <img
              src={expandedSnapshot.screenshot_url}
              alt="Full screenshot"
              className="w-full"
              onError={(e) => { (e.target as HTMLImageElement).style.display = 'none'; }}
            />
          </div>

          {/* Changed files + console errors */}
          <div className="p-2 space-y-2 border-t">
            {expandedSnapshot.changed_files &&
              expandedSnapshot.changed_files.length > 0 && (
                <div className="space-y-1">
                  <span className="text-xs font-medium text-muted-foreground flex items-center gap-1">
                    <FileCode2 className="size-3" />
                    Changed files
                  </span>
                  <div className="flex flex-wrap gap-1">
                    {expandedSnapshot.changed_files.map((file) => (
                      <Badge
                        key={file}
                        variant="secondary"
                        className="text-xs font-mono"
                      >
                        {file}
                      </Badge>
                    ))}
                  </div>
                </div>
              )}

            {expandedSnapshot.console_error_count > 0 && (
              <div className="flex items-center gap-1 text-xs text-destructive">
                <AlertTriangle className="size-3" />
                {expandedSnapshot.console_error_count} console error
                {expandedSnapshot.console_error_count !== 1 ? "s" : ""}
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
