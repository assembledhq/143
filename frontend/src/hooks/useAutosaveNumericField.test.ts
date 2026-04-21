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
});
