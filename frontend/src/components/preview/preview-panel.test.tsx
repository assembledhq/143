import { describe, expect, it } from "vitest";
import {
  buildPreviewIframeSrc,
  PREVIEW_BOOTSTRAP_READY_EVENT,
  PREVIEW_BOOTSTRAP_TOKEN_EVENT,
} from "./preview-panel";

describe("PreviewPanel bootstrap helpers", () => {
  it("points ready previews at the bootstrap path", () => {
    const src = buildPreviewIframeSrc("https://abc.preview.143.dev");
    expect(src).toBe("https://abc.preview.143.dev/bootstrap");
  });

  it("uses the gateway bootstrap message names", () => {
    expect(PREVIEW_BOOTSTRAP_READY_EVENT).toBe("preview_bootstrap_ready");
    expect(PREVIEW_BOOTSTRAP_TOKEN_EVENT).toBe("preview_bootstrap_token");
  });
});
