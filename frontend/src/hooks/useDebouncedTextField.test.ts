import { describe, it, expect, vi, type Mock } from "vitest";
import { renderHook, act, waitFor } from "@testing-library/react";
import { useDebouncedTextField } from "./useDebouncedTextField";

describe("useDebouncedTextField", () => {
  it("commits the typed value after the debounce window", async () => {
    const onCommit: Mock<(value: string) => void> = vi.fn();
    const { result } = renderHook(() =>
      useDebouncedTextField({ serverValue: "", onCommit, debounceMs: 30 }),
    );

    act(() => {
      result.current.onChange("hello");
    });

    expect(onCommit).not.toHaveBeenCalled();
    expect(result.current.value).toBe("hello");

    await waitFor(() => expect(onCommit).toHaveBeenCalledTimes(1));
    expect(onCommit).toHaveBeenCalledWith("hello");
  });

  it("commits immediately on blur and cancels the pending debounce", async () => {
    const onCommit: Mock<(value: string) => void> = vi.fn();
    const { result } = renderHook(() =>
      useDebouncedTextField({ serverValue: "", onCommit, debounceMs: 5_000 }),
    );

    act(() => {
      result.current.onChange("typed");
    });
    expect(onCommit).not.toHaveBeenCalled();

    act(() => {
      result.current.onBlur();
    });

    expect(onCommit).toHaveBeenCalledTimes(1);
    expect(onCommit).toHaveBeenCalledWith("typed");

    // Give the (now cancelled) debounce timer a chance to fire and confirm
    // it doesn't double-commit.
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(onCommit).toHaveBeenCalledTimes(1);
  });

  it("does not commit when blurring without a change to the value", () => {
    const onCommit: Mock<(value: string) => void> = vi.fn();
    const { result } = renderHook(() =>
      useDebouncedTextField({ serverValue: "same", onCommit, debounceMs: 30 }),
    );

    act(() => {
      result.current.onBlur();
    });

    expect(onCommit).not.toHaveBeenCalled();
  });

  it("coalesces successive keystrokes into one commit", async () => {
    const onCommit: Mock<(value: string) => void> = vi.fn();
    const { result } = renderHook(() =>
      useDebouncedTextField({ serverValue: "", onCommit, debounceMs: 30 }),
    );

    act(() => {
      result.current.onChange("a");
      result.current.onChange("ab");
      result.current.onChange("abc");
    });

    await waitFor(() => expect(onCommit).toHaveBeenCalledTimes(1));
    expect(onCommit).toHaveBeenCalledWith("abc");
  });

  it("resyncs local value from the server when no edit is pending", () => {
    const onCommit: Mock<(value: string) => void> = vi.fn();
    const { result, rerender } = renderHook(
      ({ serverValue }: { serverValue: string }) =>
        useDebouncedTextField({ serverValue, onCommit, debounceMs: 30 }),
      { initialProps: { serverValue: "initial" } },
    );

    expect(result.current.value).toBe("initial");

    rerender({ serverValue: "from-server" });

    // With no local divergence from lastSent, an incoming server change
    // overwrites the display so other-tab / refetch edits are reflected.
    expect(result.current.value).toBe("from-server");
    expect(onCommit).not.toHaveBeenCalled();
  });

  it("preserves the user's in-progress typing when the server value changes", () => {
    const onCommit: Mock<(value: string) => void> = vi.fn();
    const { result, rerender } = renderHook(
      ({ serverValue }: { serverValue: string }) =>
        useDebouncedTextField({ serverValue, onCommit, debounceMs: 5_000 }),
      { initialProps: { serverValue: "initial" } },
    );

    // User starts typing; debounce hasn't committed yet, so local !== lastSent.
    act(() => {
      result.current.onChange("half-typed");
    });
    expect(result.current.value).toBe("half-typed");

    // Server value changes mid-edit (e.g. a refetch). The local value must
    // NOT be clobbered — user intent wins.
    rerender({ serverValue: "from-server" });
    expect(result.current.value).toBe("half-typed");
  });

  it("doesn't re-commit a value that already matches the server", async () => {
    // serverValue === lastSent initially ("foo"). The user types "foo" again
    // (no change) — we should not dispatch a redundant commit.
    const onCommit: Mock<(value: string) => void> = vi.fn();
    const { result } = renderHook(() =>
      useDebouncedTextField({ serverValue: "foo", onCommit, debounceMs: 30 }),
    );

    act(() => {
      result.current.onChange("foo");
    });

    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(onCommit).not.toHaveBeenCalled();
  });

  it("never commits a rejected value and doesn't advance lastSent", async () => {
    const onCommit: Mock<(value: string) => void> = vi.fn();
    const { result } = renderHook(() =>
      useDebouncedTextField({
        serverValue: "Weekly audit",
        onCommit,
        debounceMs: 30,
        rejectValue: (value) => value.trim() === "",
      }),
    );

    act(() => {
      result.current.onChange("");
    });
    expect(result.current.value).toBe("");

    // The debounce fires but the rejected empty value is never committed and,
    // critically, lastSent is not poisoned to "".
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(onCommit).not.toHaveBeenCalled();
  });

  it("reverts a rejected value to the last committed value on blur", () => {
    const onCommit: Mock<(value: string) => void> = vi.fn();
    const { result } = renderHook(() =>
      useDebouncedTextField({
        serverValue: "Weekly audit",
        onCommit,
        debounceMs: 5_000,
        rejectValue: (value) => value.trim() === "",
      }),
    );

    act(() => {
      result.current.onChange("");
    });
    expect(result.current.value).toBe("");

    act(() => {
      result.current.onBlur();
    });

    // Blur snaps the required field back to the last saved value instead of
    // leaving it blank, and never commits the empty value.
    expect(result.current.value).toBe("Weekly audit");
    expect(onCommit).not.toHaveBeenCalled();
  });

  it("still commits a valid value after a rejected one was reverted", async () => {
    const onCommit: Mock<(value: string) => void> = vi.fn();
    const { result } = renderHook(() =>
      useDebouncedTextField({
        serverValue: "Weekly audit",
        onCommit,
        debounceMs: 30,
        rejectValue: (value) => value.trim() === "",
      }),
    );

    act(() => {
      result.current.onChange("");
    });
    act(() => {
      result.current.onBlur();
    });
    expect(result.current.value).toBe("Weekly audit");

    act(() => {
      result.current.onChange("Release audit");
    });
    await waitFor(() => expect(onCommit).toHaveBeenCalledTimes(1));
    expect(onCommit).toHaveBeenCalledWith("Release audit");
  });

  it("uses the latest onCommit closure at fire time", async () => {
    const first: Mock<(value: string) => void> = vi.fn();
    const second: Mock<(value: string) => void> = vi.fn();
    const { result, rerender } = renderHook(
      ({ onCommit }: { onCommit: (value: string) => void }) =>
        useDebouncedTextField({ serverValue: "", onCommit, debounceMs: 30 }),
      { initialProps: { onCommit: first as (value: string) => void } },
    );

    act(() => {
      result.current.onChange("draft");
    });

    // Parent re-renders with a new onCommit identity before the debounce
    // fires. The ref-mirror in the hook should pick up the latest closure.
    rerender({ onCommit: second as (value: string) => void });

    await waitFor(() => expect(second).toHaveBeenCalledTimes(1));
    expect(second).toHaveBeenCalledWith("draft");
    expect(first).not.toHaveBeenCalled();
  });
});
