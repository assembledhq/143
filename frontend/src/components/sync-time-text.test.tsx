import { act } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { renderWithProviders, screen } from "@/test/test-utils";

import { SyncTimeText } from "./sync-time-text";

describe("SyncTimeText", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-04-29T12:00:00.000Z"));
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("renders seconds-ago copy for recent syncs and refreshes on a short cadence", () => {
    renderWithProviders(
      <SyncTimeText
        syncedAt="2026-04-29T11:59:50.000Z"
        prefix="Synced"
      />,
    );

    expect(screen.getByText("Synced 10s ago")).toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(5000);
    });

    expect(screen.getByText("Synced 15s ago")).toBeInTheDocument();
  });

  it("switches to minute-level copy and only refreshes when the minute boundary changes", () => {
    renderWithProviders(
      <SyncTimeText
        syncedAt="2026-04-29T11:58:50.000Z"
        prefix="Synced"
      />,
    );

    expect(screen.getByText("Synced 1m ago")).toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(49000);
    });

    expect(screen.getByText("Synced 1m ago")).toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(1000);
    });

    expect(screen.getByText("Synced 2m ago")).toBeInTheDocument();
  });

  it("falls back to syncing copy when no sync timestamp is available", () => {
    renderWithProviders(<SyncTimeText syncedAt={undefined} prefix="Synced" fallback="Syncing" />);

    expect(screen.getByText("Syncing")).toBeInTheDocument();
  });
});
