import { describe, it, expect } from "vitest";
import { capitalizeWords, formatTimeAgo, isImageURL, fileNameFromURL } from "./utils";

describe("capitalizeWords", () => {
  it("capitalizes each word in a space-delimited string", () => {
    expect(capitalizeWords("chatgpt plus")).toBe("Chatgpt Plus");
  });

  it("replaces underscores with spaces before capitalizing", () => {
    expect(capitalizeWords("needs_reauth")).toBe("Needs Reauth");
  });

  it("returns an empty string unchanged", () => {
    expect(capitalizeWords("")).toBe("");
  });
});

describe("formatTimeAgo", () => {
  it("returns 'just now' for dates less than a minute ago", () => {
    const now = new Date().toISOString();
    expect(formatTimeAgo(now)).toBe("just now");
  });

  it("returns minutes ago", () => {
    const fiveMinAgo = new Date(Date.now() - 5 * 60_000).toISOString();
    expect(formatTimeAgo(fiveMinAgo)).toBe("5m ago");
  });

  it("returns '1m ago' at the minute boundary", () => {
    const oneMinAgo = new Date(Date.now() - 60_000).toISOString();
    expect(formatTimeAgo(oneMinAgo)).toBe("1m ago");
  });

  it("returns hours ago", () => {
    const twoHoursAgo = new Date(Date.now() - 2 * 3_600_000).toISOString();
    expect(formatTimeAgo(twoHoursAgo)).toBe("2h ago");
  });

  it("returns days ago for dates within 30 days", () => {
    const threeDaysAgo = new Date(Date.now() - 3 * 86_400_000).toISOString();
    expect(formatTimeAgo(threeDaysAgo)).toBe("3d ago");
  });

  it("returns a formatted date for dates older than 30 days", () => {
    const fiftyDaysAgo = new Date(Date.now() - 50 * 86_400_000).toISOString();
    const result = formatTimeAgo(fiftyDaysAgo);
    // Should not be "Xd ago" format — should be a locale date string
    expect(result).not.toMatch(/\d+d ago/);
    expect(result.length).toBeGreaterThan(0);
  });

  it("returns '59m ago' just under an hour", () => {
    const fiftyNineMinAgo = new Date(Date.now() - 59 * 60_000).toISOString();
    expect(formatTimeAgo(fiftyNineMinAgo)).toBe("59m ago");
  });

  it("returns seconds ago when requested for very recent timestamps", () => {
    const tenSecondsAgo = new Date(Date.now() - 10_000).toISOString();
    expect(formatTimeAgo(tenSecondsAgo, { includeSeconds: true })).toBe("10s ago");
  });

  it("returns '23h ago' just under a day", () => {
    const twentyThreeHoursAgo = new Date(Date.now() - 23 * 3_600_000).toISOString();
    expect(formatTimeAgo(twentyThreeHoursAgo)).toBe("23h ago");
  });

  it("returns a custom fallback when provided", () => {
    expect(formatTimeAgo(undefined, { fallback: "Syncing" })).toBe("Syncing");
  });

  it("returns an em-dash for missing input (rollback safety)", () => {
    expect(formatTimeAgo(undefined)).toBe("—");
    expect(formatTimeAgo(null)).toBe("—");
    expect(formatTimeAgo("")).toBe("—");
    expect(formatTimeAgo("not-a-date")).toBe("—");
  });
});

describe("isImageURL", () => {
  it("matches common image extensions", () => {
    expect(isImageURL("/uploads/photo.png")).toBe(true);
    expect(isImageURL("/uploads/photo.jpg")).toBe(true);
    expect(isImageURL("/uploads/photo.jpeg")).toBe(true);
    expect(isImageURL("/uploads/photo.gif")).toBe(true);
    expect(isImageURL("/uploads/photo.webp")).toBe(true);
    expect(isImageURL("/uploads/photo.svg")).toBe(true);
  });

  it("matches data: image URLs", () => {
    expect(isImageURL("data:image/png;base64,abc")).toBe(true);
  });

  it("rejects non-image URLs", () => {
    expect(isImageURL("/uploads/doc.pdf")).toBe(false);
    expect(isImageURL("/uploads/file.txt")).toBe(false);
    expect(isImageURL("/uploads/data.json")).toBe(false);
  });

  it("strips query params from S3 presigned URLs", () => {
    expect(isImageURL("https://bucket.s3.amazonaws.com/uploads/photo.png?X-Amz-Algorithm=AWS4")).toBe(true);
    expect(isImageURL("https://bucket.s3.amazonaws.com/uploads/doc.pdf?X-Amz-Algorithm=AWS4")).toBe(false);
  });

  it("strips fragments", () => {
    expect(isImageURL("/uploads/photo.jpg#section")).toBe(true);
  });

  it("is case-insensitive", () => {
    expect(isImageURL("/uploads/PHOTO.PNG")).toBe(true);
    expect(isImageURL("/uploads/photo.JPG")).toBe(true);
  });
});

describe("fileNameFromURL", () => {
  it("extracts filename from simple path", () => {
    expect(fileNameFromURL("/uploads/org-1/photo.png")).toBe("photo.png");
  });

  it("strips query params", () => {
    expect(fileNameFromURL("https://s3.amazonaws.com/uploads/photo.png?token=abc")).toBe("photo.png");
  });

  it("strips fragments", () => {
    expect(fileNameFromURL("/uploads/photo.png#section")).toBe("photo.png");
  });

  it("returns 'file' for empty paths", () => {
    expect(fileNameFromURL("")).toBe("file");
  });
});
