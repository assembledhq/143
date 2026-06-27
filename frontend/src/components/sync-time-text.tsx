"use client";

import { useEffect, useMemo, useState } from "react";

import { cn, formatDateTime, formatTimeAgo } from "@/lib/utils";

type SyncTimeTextProps = {
  syncedAt?: string | null;
  prefix?: string;
  fallback?: string;
  className?: string;
};

type RelativeSyncLabel = {
  label: string;
  nextUpdateInMs: number | null;
  title?: string;
  includePrefix: boolean;
};

export function SyncTimeText({
  syncedAt,
  prefix,
  fallback = "Syncing",
  className,
}: SyncTimeTextProps) {
  const [nowMs, setNowMs] = useState(() => Date.now());
  const relativeLabel = useMemo(
    () => formatRelativeSyncLabel(syncedAt, fallback, nowMs),
    [fallback, nowMs, syncedAt],
  );

  useEffect(() => {
    if (relativeLabel.nextUpdateInMs === null) {
      return;
    }

    const timeoutId = window.setTimeout(() => {
      setNowMs(Date.now());
    }, relativeLabel.nextUpdateInMs);

    return () => window.clearTimeout(timeoutId);
  }, [relativeLabel.nextUpdateInMs]);

  return (
    <span
      className={cn("text-xs text-muted-foreground", className)}
      title={relativeLabel.title}
    >
      {prefix && relativeLabel.includePrefix ? `${prefix} ${relativeLabel.label}` : relativeLabel.label}
    </span>
  );
}

function formatRelativeSyncLabel(syncedAt: string | null | undefined, fallback: string, nowMs: number): RelativeSyncLabel {
  if (!syncedAt) {
    return { label: fallback, nextUpdateInMs: null, includePrefix: false };
  }

  const syncedDate = new Date(syncedAt);
  if (Number.isNaN(syncedDate.getTime())) {
    return { label: fallback, nextUpdateInMs: null, includePrefix: false };
  }

  const diffMs = Math.max(0, nowMs - syncedDate.getTime());
  const title = formatDateTime(syncedAt);
  const label = formatTimeAgo(syncedAt, { fallback, includeSeconds: true, nowMs });

  if (diffMs < 60_000) {
    return {
      label,
      nextUpdateInMs: millisecondsUntilNextBucket(diffMs, 5000),
      title,
      includePrefix: true,
    };
  }

  if (diffMs < 3_600_000) {
    return {
      label,
      nextUpdateInMs: millisecondsUntilNextBucket(diffMs, 60000),
      title,
      includePrefix: true,
    };
  }

  if (diffMs < 86_400_000) {
    return {
      label,
      nextUpdateInMs: millisecondsUntilNextBucket(diffMs, 3600000),
      title,
      includePrefix: true,
    };
  }

  if (diffMs < 2_592_000_000) {
    return {
      label,
      nextUpdateInMs: millisecondsUntilNextBucket(diffMs, 86400000),
      title,
      includePrefix: true,
    };
  }

  return {
    label,
    nextUpdateInMs: null,
    title,
    includePrefix: true,
  };
}

function millisecondsUntilNextBucket(diffMs: number, bucketMs: number) {
  const remainder = diffMs % bucketMs;
  return remainder === 0 ? bucketMs : bucketMs - remainder;
}
