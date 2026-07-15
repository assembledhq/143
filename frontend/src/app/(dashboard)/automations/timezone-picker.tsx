"use client";

import { useMemo, useState } from "react";
import { ChevronDown } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Command,
  CommandCheckItem,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandList,
} from "@/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";

// supportedTimezones lazily queries Intl.supportedValuesOf. Older Safari (<15.4)
// omits the API; we fall back to a curated list of common zones plus the
// viewer's detected zone so the picker is still usable. The resolver is lazy
// so SSR and first render pay nothing.
let supportedTimezonesCache: string[] | null = null;
function supportedTimezones(detected: string): string[] {
  if (supportedTimezonesCache) return supportedTimezonesCache;
  const fallback = [
    "UTC",
    "America/Los_Angeles",
    "America/Denver",
    "America/Chicago",
    "America/New_York",
    "America/Sao_Paulo",
    "Europe/London",
    "Europe/Paris",
    "Europe/Berlin",
    "Europe/Moscow",
    "Asia/Dubai",
    "Asia/Kolkata",
    "Asia/Singapore",
    "Asia/Tokyo",
    "Australia/Sydney",
    "Pacific/Auckland",
  ];
  try {
    const supportedValuesOf = (
      Intl as unknown as {
        supportedValuesOf?: (key: string) => string[];
      }
    ).supportedValuesOf;
    const list = supportedValuesOf?.("timeZone");
    supportedTimezonesCache = list && list.length > 0 ? list : fallback;
  } catch {
    supportedTimezonesCache = fallback;
  }
  // Always surface the detected zone even if it's an alias the browser omits.
  if (!supportedTimezonesCache.includes(detected)) {
    supportedTimezonesCache = [detected, ...supportedTimezonesCache];
  }
  return supportedTimezonesCache;
}

interface TimezonePickerProps {
  value: string;
  onChange: (value: string) => void;
  detected: string;
  className?: string;
}

export function TimezonePicker({
  value,
  onChange,
  detected,
  className,
}: TimezonePickerProps) {
  const [open, setOpen] = useState(false);
  // When the Detected group is rendered, the detected zone also appears in
  // the main list (supportedTimezones guarantees it). Strip it from the main
  // list so the dropdown doesn't show the same row twice.
  const options = useMemo(() => {
    const all = supportedTimezones(detected);
    if (detected && detected !== value) {
      return all.filter((tz) => tz !== detected);
    }
    return all;
  }, [detected, value]);
  const showDetectedGroup = detected && detected !== value;

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="outline"
          role="combobox"
          aria-expanded={open}
          // Match the sibling SelectTrigger so the trio in
          // the schedule row lines up visually.
          className={cn("h-9 min-w-0 justify-between type-dense max-sm:text-base font-normal", className)}
          title={value}
        >
          <span className="min-w-0 max-w-[180px] truncate">{value}</span>
          <ChevronDown className="ml-2 h-3.5 w-3.5 shrink-0 opacity-60" />
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-72 p-0" align="start">
        <Command>
          <CommandInput placeholder="Search timezone..." />
          <CommandList>
            <CommandEmpty>No timezone found.</CommandEmpty>
            {showDetectedGroup && (
              <CommandGroup heading="Detected">
                <CommandCheckItem
                  checked={detected === value}
                  // cmdk filters by `value`; include a keyword variant with
                  // spaces so "new york" matches "America/New_York".
                  value={`${detected} ${detected.replace(/_/g, " ")}`}
                  onSelect={() => {
                    onChange(detected);
                    setOpen(false);
                  }}
                >
                  {detected}{" "}
                  <span className="ml-2 text-xs text-muted-foreground">
                    (browser)
                  </span>
                </CommandCheckItem>
              </CommandGroup>
            )}
            <CommandGroup>
              {options.map((tz) => (
                <CommandCheckItem
                  key={tz}
                  checked={tz === value}
                  value={`${tz} ${tz.replace(/_/g, " ")}`}
                  onSelect={() => {
                    onChange(tz);
                    setOpen(false);
                  }}
                >
                  {tz}
                </CommandCheckItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
