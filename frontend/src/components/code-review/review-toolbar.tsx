"use client";

import { useState, useRef, useEffect } from "react";
import { Columns2, Rows3, Maximize2, Minimize2, PanelLeftClose, PanelLeft, FolderSearch, Search, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { PassSelector, type DiffPassEntry, type PassRange } from "./pass-selector";

export type ViewMode = "unified" | "split";

interface ReviewToolbarProps {
  viewMode: ViewMode;
  onViewModeChange: (mode: ViewMode) => void;
  maximized: boolean;
  onToggleMaximize: () => void;
  showFileTree: boolean;
  onToggleFileTree: () => void;
  onBrowseRepo?: () => void;
  passes?: DiffPassEntry[];
  selectedPassRange?: PassRange | null;
  onPassRangeChange?: (range: PassRange | null) => void;
  searchQuery?: string;
  onSearchChange?: (query: string) => void;
}

export function ReviewToolbar({
  viewMode,
  onViewModeChange,
  maximized,
  onToggleMaximize,
  showFileTree,
  onToggleFileTree,
  onBrowseRepo,
  passes,
  selectedPassRange,
  onPassRangeChange,
  searchQuery,
  onSearchChange,
}: ReviewToolbarProps) {
  const [showSearch, setShowSearch] = useState(false);
  const searchInputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (showSearch) {
      searchInputRef.current?.focus();
    }
  }, [showSearch]);

  return (
    <div className="flex flex-col border-b border-border bg-surface-raised">
      <div className="flex items-center justify-between px-3 py-1.5">
        <div className="flex items-center gap-1">
          <Button
            variant="ghost"
            size="sm"
            className="h-7 w-7 p-0"
            onClick={onToggleFileTree}
            title={showFileTree ? "Hide file tree" : "Show file tree"}
          >
            {showFileTree ? (
              <PanelLeftClose className="h-3.5 w-3.5" />
            ) : (
              <PanelLeft className="h-3.5 w-3.5" />
            )}
          </Button>

          {passes && passes.length >= 2 && onPassRangeChange && (
            <PassSelector
              passes={passes}
              selectedRange={selectedPassRange ?? null}
              onRangeChange={onPassRangeChange}
            />
          )}
        </div>

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
        <div className="flex items-center rounded-md border border-border bg-surface-pane">
          <button
            onClick={() => onViewModeChange("unified")}
            className={cn(
              "flex items-center gap-1 px-2 py-1 text-xs rounded-l-md transition-colors",
              viewMode === "unified"
                ? "bg-surface-raised text-foreground shadow-sm"
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
                ? "bg-surface-raised text-foreground shadow-sm"
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

        <Button
          variant="ghost"
          size="sm"
          className="h-7 w-7 p-0"
          onClick={onToggleMaximize}
          title={maximized ? "Restore" : "Maximize"}
        >
          {maximized ? (
            <Minimize2 className="h-3.5 w-3.5" />
          ) : (
            <Maximize2 className="h-3.5 w-3.5" />
          )}
        </Button>
        </div>
      </div>

      {/* Search bar */}
      {showSearch && onSearchChange && (
        <div className="flex items-center gap-2 px-3 py-1.5 border-t border-border/50 bg-surface-pane/70">
          <Search className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
          <Input
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
            className="h-7 flex-1 border-none bg-transparent px-0 py-0 text-xs text-foreground shadow-none placeholder:text-muted-foreground/60 focus-visible:ring-0"
          />
          {searchQuery && (
            <Button
              type="button"
              variant="ghost"
              size="icon"
              onClick={() => onSearchChange("")}
              className="h-5 w-5 text-muted-foreground hover:text-foreground"
              aria-label="Clear diff search"
            >
              <X className="h-3 w-3" />
            </Button>
          )}
        </div>
      )}
    </div>
  );
}
