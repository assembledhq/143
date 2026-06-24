import { afterEach, describe, expect, it, vi } from "vitest";

import { canCopyToClipboard, copyTextToClipboard } from "./clipboard";

const originalClipboard = Object.getOwnPropertyDescriptor(navigator, "clipboard");

function setClipboard(value: Clipboard | undefined): void {
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value,
  });
}

describe("clipboard helpers", () => {
  afterEach(() => {
    if (originalClipboard) {
      Object.defineProperty(navigator, "clipboard", originalClipboard);
    } else {
      delete (navigator as unknown as Record<string, unknown>).clipboard;
    }
  });

  it("reports clipboard support when writeText exists", () => {
    setClipboard({ writeText: vi.fn() } as unknown as Clipboard);

    expect(canCopyToClipboard()).toBe(true);
  });

  it("reports clipboard support as unavailable without writeText", () => {
    setClipboard({} as Clipboard);

    expect(canCopyToClipboard()).toBe(false);
  });

  it("writes text through the Clipboard API", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    setClipboard({ writeText } as unknown as Clipboard);

    await expect(copyTextToClipboard("copy me")).resolves.toBeUndefined();
    expect(writeText).toHaveBeenCalledWith("copy me");
  });

  it("rejects when the Clipboard API is unavailable", async () => {
    setClipboard(undefined);

    await expect(copyTextToClipboard("copy me")).rejects.toThrow(
      "Clipboard API is unavailable",
    );
  });
});
