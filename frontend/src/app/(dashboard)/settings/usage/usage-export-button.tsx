"use client";

import { useState, useRef, useEffect, useCallback } from "react";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Label } from "@/components/ui/label";
import { Download } from "lucide-react";
import { api } from "@/lib/api";

interface UsageExportButtonProps {
  start: string;
  end: string;
  dimension?: "user" | "agent" | "model" | "reasoning";
  filters?: {
    agent?: string | null;
    model?: string | null;
    reasoning?: string | null;
  };
}

export function UsageExportButton({ start, end, dimension = "user", filters }: UsageExportButtonProps) {
  const [granularity, setGranularity] = useState<"daily" | "hourly">("daily");
  const [exportDimension, setExportDimension] = useState<"none" | "user" | "agent" | "model" | "reasoning">(dimension);
  const [showOptions, setShowOptions] = useState(false);
  const panelRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    setExportDimension(dimension);
  }, [dimension]);

  const handleExport = () => {
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
    const url = api.usage.getExportUrl({
      start,
      end,
      granularity,
      dimension: exportDimension,
      tz,
      ...(filters?.agent ? { agent: filters.agent } : {}),
      ...(filters?.model ? { model: filters.model } : {}),
      ...(filters?.reasoning ? { reasoning: filters.reasoning } : {}),
    });
    // Try window.open first; fall back to location.href for popup-blocked
    // browsers. Since this is a file download, location.href won't navigate away.
    const w = window.open(url, "_blank");
    if (!w) {
      window.location.href = url;
    }
  };

  const close = useCallback(() => {
    setShowOptions(false);
    triggerRef.current?.focus();
  }, []);

  // Close on Escape
  useEffect(() => {
    if (!showOptions) return;
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        close();
      }
    };
    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [showOptions, close]);

  // Focus the panel when opened
  useEffect(() => {
    if (showOptions) {
      panelRef.current?.focus();
    }
  }, [showOptions]);

  return (
    <div className="relative">
      <Button
        ref={triggerRef}
        variant="outline"
        size="sm"
        className="h-8 text-xs gap-1.5"
        onClick={() => setShowOptions(!showOptions)}
        aria-expanded={showOptions}
        aria-haspopup="dialog"
      >
        <Download className="h-3.5 w-3.5" />
        Export CSV
      </Button>

      {showOptions && (
        <>
          <div
            className="fixed inset-0 z-40"
            onClick={close}
            aria-hidden="true"
          />
          <div
            ref={panelRef}
            role="dialog"
            aria-label="Export options"
            tabIndex={-1}
            className="absolute right-0 top-full mt-1 z-50 w-56 rounded-lg border bg-background p-3 shadow-md space-y-3"
          >
            <div className="space-y-1.5">
              <Label className="text-xs font-medium text-muted-foreground">Granularity</Label>
              <Select value={granularity} onValueChange={(v) => setGranularity(v as "daily" | "hourly")}>
                <SelectTrigger className="h-8 text-xs">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="daily" className="text-xs">Daily</SelectItem>
                  <SelectItem value="hourly" className="text-xs">Hourly</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label className="text-xs font-medium text-muted-foreground">Breakdown</Label>
              <Select value={exportDimension} onValueChange={(v) => setExportDimension(v as "none" | "user" | "agent" | "model" | "reasoning")}>
                <SelectTrigger className="h-8 text-xs">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none" className="text-xs">Org totals</SelectItem>
                  <SelectItem value="user" className="text-xs">By User</SelectItem>
                  <SelectItem value="agent" className="text-xs">By Agent</SelectItem>
                  <SelectItem value="model" className="text-xs">By Model</SelectItem>
                  <SelectItem value="reasoning" className="text-xs">By Reasoning</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <Button
              size="sm"
              className="w-full h-8 text-xs"
              onClick={() => {
                handleExport();
                close();
              }}
            >
              Download
            </Button>
          </div>
        </>
      )}
    </div>
  );
}
