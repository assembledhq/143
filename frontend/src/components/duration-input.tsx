"use client";

import { useEffect, useRef, useState } from "react";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";

export type DurationUnit = "seconds" | "minutes" | "hours";

const UNIT_SECONDS: Record<DurationUnit, number> = {
  seconds: 1,
  minutes: 60,
  hours: 60 * 60,
};

const UNIT_LABELS: Record<DurationUnit, string> = {
  seconds: "Seconds",
  minutes: "Minutes",
  hours: "Hours",
};

export interface DurationInputProps {
  label: string;
  valueSeconds: number;
  onChangeSeconds: (seconds: number) => void;
  minSeconds?: number;
  maxSeconds?: number;
  disabled?: boolean;
  defaultUnit?: DurationUnit;
  debounceMs?: number;
  className?: string;
}

function clampSeconds(value: number, minSeconds?: number, maxSeconds?: number): number {
  const minClamped = minSeconds === undefined ? value : Math.max(minSeconds, value);
  return maxSeconds === undefined ? minClamped : Math.min(maxSeconds, minClamped);
}

function chooseDurationUnit(valueSeconds: number, defaultUnit: DurationUnit): DurationUnit {
  if (valueSeconds > 0 && valueSeconds % UNIT_SECONDS.hours === 0) return "hours";
  if (valueSeconds > 0 && valueSeconds % UNIT_SECONDS.minutes === 0) return "minutes";
  return defaultUnit;
}

function formatAmount(valueSeconds: number, unit: DurationUnit): string {
  const amount = valueSeconds / UNIT_SECONDS[unit];
  return Number.isInteger(amount) ? String(amount) : String(Number(amount.toFixed(2)));
}

function parseAmount(value: string): number | null {
  if (value.trim() === "") return null;
  const parsed = Number.parseFloat(value);
  return Number.isFinite(parsed) ? parsed : null;
}

export function DurationInput({
  label,
  valueSeconds,
  onChangeSeconds,
  minSeconds,
  maxSeconds,
  disabled,
  defaultUnit = "minutes",
  debounceMs = 400,
  className,
}: DurationInputProps) {
  const [unit, setUnit] = useState<DurationUnit>(() => chooseDurationUnit(valueSeconds, defaultUnit));
  const [amount, setAmount] = useState(() => formatAmount(valueSeconds, chooseDurationUnit(valueSeconds, defaultUnit)));
  const [trackedValueSeconds, setTrackedValueSeconds] = useState(valueSeconds);
  const [lastSentSeconds, setLastSentSeconds] = useState(valueSeconds);
  const debounceTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pendingSecondsRef = useRef<number | null>(null);
  const onChangeSecondsRef = useRef(onChangeSeconds);

  useEffect(() => {
    onChangeSecondsRef.current = onChangeSeconds;
  }, [onChangeSeconds]);

  if (valueSeconds !== trackedValueSeconds) {
    setTrackedValueSeconds(valueSeconds);
    const hasPendingEdit = amount !== formatAmount(lastSentSeconds, unit);
    if (valueSeconds !== lastSentSeconds && !hasPendingEdit) {
      const nextUnit = chooseDurationUnit(valueSeconds, defaultUnit);
      setUnit(nextUnit);
      setAmount(formatAmount(valueSeconds, nextUnit));
      setLastSentSeconds(valueSeconds);
    }
  }

  useEffect(() => {
    return () => {
      if (debounceTimerRef.current) {
        clearTimeout(debounceTimerRef.current);
        debounceTimerRef.current = null;
      }
    };
  }, []);

  const dispatch = (nextSeconds: number) => {
    const clamped = Math.round(clampSeconds(nextSeconds, minSeconds, maxSeconds));
    setLastSentSeconds(clamped);
    onChangeSecondsRef.current(clamped);
  };

  const scheduleDispatch = (nextSeconds: number) => {
    pendingSecondsRef.current = nextSeconds;
    if (debounceTimerRef.current) clearTimeout(debounceTimerRef.current);
    debounceTimerRef.current = setTimeout(() => {
      debounceTimerRef.current = null;
      const pending = pendingSecondsRef.current;
      pendingSecondsRef.current = null;
      if (pending !== null) dispatch(pending);
    }, debounceMs);
  };

  const flush = () => {
    if (debounceTimerRef.current) {
      clearTimeout(debounceTimerRef.current);
      debounceTimerRef.current = null;
    }
    const parsed = parseAmount(amount);
    if (parsed === null) {
      pendingSecondsRef.current = null;
      setAmount(formatAmount(valueSeconds, unit));
      setLastSentSeconds(valueSeconds);
      return;
    }
    const clamped = Math.round(clampSeconds(parsed * UNIT_SECONDS[unit], minSeconds, maxSeconds));
    pendingSecondsRef.current = null;
    setAmount(formatAmount(clamped, unit));
    if (clamped !== lastSentSeconds) dispatch(clamped);
  };

  const updateUnit = (nextUnit: DurationUnit) => {
    if (debounceTimerRef.current) {
      clearTimeout(debounceTimerRef.current);
      debounceTimerRef.current = null;
    }
    pendingSecondsRef.current = null;
    setUnit(nextUnit);
    const parsed = parseAmount(amount);
    if (parsed === null) {
      setAmount(formatAmount(valueSeconds, nextUnit));
      return;
    }
    const clamped = Math.round(clampSeconds(parsed * UNIT_SECONDS[nextUnit], minSeconds, maxSeconds));
    setAmount(formatAmount(clamped, nextUnit));
    if (clamped !== lastSentSeconds) dispatch(clamped);
  };

  return (
    <div className={cn("rounded-md border border-border p-4", className)}>
      <Label className="text-xs text-muted-foreground">{label}</Label>
      <div className="mt-2 grid grid-cols-[minmax(0,1fr)_8.5rem] gap-2">
        <Input
          aria-label={`${label} value`}
          type="number"
          min={minSeconds === undefined ? undefined : minSeconds / UNIT_SECONDS[unit]}
          max={maxSeconds === undefined ? undefined : maxSeconds / UNIT_SECONDS[unit]}
          step={unit === "seconds" ? 1 : 0.25}
          value={amount}
          disabled={disabled}
          onChange={(event) => {
            const nextAmount = event.target.value;
            setAmount(nextAmount);
            const parsed = parseAmount(nextAmount);
            if (parsed !== null) scheduleDispatch(parsed * UNIT_SECONDS[unit]);
          }}
          onBlur={flush}
        />
        <Select value={unit} disabled={disabled} onValueChange={(value) => updateUnit(value as DurationUnit)}>
          <SelectTrigger aria-label={`${label} unit`}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {Object.entries(UNIT_LABELS).map(([value, unitLabel]) => (
              <SelectItem key={value} value={value}>
                {unitLabel}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    </div>
  );
}
