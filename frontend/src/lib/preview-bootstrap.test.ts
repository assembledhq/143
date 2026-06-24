import { describe, expect, it } from "vitest";

import {
  PREVIEW_BOOTSTRAP_TIMEOUT_ERROR,
  buildPreviewBootstrapSrc,
  previewBootstrapTimeoutDetails,
  previewOriginFromURL,
} from "./preview-bootstrap";

describe("previewOriginFromURL", () => {
  it("returns the origin for HTTPS preview URLs", () => {
    expect(previewOriginFromURL("https://abc.preview.143.dev/path?x=1")).toBe(
      "https://abc.preview.143.dev",
    );
  });

  it("rejects non-HTTPS and malformed URLs", () => {
    expect(previewOriginFromURL("http://abc.preview.localhost:9090")).toBeUndefined();
    expect(previewOriginFromURL("not a url")).toBeUndefined();
  });
});

describe("buildPreviewBootstrapSrc", () => {
  it("appends the bootstrap path after trimming trailing slashes", () => {
    expect(buildPreviewBootstrapSrc("https://abc.preview.143.dev///")).toBe(
      "https://abc.preview.143.dev/bootstrap",
    );
  });
});

describe("previewBootstrapTimeoutDetails", () => {
  it("uses the default timeout copy and error constant", () => {
    expect(PREVIEW_BOOTSTRAP_TIMEOUT_ERROR).toContain("timed out");
    expect(previewBootstrapTimeoutDetails()).toContain("within 5 seconds");
  });

  it("formats custom timeout durations in seconds", () => {
    expect(previewBootstrapTimeoutDetails(2_400)).toContain("within 2 seconds");
    expect(previewBootstrapTimeoutDetails(2_600)).toContain("within 3 seconds");
  });
});
