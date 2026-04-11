"use client";

import { useState } from "react";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Download } from "lucide-react";
import { api } from "@/lib/api";

interface UsageExportButtonProps {
  start: string;
  end: string;
}

export function UsageExportButton({ start, end }: UsageExportButtonProps) {
  const [granularity, setGranularity] = useState<"daily" | "hourly">("daily");
  const [dimension, setDimension] = useState<"none" | "user" | "capacity">("none");
  const [showOptions, setShowOptions] = useState(false);

  const handleExport = () => {
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
    const url = api.usage.getExportUrl({ start, end, granularity, dimension, tz });
    window.open(url, "_blank");
  };

  return (
    <div className="relative">
      <Button
        variant="outline"
        size="sm"
        className="h-8 text-xs gap-1.5"
        onClick={() => setShowOptions(!showOptions)}
      >
        <Download className="h-3.5 w-3.5" />
        Export CSV
      </Button>

      {showOptions && (
        <>
          <div
            className="fixed inset-0 z-40"
            onClick={() => setShowOptions(false)}
          />
          <div className="absolute right-0 top-full mt-1 z-50 w-56 rounded-lg border bg-background p-3 shadow-md space-y-3">
            <div className="space-y-1.5">
              <label className="text-xs font-medium text-muted-foreground">Granularity</label>
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
              <label className="text-xs font-medium text-muted-foreground">Breakdown</label>
              <Select value={dimension} onValueChange={(v) => setDimension(v as "none" | "user" | "capacity")}>
                <SelectTrigger className="h-8 text-xs">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none" className="text-xs">Org totals</SelectItem>
                  <SelectItem value="user" className="text-xs">By User</SelectItem>
                  <SelectItem value="capacity" className="text-xs">By Capacity</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <Button
              size="sm"
              className="w-full h-8 text-xs"
              onClick={() => {
                handleExport();
                setShowOptions(false);
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
