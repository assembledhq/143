"use client";

import { useState, useRef, useEffect } from "react";
import { ArrowLeft, ChevronLeft, ChevronRight, Columns2, FolderSearch, Rows3, Search, X, Files, MessageSquare } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import type { ViewMode } from "./review-toolbar";

interface DiffToolbarProps {
  onBack: () => void;
  viewMode: ViewMode;
  onViewModeChange: (mode: ViewMode) => void;
  onBrowseRepo?: () => void;
  searchQuery?: string;
  onSearchChange?: (query: string) => void;
  isMobile?: boolean;
  filePath?: string;
  filePositionLabel?: string;
  onOpenFileList?: () => void;
  onPrevFile?: () => void;
  onNextFile?: () => void;
  canGoPrev?: boolean;
  canGoNext?: boolean;
  mobileChromeCollapsed?: boolean;
  onOpenComposer?: () => void;
}

export function DiffToolbar({
  onBack,
  viewMode,
  onViewModeChange,
  onBrowseRepo,
  searchQuery,
  onSearchChange,
  isMobile = false,
  filePath,
  filePositionLabel,
  onOpenFileList,
  onPrevFile,
  onNextFile,
  canGoPrev = false,
  canGoNext = false,
  mobileChromeCollapsed = false,
  onOpenComposer,
}: DiffToolbarProps) {
  const [showSearch, setShowSearch] = useState(false);
  const searchInputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (showSearch) {
      searchInputRef.current?.focus();
    }
  }, [showSearch]);

  if (isMobile) {
    return (
      <div
        className={cn(
          "sticky top-0 z-20 shrink-0 border-b border-border/70 bg-background/95 backdrop-blur transition-all duration-200",
          mobileChromeCollapsed && !showSearch && "shadow-sm"
        )}
      >
        <div className="flex items-center gap-1.5 px-2 py-2">
          <Button
            variant="ghost"
            size="sm"
            className="h-8 w-8 shrink-0 px-0 text-muted-foreground hover:text-foreground"
            onClick={onBack}
            aria-label="Back to conversation"
          >
            <ArrowLeft className="h-3.5 w-3.5" />
          </Button>
          <div className="min-w-0 flex-1">
            <div className="truncate font-mono text-xs font-medium text-foreground">
              {filePath || "Changed file"}
            </div>
            {filePositionLabel ? (
              <div
                className={cn(
                  "text-xs text-muted-foreground transition-all duration-200",
                  mobileChromeCollapsed && "max-h-0 overflow-hidden opacity-0"
                )}
              >
                {filePositionLabel}
              </div>
            ) : null}
          </div>
          <div className="ml-auto flex items-center gap-0.5">
            <Button
              variant="ghost"
              size="sm"
              className="h-8 w-8 shrink-0 px-0"
              onClick={onPrevFile}
              disabled={!canGoPrev}
              aria-label="Previous file"
            >
              <ChevronLeft className="h-3.5 w-3.5" />
            </Button>
            <Button
              variant="ghost"
              size="sm"
              className="h-8 w-8 shrink-0 px-0"
              onClick={onNextFile}
              disabled={!canGoNext}
              aria-label="Next file"
            >
              <ChevronRight className="h-3.5 w-3.5" />
            </Button>
            {onSearchChange ? (
              <Button
                variant="ghost"
                size="sm"
                className={cn("h-8 w-8 shrink-0 px-0", showSearch && "text-primary")}
                onClick={() => {
                  setShowSearch((v) => !v);
                  if (showSearch) {
                    onSearchChange("");
                  }
                }}
                title="Search in diff"
                aria-label="Search in diff"
              >
                <Search className="h-3.5 w-3.5" />
              </Button>
            ) : null}
            {onOpenFileList ? (
              <Button
                variant="ghost"
                size="sm"
                className="h-8 w-8 shrink-0 px-0"
                onClick={onOpenFileList}
                aria-label="Open files list"
              >
                <Files className="h-3.5 w-3.5" />
              </Button>
            ) : null}
            {onOpenComposer ? (
              <Button
                variant="ghost"
                size="sm"
                className="h-8 w-8 shrink-0 px-0"
                onClick={onOpenComposer}
                aria-label="Message agent"
              >
                <MessageSquare className="h-3.5 w-3.5" />
              </Button>
            ) : null}
            {onBrowseRepo ? (
              <Button
                variant="ghost"
                size="sm"
                className="h-8 w-8 shrink-0 px-0"
                onClick={onBrowseRepo}
                title="Browse repository"
                aria-label="Browse repository"
              >
                <FolderSearch className="h-3.5 w-3.5" />
              </Button>
            ) : null}
          </div>
        </div>

        {showSearch && onSearchChange ? (
          <div className="flex items-center gap-2 border-t border-border/50 bg-muted/20 px-2 py-2">
            <Search className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
            <Input
              ref={searchInputRef}
              value={searchQuery ?? ""}
              onChange={(e) => onSearchChange(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Escape") {
                  setShowSearch(false);
                  onSearchChange("");
                }
              }}
              placeholder="Search in diff..."
              className="h-8 border-border/60 bg-background text-xs placeholder:text-muted-foreground/60"
            />
            {searchQuery ? (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => onSearchChange("")}
                className="h-8 w-8 px-0 text-muted-foreground hover:text-foreground"
              >
                <X className="h-3 w-3" />
              </Button>
            ) : null}
          </div>
        ) : null}
      </div>
    );
  }

  return (
    <div className="flex flex-col border-b border-border bg-background shrink-0">
      <div className="flex items-center justify-between px-3 py-1.5">
        <Button
          variant="ghost"
          size="sm"
          className="h-7 gap-1.5 text-xs px-2 text-muted-foreground hover:text-foreground"
          onClick={onBack}
        >
          <ArrowLeft className="h-3.5 w-3.5" />
          Back to conversation
        </Button>

        <div className="flex items-center gap-1">
          {/* Search toggle */}
          {onSearchChange && (
            <Button
              variant="ghost"
              size="sm"
              className={cn("h-7 w-7 p-0", showSearch && "text-primary")}
              onClick={() => {
                setShowSearch((v) => !v);
                if (showSearch && onSearchChange) {
                  onSearchChange("");
                }
              }}
              title="Search in diff (Ctrl+F)"
            >
              <Search className="h-3.5 w-3.5" />
            </Button>
          )}
          {/* View mode toggle */}
          <div className="flex items-center rounded-md border border-border bg-muted/30">
            <button
              onClick={() => onViewModeChange("unified")}
              className={cn(
                "flex items-center gap-1 px-2 py-1 text-xs rounded-l-md transition-colors",
                viewMode === "unified"
                  ? "bg-background text-foreground shadow-sm"
                  : "text-muted-foreground hover:text-foreground"
              )}
            >
              <Rows3 className="h-3 w-3" />
              Unified
            </button>
            <button
              onClick={() => onViewModeChange("split")}
              className={cn(
                "flex items-center gap-1 px-2 py-1 text-xs rounded-r-md transition-colors",
                viewMode === "split"
                  ? "bg-background text-foreground shadow-sm"
                  : "text-muted-foreground hover:text-foreground"
              )}
            >
              <Columns2 className="h-3 w-3" />
              Split
            </button>
          </div>

          {onBrowseRepo && (
            <Button
              variant="ghost"
              size="sm"
              className="h-7 gap-1 text-xs px-2"
              onClick={onBrowseRepo}
              title="Browse repository (e)"
            >
              <FolderSearch className="h-3 w-3" />
              Browse
            </Button>
          )}
        </div>
      </div>

      {/* Search bar */}
      {showSearch && onSearchChange && (
        <div className="flex items-center gap-2 px-3 py-1.5 border-t border-border/50 bg-muted/20">
          <Search className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
          <Input
            ref={searchInputRef}
            value={searchQuery ?? ""}
            onChange={(e) => onSearchChange(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                setShowSearch(false);
                onSearchChange("");
              }
            }}
            placeholder="Search in diff..."
            className="h-8 flex-1 border-border/60 bg-background text-xs placeholder:text-muted-foreground/60"
          />
          {searchQuery && (
            <button
              onClick={() => onSearchChange("")}
              className="text-muted-foreground hover:text-foreground"
            >
              <X className="h-3 w-3" />
            </button>
          )}
        </div>
      )}
    </div>
  );
}
