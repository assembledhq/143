"use client";

import { ChevronDown, GitCompare } from "lucide-react";
import { useState, useRef, useEffect, useCallback } from "react";
import { cn } from "@/lib/utils";

export interface DiffPassEntry {
  pass: number;
  diff: string;
  diff_stats: { added: number; removed: number; files_changed: number };
  created_at: string;
}

export interface PassRange {
  from: number;
  to: number;
}

interface PassSelectorProps {
  passes: DiffPassEntry[];
  selectedRange: PassRange | null;
  onRangeChange: (range: PassRange | null) => void;
}

function formatPassLabel(range: PassRange | null): string {
  if (!range) return "All changes";
  if (range.from === 0) return `Base → Pass ${range.to}`;
  return `Pass ${range.from} → Pass ${range.to}`;
}

function formatRelativeTime(dateStr: string): string {
  const now = Date.now();
  const then = new Date(dateStr).getTime();
  const diffMs = now - then;
  const diffMins = Math.floor(diffMs / 60000);
  if (diffMins < 1) return "just now";
  if (diffMins < 60) return `${diffMins}m ago`;
  const diffHours = Math.floor(diffMins / 60);
  if (diffHours < 24) return `${diffHours}h ago`;
  return `${Math.floor(diffHours / 24)}d ago`;
}

export function PassSelector({
  passes,
  selectedRange,
  onRangeChange,
}: PassSelectorProps) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  // Close on outside click
  useEffect(() => {
    if (!open) return;
    function handleClick(e: MouseEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) {
        setOpen(false);
      }
    }
    document.addEventListener("mousedown", handleClick);
    return () => document.removeEventListener("mousedown", handleClick);
  }, [open]);

  const handleSelect = useCallback(
    (range: PassRange | null) => {
      onRangeChange(range);
      setOpen(false);
    },
    [onRangeChange]
  );

  if (passes.length < 2) return null;

  const label = formatPassLabel(selectedRange);

  // Build available ranges:
  // - "All changes" (null) — shows the latest full diff
  // - Each consecutive pair: Pass N → Pass N+1
  // - Base → each pass (for single-pass view)
  const ranges: { range: PassRange | null; label: string; description: string }[] = [
    {
      range: null,
      label: "All changes",
      description: "Latest full diff against base",
    },
  ];

  // Add consecutive pass-to-pass ranges (most useful for review)
  for (let i = 0; i < passes.length - 1; i++) {
    const from = passes[i];
    const to = passes[i + 1];
    ranges.push({
      range: { from: from.pass, to: to.pass },
      label: `Pass ${from.pass} → Pass ${to.pass}`,
      description: `${formatRelativeTime(to.created_at)} · +${to.diff_stats.added} / -${to.diff_stats.removed}`,
    });
  }

  // Add "Base → Pass N" for jumping to a specific pass
  if (passes.length > 2) {
    for (const p of passes) {
      ranges.push({
        range: { from: 0, to: p.pass },
        label: `Base → Pass ${p.pass}`,
        description: `+${p.diff_stats.added} / -${p.diff_stats.removed} · ${p.diff_stats.files_changed} files`,
      });
    }
  }

  return (
    <div ref={ref} className="relative">
      <button
        onClick={() => setOpen(!open)}
        className={cn(
          "flex items-center gap-1.5 px-2 py-1 text-xs rounded-md border border-border transition-colors",
          selectedRange
            ? "bg-surface-selected text-primary border-primary/20"
            : "bg-surface-pane text-muted-foreground hover:text-foreground"
        )}
        title="Compare between passes"
      >
        <GitCompare className="h-3 w-3" />
        <span className="font-medium">{label}</span>
        <ChevronDown className="h-3 w-3" />
      </button>

      {open && (
        <div className="absolute top-full left-0 mt-1 z-50 min-w-[240px] bg-surface-raised border border-border rounded-lg shadow-lg overflow-hidden">
          <div className="px-3 py-1.5 text-xs font-medium text-muted-foreground uppercase tracking-wider border-b border-border/50">
            Compare passes
          </div>
          <div className="max-h-[300px] overflow-y-auto py-1">
            {ranges.map((item, i) => {
              const isActive =
                item.range === null
                  ? selectedRange === null
                  : selectedRange !== null &&
                    selectedRange.from === item.range.from &&
                    selectedRange.to === item.range.to;

              return (
                <button
                  key={i}
                  onClick={() => handleSelect(item.range)}
                  className={cn(
                    "w-full text-left px-3 py-1.5 text-xs hover:bg-surface-hover transition-colors",
                    isActive && "bg-surface-selected text-primary"
                  )}
                >
                  <div className="font-medium">{item.label}</div>
                  <div className="text-xs text-muted-foreground">
                    {item.description}
                  </div>
                </button>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
}
