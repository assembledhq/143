"use client";

import { useCallback, useLayoutEffect, useRef, type RefObject } from "react";
import { recordSessionActivityEvent } from "@/lib/session-activity-events";
import type { SessionActivityDetail } from "@/lib/types";

interface UseTranscriptPrependCompensationOptions {
  scrollContainerRef: RefObject<HTMLElement | null>;
  isFetching: boolean;
  contentVersion: number;
  detail: SessionActivityDetail;
}

export function useTranscriptPrependCompensation({
  scrollContainerRef,
  isFetching,
  contentVersion,
  detail,
}: UseTranscriptPrependCompensationOptions) {
  const snapshotRef = useRef<{ scrollHeight: number; scrollTop: number } | null>(null);

  const capturePrependPosition = useCallback(() => {
    const element = scrollContainerRef.current;
    if (!element) return;
    snapshotRef.current = {
      scrollHeight: element.scrollHeight,
      scrollTop: element.scrollTop,
    };
  }, [scrollContainerRef]);

  useLayoutEffect(() => {
    const snapshot = snapshotRef.current;
    const element = scrollContainerRef.current;
    if (!snapshot || !element || isFetching) return;

    snapshotRef.current = null;
    const requestedScrollTop = snapshot.scrollTop + (element.scrollHeight - snapshot.scrollHeight);
    const expectedScrollTop = Math.max(
      0,
      Math.min(requestedScrollTop, element.scrollHeight - element.clientHeight),
    );
    element.scrollTop = expectedScrollTop;
    window.requestAnimationFrame(() => {
      if (Math.abs(element.scrollTop - expectedScrollTop) < 48) return;
      recordSessionActivityEvent({
        event: "unexpected_scroll_delta",
        detail,
        trigger: "manual",
        viewport_class: window.innerWidth < 768 ? "mobile" : "desktop",
      });
    });
  }, [contentVersion, detail, isFetching, scrollContainerRef]);

  return capturePrependPosition;
}
