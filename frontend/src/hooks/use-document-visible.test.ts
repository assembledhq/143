import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { useDocumentVisible } from "./use-document-visible";

const originalVisibilityState = Object.getOwnPropertyDescriptor(
  Document.prototype,
  "visibilityState",
);

function setVisibilityState(value: DocumentVisibilityState): void {
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => value,
  });
}

describe("useDocumentVisible", () => {
  afterEach(() => {
    delete (document as unknown as Record<string, unknown>).visibilityState;
    if (originalVisibilityState) {
      Object.defineProperty(Document.prototype, "visibilityState", originalVisibilityState);
    }
  });

  it("initializes from the current document visibility state", () => {
    setVisibilityState("hidden");

    const { result } = renderHook(() => useDocumentVisible());

    expect(result.current).toBe(false);
  });

  it("updates when document visibility changes", () => {
    setVisibilityState("visible");
    const { result } = renderHook(() => useDocumentVisible());

    expect(result.current).toBe(true);

    setVisibilityState("hidden");
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });

    expect(result.current).toBe(false);
  });
});
