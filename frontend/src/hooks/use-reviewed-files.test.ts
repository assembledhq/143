import { describe, it, expect, beforeEach } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { useReviewedFiles } from "./use-reviewed-files";

const STORAGE_KEY_PREFIX = "diff-reviewed-files";

describe("useReviewedFiles", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("returns empty set initially", () => {
    const { result } = renderHook(() => useReviewedFiles("session-1"));
    expect(result.current.reviewedFiles.size).toBe(0);
  });

  it("toggles a file as reviewed", () => {
    const { result } = renderHook(() => useReviewedFiles("session-1"));
    act(() => {
      result.current.toggleReviewed("src/app.ts");
    });
    expect(result.current.reviewedFiles.has("src/app.ts")).toBe(true);
  });

  it("toggles off on second call", () => {
    const { result } = renderHook(() => useReviewedFiles("session-1"));
    act(() => {
      result.current.toggleReviewed("src/app.ts");
    });
    expect(result.current.reviewedFiles.has("src/app.ts")).toBe(true);
    act(() => {
      result.current.toggleReviewed("src/app.ts");
    });
    expect(result.current.reviewedFiles.has("src/app.ts")).toBe(false);
  });

  it("persists to localStorage", () => {
    const { result } = renderHook(() => useReviewedFiles("session-1"));
    act(() => {
      result.current.toggleReviewed("src/app.ts");
    });
    const stored = localStorage.getItem(`${STORAGE_KEY_PREFIX}:session-1`);
    expect(stored).toBeTruthy();
    const parsed = JSON.parse(stored!) as string[];
    expect(parsed).toContain("src/app.ts");
  });

  it("loads from localStorage on mount", () => {
    localStorage.setItem(
      `${STORAGE_KEY_PREFIX}:session-2`,
      JSON.stringify(["src/old.ts"])
    );
    const { result } = renderHook(() => useReviewedFiles("session-2"));
    expect(result.current.reviewedFiles.has("src/old.ts")).toBe(true);
  });

  it("uses separate storage per session ID", () => {
    const { result: r1 } = renderHook(() => useReviewedFiles("session-a"));
    const { result: r2 } = renderHook(() => useReviewedFiles("session-b"));
    act(() => {
      r1.current.toggleReviewed("file1.ts");
    });
    expect(r1.current.reviewedFiles.has("file1.ts")).toBe(true);
    expect(r2.current.reviewedFiles.has("file1.ts")).toBe(false);
  });
});
