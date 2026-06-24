import { describe, expect, it, vi } from "vitest";

import { getClipboardFiles } from "./clipboard-files";

describe("getClipboardFiles", () => {
  it("returns an empty array without transfer data", () => {
    expect(getClipboardFiles(null)).toEqual([]);
    expect(getClipboardFiles(undefined)).toEqual([]);
  });

  it("prefers file items from the transfer item list", () => {
    const file = new File(["image"], "image.png", { type: "image/png" });
    const fallback = new File(["fallback"], "fallback.png", { type: "image/png" });
    const data = {
      items: [
        { kind: "string", getAsFile: vi.fn(() => null) },
        { kind: "file", getAsFile: vi.fn(() => file) },
      ],
      files: [fallback],
    } as unknown as DataTransfer;

    expect(getClipboardFiles(data)).toEqual([file]);
  });

  it("falls back to the transfer file list when item files are unavailable", () => {
    const fallback = new File(["fallback"], "fallback.png", { type: "image/png" });
    const data = {
      items: [{ kind: "file", getAsFile: vi.fn(() => null) }],
      files: [fallback],
    } as unknown as DataTransfer;

    expect(getClipboardFiles(data)).toEqual([fallback]);
  });
});
