import { describe, it, expect, vi, type Mock } from "vitest";
import { renderHook, act } from "@testing-library/react";
import type { ChangeEvent } from "react";
import { useAutosaveNumericField } from "./useAutosaveNumericField";
import type { UseAutosaveResult } from "./useAutosave";

function makeAutosaveStub<TVars>(): UseAutosaveResult<TVars> & {
  save: Mock<(vars: TVars) => void>;
} {
  return {
    save: vi.fn<(vars: TVars) => void>(),
    flush: vi.fn<() => void>(),
    status: "idle",
    debounceMs: 0,
  };
}

function changeEvent(value: string): ChangeEvent<HTMLInputElement> {
  return { target: { value } } as ChangeEvent<HTMLInputElement>;
}

describe("useAutosaveNumericField", () => {
  it("resets to the server value and does not save when blurred with empty input", () => {
    const autosave = makeAutosaveStub<{ settings: { n: number } }>();
    const { result } = renderHook(() =>
      useAutosaveNumericField({
        serverValue: 5,
        autosave,
        toPatch: (n) => ({ settings: { n } }),
      }),
    );

    act(() => {
      result.current.onChange(changeEvent(""));
    });
    expect(result.current.value).toBe("");

    act(() => {
      result.current.onBlur();
    });

    expect(result.current.value).toBe("5");
    expect(autosave.save).not.toHaveBeenCalled();
  });

  it("commits a clamped value on blur when input is in range", () => {
    const autosave = makeAutosaveStub<{ settings: { n: number } }>();
    const { result } = renderHook(() =>
      useAutosaveNumericField({
        serverValue: 5,
        autosave,
        toPatch: (n) => ({ settings: { n } }),
        clamp: (v) => Math.min(10, Math.max(1, v)),
      }),
    );

    act(() => {
      result.current.onChange(changeEvent("99"));
    });
    act(() => {
      result.current.onBlur();
    });

    expect(result.current.value).toBe("10");
    expect(autosave.save).toHaveBeenCalledWith({ settings: { n: 10 } });
  });

  it("dispatches using the latest toPatch closure when it changes after the debounce was armed", () => {
    vi.useFakeTimers();
    try {
      const autosave = makeAutosaveStub<{ settings: { n: number; tag: string } }>();
      let tag = "v1";
      const { result, rerender } = renderHook(() =>
        useAutosaveNumericField({
          serverValue: 5,
          autosave,
          toPatch: (n) => ({ settings: { n, tag } }),
        }),
      );

      act(() => {
        result.current.onChange(changeEvent("7"));
      });

      // Caller swaps the `toPatch` closure — e.g. because a sibling field was
      // optimistically updated and the component rerendered with a fresh
      // snapshot — before the debounce timer fires.
      tag = "v2";
      rerender();

      act(() => {
        vi.advanceTimersByTime(400);
      });

      expect(autosave.save).toHaveBeenCalledTimes(1);
      expect(autosave.save).toHaveBeenCalledWith({ settings: { n: 7, tag: "v2" } });
    } finally {
      vi.useRealTimers();
    }
  });

  it("resets to the server value when blurred with non-numeric garbage", () => {
    const autosave = makeAutosaveStub<{ settings: { n: number } }>();
    const { result } = renderHook(() =>
      useAutosaveNumericField({
        serverValue: 7,
        autosave,
        toPatch: (n) => ({ settings: { n } }),
      }),
    );

    act(() => {
      result.current.onChange(changeEvent("abc"));
    });
    act(() => {
      result.current.onBlur();
    });

    expect(result.current.value).toBe("7");
    expect(autosave.save).not.toHaveBeenCalled();
  });

  it("drops a pending edit on unmount by default", async () => {
    const autosave = makeAutosaveStub<{ settings: { n: number } }>();
    const { result, unmount } = renderHook(() =>
      useAutosaveNumericField({
        serverValue: 1,
        autosave,
        debounceMs: 5_000,
        toPatch: (n) => ({ settings: { n } }),
      }),
    );

    act(() => result.current.onChange(changeEvent("4")));
    unmount();

    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(autosave.save).not.toHaveBeenCalled();
  });

  it("commits a pending edit on unmount when flushOnUnmount is set", () => {
    // A numeric field inside a collapsible is unmounted without a focusout, so
    // onBlur never runs and the debounced edit would be silently dropped.
    const autosave = makeAutosaveStub<{ settings: { n: number } }>();
    const { result, unmount } = renderHook(() =>
      useAutosaveNumericField({
        serverValue: 1,
        autosave,
        debounceMs: 5_000,
        flushOnUnmount: true,
        clamp: (raw) => Math.min(5, Math.max(0, raw)),
        toPatch: (n) => ({ settings: { n } }),
      }),
    );

    act(() => result.current.onChange(changeEvent("9")));
    unmount();

    expect(autosave.save).toHaveBeenCalledTimes(1);
    // Clamped on the way out, exactly as the debounced path would have.
    expect(autosave.save).toHaveBeenCalledWith({ settings: { n: 5 } });
  });

  it("does not flush on unmount when nothing is pending", () => {
    const autosave = makeAutosaveStub<{ settings: { n: number } }>();
    const { unmount } = renderHook(() =>
      useAutosaveNumericField({
        serverValue: 1,
        autosave,
        flushOnUnmount: true,
        toPatch: (n) => ({ settings: { n } }),
      }),
    );

    unmount();

    expect(autosave.save).not.toHaveBeenCalled();
  });

  it("cancels the queued save when the box is cleared after typing", async () => {
    // All three exits from this state have to agree. Previously the debounce
    // timer (and therefore the unmount flush) persisted the digit the user had
    // just deleted, while onBlur reset to the server value and saved nothing.
    const autosave = makeAutosaveStub<{ settings: { n: number } }>();
    const { result, unmount } = renderHook(() =>
      useAutosaveNumericField({
        serverValue: 1,
        autosave,
        debounceMs: 20,
        flushOnUnmount: true,
        toPatch: (n) => ({ settings: { n } }),
      }),
    );

    act(() => result.current.onChange(changeEvent("4")));
    act(() => result.current.onChange(changeEvent("")));

    await new Promise((resolve) => setTimeout(resolve, 80));
    expect(autosave.save).not.toHaveBeenCalled();

    unmount();
    expect(autosave.save).not.toHaveBeenCalled();
  });

  it("cancels the queued save when the box is garbled after typing", async () => {
    const autosave = makeAutosaveStub<{ settings: { n: number } }>();
    const { result } = renderHook(() =>
      useAutosaveNumericField({
        serverValue: 1,
        autosave,
        debounceMs: 20,
        toPatch: (n) => ({ settings: { n } }),
      }),
    );

    act(() => result.current.onChange(changeEvent("4")));
    act(() => result.current.onChange(changeEvent("abc")));

    await new Promise((resolve) => setTimeout(resolve, 80));
    expect(autosave.save).not.toHaveBeenCalled();
  });

  it("does not flush on unmount after the stepper already saved the value", () => {
    const autosave = makeAutosaveStub<{ settings: { n: number } }>();
    const { result, unmount } = renderHook(() =>
      useAutosaveNumericField({
        serverValue: 1,
        autosave,
        debounceMs: 5_000,
        flushOnUnmount: true,
        toPatch: (n) => ({ settings: { n } }),
      }),
    );

    act(() => result.current.setValueAndSave(3));
    unmount();

    expect(autosave.save).toHaveBeenCalledTimes(1);
  });
});
