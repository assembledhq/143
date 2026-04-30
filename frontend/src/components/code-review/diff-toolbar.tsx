"use client";

import { useState, useRef, useEffect } from "react";
import { ArrowLeft, ChevronLeft, ChevronRight, Columns2, FolderSearch, Rows3, Search, X, Files } from "lucide-react";
import { Button } from "@/components/ui/button";
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
      <div className="flex flex-col border-b border-border bg-background shrink-0">
        <div className="flex items-start gap-2 px-3 py-2">
          <Button
            variant="ghost"
            size="sm"
            className="h-8 shrink-0 gap-1.5 px-2 text-xs text-muted-foreground hover:text-foreground"
            onClick={onBack}
          >
            <ArrowLeft className="h-3.5 w-3.5" />
            Back
          </Button>
          <div className="min-w-0 flex-1">
            <div className="truncate font-mono text-xs font-medium text-foreground">
              {filePath || "Changed file"}
            </div>
            {filePositionLabel ? (
              <div className="text-xs text-muted-foreground">
                {filePositionLabel}
              </div>
            ) : null}
          </div>
          {onOpenFileList ? (
            <Button
              variant="outline"
              size="sm"
              className="h-8 shrink-0 gap-1.5 px-2 text-xs"
              onClick={onOpenFileList}
              aria-label="Open files list"
            >
              <Files className="h-3.5 w-3.5" />
              Files
            </Button>
          ) : null}
        </div>

        <div className="flex items-center gap-1 border-t border-border/50 px-3 py-2">
          <Button
            variant="ghost"
            size="sm"
            className="h-8 gap-1 px-2 text-xs"
            onClick={onPrevFile}
            disabled={!canGoPrev}
            aria-label="Previous file"
          >
            <ChevronLeft className="h-3.5 w-3.5" />
            Prev
          </Button>
          <Button
            variant="ghost"
            size="sm"
            className="h-8 gap-1 px-2 text-xs"
            onClick={onNextFile}
            disabled={!canGoNext}
            aria-label="Next file"
          >
            Next
            <ChevronRight className="h-3.5 w-3.5" />
          </Button>
          <div className="ml-auto flex items-center gap-1">
            {onSearchChange ? (
              <Button
                variant="ghost"
                size="sm"
                className={cn("h-8 w-8 p-0", showSearch && "text-primary")}
                onClick={() => {
                  setShowSearch((v) => !v);
                  if (showSearch) {
                    onSearchChange("");
                  }
                }}
                title="Search in diff"
              >
                <Search className="h-3.5 w-3.5" />
              </Button>
            ) : null}
            {onBrowseRepo ? (
              <Button
                variant="ghost"
                size="sm"
                className="h-8 gap-1 px-2 text-xs"
                onClick={onBrowseRepo}
                title="Browse repository"
              >
                <FolderSearch className="h-3.5 w-3.5" />
                Browse
              </Button>
            ) : null}
          </div>
        </div>

        {showSearch && onSearchChange ? (
          <div className="flex items-center gap-2 border-t border-border/50 bg-muted/20 px-3 py-1.5">
            <Search className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
            <input
              ref={searchInputRef}
              type="text"
              value={searchQuery ?? ""}
              onChange={(e) => onSearchChange(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Escape") {
                  setShowSearch(false);
                  onSearchChange("");
                }
              }}
              placeholder="Search in diff..."
              className="flex-1 bg-transparent text-xs text-foreground placeholder:text-muted-foreground/60 outline-none"
            />
            {searchQuery ? (
              <button
                onClick={() => onSearchChange("")}
                className="text-muted-foreground hover:text-foreground"
              >
                <X className="h-3 w-3" />
              </button>
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
          <input
            ref={searchInputRef}
            type="text"
            value={searchQuery ?? ""}
            onChange={(e) => onSearchChange(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                setShowSearch(false);
                onSearchChange("");
              }
            }}
            placeholder="Search in diff..."
            className="flex-1 bg-transparent text-xs text-foreground placeholder:text-muted-foreground/60 outline-none"
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
